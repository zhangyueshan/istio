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

package k8sversion

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/version"
)

var (
	version1_17 = &version.Info{
		Major:      "1",
		Minor:      "17",
		GitVersion: "1.17",
	}
	version1_8 = &version.Info{
		Major:      "1",
		Minor:      "8",
		GitVersion: "1.8",
	}
	version1_17GKE = &version.Info{
		Major:      "1",
		Minor:      "17+",
		GitVersion: "v1.17.7-gke.10",
	}
	version1_8GKE = &version.Info{
		Major:      "1",
		Minor:      "8",
		GitVersion: "v1.8.7-gke.8",
	}
	versionInvalid1 = &version.Info{
		Major:      "1",
		Minor:      "7",
		GitVersion: "v1.invalid.7",
	}
	versionInvalid2 = &version.Info{
		Major:      "one",
		Minor:      "seven",
		GitVersion: "one.seven",
	}
)

func TestExtractKubernetesVersion(t *testing.T) {
	cases := []struct {
		version *version.Info
		errMsg  error
		isValid bool
	}{
		{
			version: version1_17,
			errMsg:  nil,
			isValid: true,
		},
		{
			version: version1_8,
			errMsg:  nil,
			isValid: true,
		},
		{
			version: version1_17GKE,
			errMsg:  nil,
			isValid: true,
		},
		{
			version: version1_8GKE,
			errMsg:  nil,
			isValid: true,
		},
		{
			version: versionInvalid1,
			errMsg:  fmt.Errorf("the version %q is invalid", versionInvalid1.GitVersion),
			isValid: false,
		},
		{
			version: versionInvalid2,
			errMsg:  fmt.Errorf("could not parse %q as version", versionInvalid2.GitVersion),
			isValid: false,
		},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("case %d %s", i, c.version), func(t *testing.T) {
			_, err := extractKubernetesVersion(c.version)
			if c.errMsg != err && c.isValid {
				t.Fatalf("\nwanted: %v \nbut found: %v", c.errMsg, err)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		version *version.Info
		value   int64
		isValid bool
	}{
		{
			version: version1_17,
			value:   10017,
			isValid: true,
		},
		{
			version: version1_8,
			value:   10008,
			isValid: true,
		},
		{
			version: version1_17GKE,
			value:   0,
			isValid: false,
		},
		{
			version: version1_8GKE,
			value:   0,
			isValid: false,
		},
		{
			version: versionInvalid1,
			value:   0,
			isValid: false,
		},
		{
			version: versionInvalid2,
			value:   0,
			isValid: false,
		},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("case %d %s", i, c.version), func(t *testing.T) {
			val := parseVersion(c.version.GitVersion, 4)
			if c.value != val && c.isValid {
				t.Fatalf("\nwanted: %v \nbut found: %v", c.value, val)
			}
		})
	}
}
