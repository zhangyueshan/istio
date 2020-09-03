// Copyright Istio Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hashicorp/go-multierror"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	operator_istio "istio.io/istio/operator/pkg/apis/istio"
	"istio.io/istio/operator/pkg/name"
	"istio.io/istio/operator/pkg/util"
	operator_validate "istio.io/istio/operator/pkg/validate"
	"istio.io/istio/pilot/pkg/serviceregistry/kube/controller"
	"istio.io/istio/pkg/config"
	"istio.io/istio/pkg/config/protocol"
	"istio.io/istio/pkg/config/schema/collection"
	"istio.io/istio/pkg/config/schema/collections"
	"istio.io/istio/pkg/url"
	"istio.io/pkg/log"
)

var (
	errMissingFilename = errors.New(`error: you must specify resources by --filename.
Example resource specifications include:
   '-f rsrc.yaml'
   '--filename=rsrc.json'`)

	validFields = map[string]struct{}{
		"apiVersion": {},
		"kind":       {},
		"metadata":   {},
		"spec":       {},
		"status":     {},
	}

	istioDeploymentLabel = []string{
		"app",
		"version",
	}
	serviceProtocolUDP = "UDP"
)

type validator struct {
}

func checkFields(un *unstructured.Unstructured) error {
	var errs error
	for key := range un.Object {
		if _, ok := validFields[key]; !ok {
			errs = multierror.Append(errs, fmt.Errorf("unknown field %q", key))
		}
	}
	return errs
}

func (v *validator) validateResource(istioNamespace string, un *unstructured.Unstructured) error {
	gvk := config.GroupVersionKind{
		Group:   un.GroupVersionKind().Group,
		Version: un.GroupVersionKind().Version,
		Kind:    un.GroupVersionKind().Kind,
	}
	// TODO(jasonwzm) remove this when multi-version is supported. v1beta1 shares the same
	// schema as v1lalpha3. Fake conversion and validate against v1alpha3.
	if gvk.Group == name.NetworkingAPIGroupName && gvk.Version == "v1beta1" {
		gvk.Version = "v1alpha3"
	}
	schema, exists := collections.Pilot.FindByGroupVersionKind(gvk)
	if exists {
		obj, err := convertObjectFromUnstructured(schema, un, "")
		if err != nil {
			return fmt.Errorf("cannot parse proto message: %v", err)
		}
		if err = checkFields(un); err != nil {
			return err
		}
		return schema.Resource().ValidateConfig(*obj)
	}

	var errs error
	if un.IsList() {
		_ = un.EachListItem(func(item runtime.Object) error {
			castItem := item.(*unstructured.Unstructured)
			if castItem.GetKind() == name.ServiceStr {
				err := v.validateServicePortPrefix(istioNamespace, castItem)
				if err != nil {
					errs = multierror.Append(errs, err)
				}
			}
			if castItem.GetKind() == name.DeploymentStr {
				v.validateDeploymentLabel(istioNamespace, castItem)
			}
			return nil
		})
	}

	if errs != nil {
		return errs
	}
	if un.GetKind() == name.ServiceStr {
		return v.validateServicePortPrefix(istioNamespace, un)
	}

	if un.GetKind() == name.DeploymentStr {
		v.validateDeploymentLabel(istioNamespace, un)
		return nil
	}

	if un.GetAPIVersion() == "install.istio.io/v1alpha1" {
		if un.GetKind() == "IstioOperator" {
			if err := checkFields(un); err != nil {
				return err
			}
			// IstioOperator isn't part of pkg/config/schema/collections,
			// usual conversion not available.  Convert unstructured to string
			// and ask operator code to check.
			un.SetCreationTimestamp(metav1.Time{}) // UnmarshalIstioOperator chokes on these
			by := util.ToYAML(un)
			iop, err := operator_istio.UnmarshalIstioOperator(by, false)
			if err != nil {
				return err
			}
			return operator_validate.CheckIstioOperator(iop, true)
		}
	}

	// Didn't really validate.  This is OK, as we often get non-Istio Kubernetes YAML
	// we can't complain about.

	return nil
}

func (v *validator) validateServicePortPrefix(istioNamespace string, un *unstructured.Unstructured) error {
	var errs error
	if un.GetNamespace() == handleNamespace(istioNamespace) {
		return nil
	}
	spec := un.Object["spec"].(map[string]interface{})
	if _, ok := spec["ports"]; ok {
		ports := spec["ports"].([]interface{})
		for _, port := range ports {
			p := port.(map[string]interface{})
			if p["protocol"] != nil && strings.EqualFold(p["protocol"].(string), serviceProtocolUDP) {
				continue
			}
			if p["name"] == nil {
				errs = multierror.Append(errs, fmt.Errorf("service %q has an unnamed port. This is not recommended,"+
					" See "+url.DeploymentRequirements, fmt.Sprintf("%s/%s/:", un.GetName(), un.GetNamespace())))
				continue
			}
			if servicePortPrefixed(p["name"].(string)) {
				errs = multierror.Append(errs, fmt.Errorf("service %q port %q does not follow the Istio naming convention."+
					" See "+url.DeploymentRequirements, fmt.Sprintf("%s/%s/:", un.GetName(), un.GetNamespace()), p["name"].(string)))
			}
		}
	}
	if errs != nil {
		return errs
	}
	return nil
}

func (v *validator) validateDeploymentLabel(istioNamespace string, un *unstructured.Unstructured) {
	if un.GetNamespace() == handleNamespace(istioNamespace) {
		return
	}
	labels := un.GetLabels()
	for _, l := range istioDeploymentLabel {
		if _, ok := labels[l]; !ok {
			log.Warnf("deployment %q may not provide Istio metrics and telemetry without label %q."+
				" See "+url.DeploymentRequirements, fmt.Sprintf("%s/%s:", un.GetName(), un.GetNamespace()), l)
		}
	}
}

func (v *validator) validateFile(istioNamespace *string, reader io.Reader) error {
	decoder := yaml.NewDecoder(reader)
	decoder.SetStrict(true)
	var errs error
	for {
		// YAML allows non-string keys and the produces generic keys for nested fields
		raw := make(map[interface{}]interface{})
		err := decoder.Decode(&raw)
		if err == io.EOF {
			return errs
		}
		if err != nil {
			errs = multierror.Append(errs, err)
			return errs
		}
		if len(raw) == 0 {
			continue
		}
		out := transformInterfaceMap(raw)
		un := unstructured.Unstructured{Object: out}
		err = v.validateResource(*istioNamespace, &un)
		if err != nil {
			errs = multierror.Append(errs, multierror.Prefix(err, fmt.Sprintf("%s/%s/%s:",
				un.GetKind(), un.GetNamespace(), un.GetName())))
		}
	}
}

func validateFiles(istioNamespace *string, filenames []string, writer io.Writer) error {
	if len(filenames) == 0 {
		return errMissingFilename
	}

	v := &validator{}

	var errs, err error
	var reader io.Reader
	for _, filename := range filenames {
		if filename == "-" {
			reader = os.Stdin
		} else {
			reader, err = os.Open(filename)
		}
		if err != nil {
			errs = multierror.Append(errs, fmt.Errorf("cannot read file %q: %v", filename, err))
			continue
		}
		err = v.validateFile(istioNamespace, reader)
		if err != nil {
			errs = multierror.Append(errs, err)
		}
	}
	if errs != nil {
		return errs
	}
	for _, fname := range filenames {
		if fname == "-" {
			_, _ = fmt.Fprintf(writer, "validation succeed\n")
			break
		} else {
			_, _ = fmt.Fprintf(writer, "%q is valid\n", fname)
		}
	}

	return nil
}

// NewValidateCommand creates a new command for validating Istio k8s resources.
func NewValidateCommand(istioNamespace *string) *cobra.Command {
	var filenames []string
	var referential bool

	c := &cobra.Command{
		Use:     "validate -f FILENAME [options]",
		Aliases: []string{"v"},
		Short:   "Validate Istio policy and rules files",
		Example: `
		# Validate bookinfo-gateway.yaml
		istioctl validate -f samples/bookinfo/networking/bookinfo-gateway.yaml

		# Validate bookinfo-gateway.yaml with shorthand syntax
		istioctl v -f samples/bookinfo/networking/bookinfo-gateway.yaml
		
		# Validate current deployments under 'default' namespace within the cluster
		kubectl get deployments -o yaml | istioctl validate -f -

		# Validate current services under 'default' namespace within the cluster
		kubectl get services -o yaml | istioctl validate -f -

		# Also see the related command 'istioctl analyze'
		istioctl analyze samples/bookinfo/networking/bookinfo-gateway.yaml
`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return validateFiles(istioNamespace, filenames, c.OutOrStderr())
		},
	}

	flags := c.PersistentFlags()
	flags.StringSliceVarP(&filenames, "filename", "f", nil, "Names of files to validate")
	flags.BoolVarP(&referential, "referential", "x", true, "Enable structural validation for policy and telemetry")

	return c
}

func transformInterfaceArray(in []interface{}) []interface{} {
	out := make([]interface{}, len(in))
	for i, v := range in {
		out[i] = transformMapValue(v)
	}
	return out
}

func transformInterfaceMap(in map[interface{}]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[fmt.Sprintf("%v", k)] = transformMapValue(v)
	}
	return out
}

func transformMapValue(in interface{}) interface{} {
	switch v := in.(type) {
	case []interface{}:
		return transformInterfaceArray(v)
	case map[interface{}]interface{}:
		return transformInterfaceMap(v)
	default:
		return v
	}
}

func servicePortPrefixed(n string) bool {
	i := strings.IndexByte(n, '-')
	if i >= 0 {
		n = n[:i]
	}
	p := protocol.Parse(n)
	return p == protocol.Unsupported
}

func handleNamespace(istioNamespace string) string {
	if istioNamespace == "" {
		istioNamespace = controller.IstioNamespace
	}
	return istioNamespace
}

// TODO(nmittler): Remove this once Pilot migrates to galley schema.
func convertObjectFromUnstructured(schema collection.Schema, un *unstructured.Unstructured, domain string) (*config.Config, error) {
	data, err := fromSchemaAndJSONMap(schema, un.Object["spec"])
	if err != nil {
		return nil, err
	}

	return &config.Config{
		Meta: config.Meta{
			GroupVersionKind:  schema.Resource().GroupVersionKind(),
			Name:              un.GetName(),
			Namespace:         un.GetNamespace(),
			Domain:            domain,
			Labels:            un.GetLabels(),
			Annotations:       un.GetAnnotations(),
			ResourceVersion:   un.GetResourceVersion(),
			CreationTimestamp: un.GetCreationTimestamp().Time,
		},
		Spec: data,
	}, nil
}

// TODO(nmittler): Remove this once Pilot migrates to galley schema.
func fromSchemaAndJSONMap(schema collection.Schema, data interface{}) (config.Spec, error) {
	// Marshal to json bytes
	str, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	out, err := schema.Resource().NewInstance()
	if err != nil {
		return nil, err
	}
	if err = config.ApplyJSONStrict(out, string(str)); err != nil {
		return nil, err
	}
	return out, nil
}
