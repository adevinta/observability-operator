package controllers

import (
	"bytes"
	"context"
	"flag"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"os"
	"os/exec"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var verifyAlloyRendering = flag.Bool("verify-alloy", false, "use the alloy binary to verify rendered template correctness")

func setupAlloyLinter(t *testing.T, buffer []byte) (*exec.Cmd, *strings.Builder) {
	t.Helper()
	paths := []string{
		"./alloy-linux-amd64",
		"./alloy",
		"alloy-linux-amd64",
		"alloy",
	}

	var path string
	var errors []error
	for _, p := range paths {
		np, err := exec.LookPath(p)
		if err != nil {
			errors = append(errors, err)
			continue
		}

		path = np
		break
	}
	if path == "" {
		for _, err := range errors {
			assert.NoError(t, err)
		}
		t.Fail()
	}
	cmd := exec.Command(path, "fmt")
	cmd.Stdin = bytes.NewReader(buffer)
	var out strings.Builder
	cmd.Stdout = &out
	return cmd, &out
}

func TestAlloyConfigTemplateCorrectness(t *testing.T) {
	// This test verifies that changes to the template result in valid, executable templates
	path, err := os.MkdirTemp("", "alloy-lint-*")
	require.NoError(t, err)
	defer os.RemoveAll(path)

	tpl := template.New("alloy-config")
	_, err = tpl.Parse(alloyConfigTemplate)
	require.NoError(t, err, "couldn't parse Alloy configuration template successfully")

	t.Run("test alloy config file generated with a single credential is syntactically correct", func(t *testing.T) {
		config := alloyConfig{
			Credentials: []OTelCredentials{
				{
					User:     "123",
					Password: `"my-pass"`,
					Endpoint: `"my.domain.tld/otlp"`,
				},
			},
		}
		var buffer bytes.Buffer
		err = tpl.Execute(&buffer, config)
		require.NoError(t, err, "couldn't execute Alloy configuration template successfully")

		if *verifyAlloyRendering {
			cmd, out := setupAlloyLinter(t, buffer.Bytes())
			err = cmd.Run()
			require.NoError(t, err)
			assert.Equal(t, buffer.String(), out.String())
		}
	})
	t.Run("test alloy config file generated with multiple credentials is syntactically correct", func(t *testing.T) {
		config := alloyConfig{
			Credentials: []OTelCredentials{
				{
					User:     "123",
					Password: `"my-pass"`,
					Endpoint: `"my.domain.tld/otlp"`,
				},
				{
					User:     "456",
					Password: `"my-other-pass"`,
					Endpoint: `"my.other.domain.tld/otlp"`,
				},
			},
		}
		var buffer bytes.Buffer
		err = tpl.Execute(&buffer, config)
		require.NoError(t, err, "couldn't execute Alloy configuration template successfully")

		if *verifyAlloyRendering {
			cmd, out := setupAlloyLinter(t, buffer.Bytes())
			err = cmd.Run()
			require.NoError(t, err)
			assert.Equal(t, buffer.String(), out.String())
		}
	})
}

func TestAlloyConfigSupportsMultipleDestinations(t *testing.T) {
	credentials := []OTelCredentials{
		{
			User:     "123",
			Password: `"my-pass"`,
			Endpoint: `"my.domain.tld/otlp"`,
		},
		{
			User:     "456",
			Password: `"my-other-pass"`,
			Endpoint: `"my.other.domain.tld/otlp"`,
		},
	}

	t.Run("when there are multiple destinations all of them appear", func(t *testing.T) {
		tpl := template.New("alloy-config")
		_, err := tpl.Parse(alloyConfigTemplate)
		require.NoError(t, err, "couldn't parse Alloy configuration template successfully")

		var buffer bytes.Buffer
		err = tpl.Execute(&buffer, alloyConfig{Credentials: credentials})
		require.NoError(t, err, "couldn't execute Alloy configuration template successfully")

		assert.Contains(t, buffer.String(), "otelcol.exporter.otlphttp.default_0.input")
		assert.Contains(t, buffer.String(), `otelcol.auth.basic "credentials_0"`)
		assert.Contains(t, buffer.String(), "username = "+credentials[0].User)
		assert.Contains(t, buffer.String(), "password = "+credentials[0].Password)
		assert.Contains(t, buffer.String(), `otelcol.exporter.otlphttp "default_0"`)
		assert.Contains(t, buffer.String(), "endpoint = "+credentials[0].Endpoint)
		assert.Contains(t, buffer.String(), "auth     = otelcol.auth.basic.credentials_0.handler")

		assert.Contains(t, buffer.String(), "otelcol.exporter.otlphttp.default_1.input")
		assert.Contains(t, buffer.String(), `otelcol.auth.basic "credentials_1"`)
		assert.Contains(t, buffer.String(), "username = "+credentials[1].User)
		assert.Contains(t, buffer.String(), "password = "+credentials[1].Password)
		assert.Contains(t, buffer.String(), `otelcol.exporter.otlphttp "default_1"`)
		assert.Contains(t, buffer.String(), "endpoint = "+credentials[1].Endpoint)
		assert.Contains(t, buffer.String(), "auth     = otelcol.auth.basic.credentials_1.handler")
	})
	t.Run("when there's only one destination the others aren't there", func(t *testing.T) {
		tpl := template.New("alloy-config")
		_, err := tpl.Parse(alloyConfigTemplate)
		require.NoError(t, err, "couldn't parse Alloy configuration template successfully")

		var buffer bytes.Buffer
		err = tpl.Execute(&buffer, alloyConfig{Credentials: []OTelCredentials{credentials[0]}})
		require.NoError(t, err, "couldn't execute Alloy configuration template successfully")

		assert.Contains(t, buffer.String(), "otelcol.exporter.otlphttp.default_0.input")
		assert.Contains(t, buffer.String(), `otelcol.auth.basic "credentials_0"`)
		assert.Contains(t, buffer.String(), "username = "+credentials[0].User)
		assert.Contains(t, buffer.String(), "password = "+credentials[0].Password)
		assert.Contains(t, buffer.String(), `otelcol.exporter.otlphttp "default_0"`)
		assert.Contains(t, buffer.String(), "endpoint = "+credentials[0].Endpoint)
		assert.Contains(t, buffer.String(), "auth     = otelcol.auth.basic.credentials_0.handler")

		assert.NotContains(t, buffer.String(), "otelcol.exporter.otlphttp.default_1.input")
		assert.NotContains(t, buffer.String(), `otelcol.auth.basic "credentials_1"`)
		assert.NotContains(t, buffer.String(), "username = "+credentials[1].User)
		assert.NotContains(t, buffer.String(), "password = "+credentials[1].Password)
		assert.NotContains(t, buffer.String(), `otelcol.exporter.otlphttp "default_1"`)
		assert.NotContains(t, buffer.String(), "endpoint = "+credentials[1].Endpoint)
		assert.NotContains(t, buffer.String(), "auth     = otelcol.auth.basic.credentials_1.handler")
	})
}

func TestSecretUpdate(t *testing.T) {
	tracesNs := newNamespace("observability")
	ns := newNamespace("my-namespace-dev")
	gcToken := "grafana_traces_token"
	gcOtherToken := "grafana_traces_token_2"

	t.Run("verify the grafana token is updated in the cluster", func(t *testing.T) {
		verifySecret := func(reconciler *NamespaceReconciler, secretName, token string) {
			secrets := &corev1.SecretList{}

			err := reconciler.Client.List(context.Background(), secrets, client.InNamespace(tracesNs.Name))
			require.NoError(t, err)

			require.Len(t, secrets.Items, 1)
			require.Equal(t, secretName, secrets.Items[0].Name)
			require.Contains(t, secrets.Items[0].Data, "grafana-cloud-traces-token")
			assert.Equal(t, token, string(secrets.Items[0].Data["grafana-cloud-traces-token"]))
		}

		reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs)

		tc, err := NewTracesCollector(ns.Name, tracesNs.Name, "", false)
		require.NoError(t, err)

		err = tc.CreateOrUpdateSecret(context.Background(), reconciler.Client, gcToken)
		require.NoError(t, err)

		verifySecret(reconciler, tc.objName(), gcToken)

		err = tc.CreateOrUpdateSecret(context.Background(), reconciler.Client, gcOtherToken)
		require.NoError(t, err)

		verifySecret(reconciler, tc.objName(), gcOtherToken)
	})
}

func TestConfigMapUpdate(t *testing.T) {
	tracesNs := newNamespace("observability")
	ns := newNamespace("my-namespace-dev")

	t.Run("verify configmap is updated in the cluster", func(t *testing.T) {
		verifyConfigMap := func(reconciler *NamespaceReconciler, name, config, token string) {
			configmaps := &corev1.ConfigMapList{}

			err := reconciler.Client.List(context.Background(), configmaps, client.InNamespace(tracesNs.Name))
			require.NoError(t, err)

			require.Len(t, configmaps.Items, 1)
			require.Equal(t, name, configmaps.Items[0].Name)
			require.Contains(t, configmaps.Items[0].Data, "config.alloy")
			assert.Equal(t, config, string(configmaps.Items[0].Data["config.alloy"]))
		}

		reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs)

		tc, err := NewTracesCollector(ns.Name, tracesNs.Name, "", false)
		require.NoError(t, err)

		err = tc.CreateOrUpdateConfigMap(context.Background(), reconciler.Client)
		require.NoError(t, err)

		verifyConfigMap(reconciler, tc.objName(), "", tc.templatedConfig)

		tc.templatedConfig = "test"
		err = tc.CreateOrUpdateConfigMap(context.Background(), reconciler.Client)
		require.NoError(t, err)

		verifyConfigMap(reconciler, tc.objName(), "test", tc.templatedConfig)
	})
}

func TestServiceUpdate(t *testing.T) {
	tracesNs := newNamespace("observability")
	ns := newNamespace("my-namespace-dev")

	t.Run("verify service is updated in the cluster", func(t *testing.T) {
		verifyService := func(reconciler *NamespaceReconciler, name string, servicePorts []servicePort) {
			service := &corev1.ServiceList{}

			err := reconciler.Client.List(context.Background(), service, client.InNamespace(tracesNs.Name))
			require.NoError(t, err)

			require.Len(t, service.Items, 1)
			require.Equal(t, name, service.Items[0].Name)
			for _, sp := range servicePorts {
				assert.Contains(t, service.Items[0].Spec.Ports, corev1.ServicePort{
					Name:     name + "-" + sp.Name,
					Protocol: corev1.ProtocolTCP,
					Port:     sp.Port,
					TargetPort: intstr.IntOrString{
						IntVal: sp.Port,
					},
				})
			}
		}

		reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs)

		tc, err := NewTracesCollector(ns.Name, tracesNs.Name, "", false)
		require.NoError(t, err)

		err = tc.CreateOrUpdateService(context.Background(), reconciler.Client)
		require.NoError(t, err)

		verifyService(reconciler, tc.svcName(), []servicePort{})

		servicePorts := []servicePort{
			{
				Name: "test",
				Port: int32(1234),
			},
		}
		tc.ServicePorts = servicePorts

		err = tc.CreateOrUpdateService(context.Background(), reconciler.Client)
		require.NoError(t, err)

		verifyService(reconciler, tc.svcName(), servicePorts)
	})
}

func TestDeploymentUpdate(t *testing.T) {
	tracesNs := newNamespace("observability")
	ns := newNamespace("my-namespace-dev")

	t.Run("verify deployment is updated in the cluster", func(t *testing.T) {
		verifyDeployment := func(reconciler *NamespaceReconciler, name string, servicePorts []servicePort) {
			deployment := &appsv1.DeploymentList{}

			err := reconciler.Client.List(context.Background(), deployment, client.InNamespace(tracesNs.Name))
			require.NoError(t, err)

			require.Len(t, deployment.Items, 1)
			require.Equal(t, name, deployment.Items[0].Name)
			require.Len(t, deployment.Items[0].Spec.Template.Spec.Containers, 2)
			for _, sp := range servicePorts {
				assert.Contains(t, deployment.Items[0].Spec.Template.Spec.Containers[0].Ports, v1.ContainerPort{
					Name:          sp.Name,
					ContainerPort: sp.Port,
				})
			}
		}

		reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs)

		tc, err := NewTracesCollector(ns.Name, tracesNs.Name, "", false)
		require.NoError(t, err)

		err = tc.CreateOrUpdateDeployment(context.Background(), reconciler.Client)
		require.NoError(t, err)

		verifyDeployment(reconciler, tc.objName(), []servicePort{})

		servicePorts := []servicePort{
			{
				Name: "test",
				Port: int32(1234),
			},
		}
		tc.ServicePorts = servicePorts

		err = tc.CreateOrUpdateDeployment(context.Background(), reconciler.Client)
		require.NoError(t, err)

		verifyDeployment(reconciler, tc.objName(), servicePorts)
	})
}

func TestNetworkPolicyUpdate(t *testing.T) {
	tracesNs := newNamespace("observability")
	ns := newNamespace("my-namespace-dev")

	t.Run("verify network policy is updated in the cluster", func(t *testing.T) {
		verifyNetworkPolicy := func(reconciler *NamespaceReconciler, name string, servicePorts []servicePort) {
			networkPolicy := &networkingv1.NetworkPolicy{}

			err := reconciler.Client.Get(context.Background(), types.NamespacedName{Namespace: tracesNs.Name, Name: name}, networkPolicy)
			require.NoError(t, err)

			if len(servicePorts) > 0 {
				for _, sp := range servicePorts {
					var found bool
					for _, np := range networkPolicy.Spec.Ingress[0].Ports {
						if sp.Port != np.Port.IntVal {
							continue
						}
						found = true
					}
					assert.True(t, found)
				}
			}
		}

		reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs)

		tc, err := NewTracesCollector(ns.Name, tracesNs.Name, "", false)
		require.NoError(t, err)

		err = tc.CreateOrUpdateNetworkPolicy(context.Background(), reconciler.Client)
		require.NoError(t, err)

		verifyNetworkPolicy(reconciler, tc.objName(), []servicePort{})

		servicePorts := []servicePort{
			{
				Name: "test",
				Port: int32(1234),
			},
		}
		tc.ServicePorts = servicePorts

		err = tc.CreateOrUpdateNetworkPolicy(context.Background(), reconciler.Client)
		require.NoError(t, err)

		verifyNetworkPolicy(reconciler, tc.objName(), servicePorts)
	})
}

func TestVPAUpdate(t *testing.T) {
	tracesNs := newNamespace("observability")
	ns := newNamespace("my-namespace-dev")

	t.Run("verify VPA is updated in the cluster", func(t *testing.T) {
		verifyVPA := func(reconciler *NamespaceReconciler, name string) {
			vpa := &vpav1.VerticalPodAutoscalerList{}

			err := reconciler.Client.List(context.Background(), vpa, client.InNamespace(tracesNs.Name))
			require.NoError(t, err)

			require.Len(t, vpa.Items, 1)
			require.Equal(t, name, vpa.Items[0].Name)
		}

		reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs)

		tc, err := NewTracesCollector(ns.Name, tracesNs.Name, "", true)
		require.NoError(t, err)

		err = tc.CreateOrUpdateVPA(context.Background(), reconciler.Client)
		require.NoError(t, err)

		verifyVPA(reconciler, tc.objName())
	})
}
