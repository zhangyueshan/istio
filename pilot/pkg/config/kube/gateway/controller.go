// Copyright Istio Authors
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

package gateway

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	svc "sigs.k8s.io/service-apis/apis/v1alpha1"

	"istio.io/istio/pilot/pkg/model"
	controller2 "istio.io/istio/pilot/pkg/serviceregistry/kube/controller"
	"istio.io/istio/pkg/config"
	"istio.io/istio/pkg/config/schema/collection"
	"istio.io/istio/pkg/config/schema/collections"
	"istio.io/istio/pkg/config/schema/gvk"
	"istio.io/pkg/ledger"
)

var (
	errUnsupportedOp   = fmt.Errorf("unsupported operation: the gateway config store is a read-only view")
	errUnsupportedType = fmt.Errorf("unsupported type: this operation only supports gateway & virtual service resource type")
	_                  = svc.HTTPRoute{}
	_                  = svc.GatewayClass{}
)

type controller struct {
	client kubernetes.Interface
	cache  model.ConfigStoreCache
	domain string
}

func NewController(client kubernetes.Interface, c model.ConfigStoreCache, options controller2.Options) model.ConfigStoreCache {
	return &controller{client, c, options.DomainSuffix}
}

func (c *controller) GetLedger() ledger.Ledger {
	return c.cache.GetLedger()
}

func (c *controller) SetLedger(l ledger.Ledger) error {
	return c.cache.SetLedger(l)
}

func (c *controller) Schemas() collection.Schemas {
	return collection.SchemasFor(
		collections.IstioNetworkingV1Alpha3Virtualservices,
		collections.IstioNetworkingV1Alpha3Gateways,
	)
}

func (c controller) Get(typ config.GroupVersionKind, name, namespace string) *config.Config {
	panic("get is not supported")
}

func (c controller) List(typ config.GroupVersionKind, namespace string) ([]config.Config, error) {
	if typ != gvk.Gateway && typ != gvk.VirtualService {
		return nil, errUnsupportedType
	}

	gatewayClass, err := c.cache.List(collections.K8SServiceApisV1Alpha1Gatewayclasses.Resource().GroupVersionKind(), namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list type GatewayClass: %v", err)
	}
	gateway, err := c.cache.List(collections.K8SServiceApisV1Alpha1Gateways.Resource().GroupVersionKind(), namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list type Gateway: %v", err)
	}
	httpRoute, err := c.cache.List(collections.K8SServiceApisV1Alpha1Httproutes.Resource().GroupVersionKind(), namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list type HTTPRoute: %v", err)
	}
	tcpRoute, err := c.cache.List(collections.K8SServiceApisV1Alpha1Tcproutes.Resource().GroupVersionKind(), namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list type TCPRoute: %v", err)
	}

	nsl, err := c.client.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list type Namespaces: %v", err)
	}
	namespaces := map[string]*corev1.Namespace{}
	for i, ns := range nsl.Items {
		namespaces[ns.Name] = &nsl.Items[i]
	}
	input := &KubernetesResources{
		GatewayClass: gatewayClass,
		Gateway:      gateway,
		HTTPRoute:    httpRoute,
		TCPRoute:     tcpRoute,
		Namespaces:   namespaces,
		Domain:       c.domain,
	}
	output := convertResources(input)

	switch typ {
	case gvk.Gateway:
		return output.Gateway, nil
	case gvk.VirtualService:
		return output.VirtualService, nil
	}
	return nil, errUnsupportedOp
}

func (c controller) Create(config config.Config) (revision string, err error) {
	return "", errUnsupportedOp
}

func (c controller) Update(config config.Config) (newRevision string, err error) {
	return "", errUnsupportedOp
}

func (c controller) Delete(typ config.GroupVersionKind, name, namespace string) error {
	return errUnsupportedOp
}

func (c controller) Version() string {
	return c.cache.Version()
}

func (c controller) GetResourceAtVersion(version string, key string) (resourceVersion string, err error) {
	return c.cache.GetResourceAtVersion(version, key)
}

func (c controller) RegisterEventHandler(typ config.GroupVersionKind, handler func(config.Config, config.Config, model.Event)) {
	c.cache.RegisterEventHandler(typ, func(prev, cur config.Config, event model.Event) {
		handler(prev, cur, event)
	})
}

func (c controller) Run(stop <-chan struct{}) {
}

func (c controller) HasSynced() bool {
	return c.cache.HasSynced()
}
