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

package reachability

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"istio.io/istio/pkg/test/echo/common/scheme"
	"istio.io/istio/pkg/test/framework"
	"istio.io/istio/pkg/test/framework/components/echo"
	"istio.io/istio/pkg/test/framework/components/echo/echoboot"
	"istio.io/istio/pkg/test/framework/components/istioctl"
	"istio.io/istio/pkg/test/framework/components/namespace"
	"istio.io/istio/pkg/test/util/file"
	"istio.io/istio/pkg/test/util/retry"
	"istio.io/istio/tests/integration/security/util"
	"istio.io/istio/tests/integration/security/util/connection"
	"istio.io/pkg/log"
)

// TestCase represents reachability test cases.
type TestCase struct {
	// ConfigFile is the name of the yaml contains the authentication policy and destination rule CRs
	// that are needed for the test setup.
	// The file is expected in the tests/integration/security/reachability/testdata folder.
	ConfigFile string
	Namespace  namespace.Instance

	// CallOpts specified the call options for destination service. If not specified, use the default
	// framework provided ones.
	CallOpts []echo.CallOptions

	// Indicates whether a test should be created for the given configuration.
	Include func(src echo.Instance, opts echo.CallOptions) bool

	// Handler called when the given test is being run.
	OnRun func(ctx framework.TestContext, src echo.Instance, opts echo.CallOptions)

	// Indicates whether the test should expect a successful response.
	ExpectSuccess func(src echo.Instance, opts echo.CallOptions) bool
}

// Context is a context for reachability tests.
type Context struct {
	ctx           framework.TestContext
	Namespace     namespace.Instance
	A, B          echo.Instance
	Multiversion  echo.Instance
	Headless      echo.Instance
	Naked         echo.Instance
	VM            echo.Instance
	HeadlessNaked echo.Instance
}

// CreateContext creates and initializes reachability context.
func CreateContext(ctx framework.TestContext, buildVM bool) Context {
	ns := namespace.NewOrFail(ctx, ctx, namespace.Config{
		Prefix: "reachability",
		Inject: true,
	})

	var a, b, multiVersion, headless, naked, vmInstance, headlessNaked echo.Instance

	// Multi-version specific setup
	cfg := util.EchoConfig("multiversion", ns, false, nil)
	cfg.Subsets = []echo.SubsetConfig{
		// Istio deployment, with sidecar.
		{
			Version: "vistio",
		},
		// Legacy deployment subset, does not have sidecar injected.
		{
			Version:     "vlegacy",
			Annotations: echo.NewAnnotations().SetBool(echo.SidecarInject, false),
		},
	}

	// VM specific setup
	vmCfg := util.EchoConfig("vm", ns, false, nil)

	// for test cases that have `buildVM` off, vm will function like a regular pod
	vmCfg.DeployAsVM = buildVM

	echoboot.NewBuilder(ctx).
		With(&a, util.EchoConfig("a", ns, false, nil)).
		With(&b, util.EchoConfig("b", ns, false, nil)).
		With(&multiVersion, cfg).
		With(&headless, util.EchoConfig("headless", ns, true, nil)).
		With(&naked, util.EchoConfig("naked", ns, false, echo.NewAnnotations().
			SetBool(echo.SidecarInject, false))).
		With(&vmInstance, vmCfg).
		With(&headlessNaked, util.EchoConfig("headless-naked", ns, true, echo.NewAnnotations().
			SetBool(echo.SidecarInject, false))).
		BuildOrFail(ctx)

	return Context{
		ctx:           ctx,
		Namespace:     ns,
		A:             a,
		B:             b,
		Multiversion:  multiVersion,
		Headless:      headless,
		Naked:         naked,
		VM:            vmInstance,
		HeadlessNaked: headlessNaked,
	}
}

// Run runs the given reachability test cases with the context.
func (rc *Context) Run(testCases []TestCase) {
	callOptions := []echo.CallOptions{
		{
			PortName: "http",
			Scheme:   scheme.HTTP,
		},
		{
			PortName: "http",
			Scheme:   scheme.WebSocket,
		},
		{
			PortName: "tcp",
			Scheme:   scheme.TCP,
		},
		{
			PortName: "grpc",
			Scheme:   scheme.GRPC,
		},
	}

	ik := istioctl.NewOrFail(rc.ctx, rc.ctx, istioctl.Config{})
	for _, c := range testCases {
		// Create a copy to avoid races, as tests are run in parallel
		c := c
		testName := strings.TrimSuffix(c.ConfigFile, filepath.Ext(c.ConfigFile))
		test := rc.ctx.NewSubTest(testName)

		test.Run(func(ctx framework.TestContext) {
			// Apply the policy.
			policyYAML := file.AsStringOrFail(ctx, filepath.Join("./testdata", c.ConfigFile))
			retry.UntilSuccessOrFail(ctx, func() error {
				ctx.Logf("[%s] [%v] Apply config %s", testName, time.Now(), c.ConfigFile)
				// TODO(https://github.com/istio/istio/issues/20460) We shouldn't need a retry loop
				return rc.ctx.Config().ApplyYAML(c.Namespace.Name(), policyYAML)
			})
			ctx.Logf("[%s] [%v] Wait for config propagate to endpoints...", testName, time.Now())
			t0 := time.Now()
			if err := ik.WaitForConfigs(c.Namespace.Name(), policyYAML); err != nil {
				// Continue anyways, so we can assess the effectiveness of using `istioctl wait`
				ctx.Logf("warning: failed to wait for config: %v", err)
				// Get proxy status for additional debugging
				s, _, _ := ik.Invoke([]string{"ps"})
				ctx.Logf("proxy status: %v", s)
			}
			// TODO(https://github.com/istio/istio/issues/25945) introducing istioctl wait in favor of a 10s sleep lead to flakes
			// to work around this, we will temporarily make sure we are always sleeping at least 10s, even if istioctl wait is faster.
			// This allows us to debug istioctl wait, while still ensuring tests are stable
			sleep := time.Second*10 - time.Since(t0)
			ctx.Logf("[%s] [%v] Wait for additional %v config propagate to endpoints...", testName, time.Now(), sleep)
			time.Sleep(sleep)
			ctx.Logf("[%s] [%v] Finish waiting. Continue testing.", testName, time.Now())
			ctx.Cleanup(func() {
				if err := retry.UntilSuccess(func() error {
					return rc.ctx.Config().DeleteYAML(c.Namespace.Name(), policyYAML)
				}); err != nil {
					log.Errorf("failed to delete configuration: %v", err)
				}
			})

			for _, src := range []echo.Instance{rc.A, rc.B, rc.Headless, rc.Naked, rc.HeadlessNaked} {
				for _, dest := range []echo.Instance{rc.A, rc.B, rc.Headless, rc.Multiversion, rc.Naked, rc.VM, rc.HeadlessNaked} {
					copts := &callOptions
					// If test case specified service call options, use that instead.
					if c.CallOpts != nil {
						copts = &c.CallOpts
					}
					for _, opts := range *copts {
						// Copy the loop variables so they won't change for the subtests.
						src := src
						dest := dest
						opts := opts
						onPreRun := c.OnRun

						// Set the target on the call options.
						opts.Target = dest

						if c.Include(src, opts) {
							expectSuccess := c.ExpectSuccess(src, opts)

							subTestName := fmt.Sprintf("%s->%s://%s:%s%s",
								src.Config().Service,
								opts.Scheme,
								dest.Config().Service,
								opts.PortName,
								opts.Path)

							ctx.NewSubTest(subTestName).
								RunParallel(func(ctx framework.TestContext) {
									if onPreRun != nil {
										onPreRun(ctx, src, opts)
									}

									checker := connection.Checker{
										From:          src,
										Options:       opts,
										ExpectSuccess: expectSuccess,
									}
									checker.CheckOrFail(ctx)
								})
						}
					}
				}
			}
		})
	}
}

func (rc *Context) IsNaked(i echo.Instance) bool {
	return i == rc.HeadlessNaked || i == rc.Naked
}

func (rc *Context) IsHeadless(i echo.Instance) bool {
	return i == rc.HeadlessNaked || i == rc.Headless
}
