package controllers

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apilabels "k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	validatingfield "k8s.io/apimachinery/pkg/util/validation/field"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func createDefaultTracesSecret(tracesNamespace, objectName string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objectName,
			Namespace: tracesNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       objectName,
				"app.kubernetes.io/managed-by": "grafana-cloud-operator",
				"app.kubernetes.io/version":    "v1.2.1",
			},
		},
		Data: map[string][]byte{
			"grafana-cloud-traces-token": []byte("GCO_TRACES_TOKEN"),
		},
		Type: corev1.SecretTypeOpaque,
	}
}

func createDefaultTenantPod(namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-pod",
			Namespace: namespace,
		},
	}
}

func createIgnoredTenantPod(namespace, appLabel string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ignore-me-pod",
			Namespace: namespace,
			Labels: map[string]string{
				"app": appLabel,
			},
		},
	}
}

func TestNamespaceControllerCreatesAlloyTracesCollector(t *testing.T) {
	tests := []struct {
		name            string
		annotations     map[string]string
		expectedObjects int
	}{
		{
			name: "Alloy traces collector objects, are created, when there are workloads in the tenant namespace",
			annotations: map[string]string{
				Config.stackNameAnnotationKey: "adevintaobs",
				Config.tracesAnnotationKey:    "enabled",
			},
		},
	}

	tracesNs := newNamespace("observability")
	ns := newNamespace("teamtest-dev")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns.Annotations = tt.annotations

			reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs, createDefaultTenantPod(ns.Name), createIgnoredTenantPod(ns.Namespace, "ignored-app"))
			path := validatingfield.NewPath("metadata", "labels")
			filter, err := apilabels.Parse("app=ignored-app", validatingfield.WithPath(path))
			require.NoError(t, err)
			reconciler.ExcludeWorkloadLabelSelector = filter
			reconciler.EnableVPA = true

			_, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
			require.NoError(t, err)

			secrets := &corev1.SecretList{}
			err = reconciler.Client.List(context.Background(), secrets, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			require.Len(t, secrets.Items, 1)
			assert.Len(t, secrets.Items[0].Data, 1)
			assert.Contains(t, secrets.Items[0].Data, "grafana-cloud-traces-token")

			services := &corev1.ServiceList{}
			err = reconciler.Client.List(context.Background(), services, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			require.Len(t, services.Items, 1)
			assert.Len(t, services.Items[0].Spec.Ports, 3)

			configMaps := &corev1.ConfigMapList{}
			err = reconciler.Client.List(context.Background(), configMaps, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			require.Len(t, configMaps.Items, 1)
			assert.Len(t, configMaps.Items[0].Data, 1)
			assert.Contains(t, configMaps.Items[0].Data, "config.alloy")

			deployments := &appv1.DeploymentList{}
			err = reconciler.Client.List(context.Background(), deployments, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			require.Len(t, deployments.Items, 1)

			networkPolicies := &networkingv1.NetworkPolicyList{}
			err = reconciler.Client.List(context.Background(), networkPolicies, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			require.Len(t, networkPolicies.Items, 1)
			assert.Len(t, networkPolicies.Items[0].Spec.Ingress, 1)
			assert.Len(t, networkPolicies.Items[0].Spec.Ingress[0].From, 1)
			assert.Len(t, networkPolicies.Items[0].Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels, 1)
			assert.Contains(t, networkPolicies.Items[0].Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels, "kubernetes.io/metadata.name")

			vpas := &vpav1.VerticalPodAutoscalerList{}
			err = reconciler.Client.List(context.Background(), vpas, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			require.Len(t, vpas.Items, 1)
			assert.Equal(t, vpas.Items[0].Spec.TargetRef.Kind, "Deployment")
			assert.Equal(t, vpas.Items[0].Spec.TargetRef.Name, deployments.Items[0].Name)
			assert.Equal(t, *vpas.Items[0].Spec.UpdatePolicy.UpdateMode, vpav1.UpdateMode("Auto"))
		})
	}
}

func TestNamespaceControllerDoesNotCreateAlloyTracesCollector(t *testing.T) {
	tracesNs := newNamespace("observability")
	ns := newNamespace("teamtest-dev")

	ignoreAppName := "ignored-app"

	tests := []struct {
		name                string
		tenantNsAnnotations map[string]string
		pod                 *corev1.Pod
	}{
		{
			name: "Alloy traces collector objects, are not created, when traces annotation value is different than enabled",
			tenantNsAnnotations: map[string]string{
				Config.stackNameAnnotationKey: "adevintaobs",
				Config.tracesAnnotationKey:    "disabled",
			},
		},
		{
			name: "Alloy traces collector objects, are not created, when the traces annotation is missing",
			tenantNsAnnotations: map[string]string{
				Config.stackNameAnnotationKey: "adevintaobs",
			},
		},
		{
			name: "Alloy traces collector objects, are not created, when there are no workloads",
			tenantNsAnnotations: map[string]string{
				Config.stackNameAnnotationKey: "adevintaobs",
				Config.tracesAnnotationKey:    "enabled",
			},
		},
		{
			name: "Alloy traces collector objects, are not created, when the only workloads are ignored",
			tenantNsAnnotations: map[string]string{
				Config.stackNameAnnotationKey: "adevintaobs",
				Config.tracesAnnotationKey:    "enabled",
			},
			pod: createIgnoredTenantPod(ns.Name, ignoreAppName),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns.Annotations = tt.tenantNsAnnotations

			var reconciler *NamespaceReconciler
			if tt.pod != nil {
				reconciler = newDefaultNamespaceReconciler(t, ns, tracesNs, tt.pod)
			} else {
				reconciler = newDefaultNamespaceReconciler(t, ns, tracesNs)
			}
			path := validatingfield.NewPath("metadata", "labels")
			filter, err := apilabels.Parse(fmt.Sprintf("app=%s", ignoreAppName), validatingfield.WithPath(path))
			require.NoError(t, err)
			reconciler.ExcludeWorkloadLabelSelector = filter

			_, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
			require.NoError(t, err)

			secrets := &corev1.SecretList{}
			err = reconciler.Client.List(context.Background(), secrets, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			assert.Len(t, secrets.Items, 0)

			services := &corev1.ServiceList{}
			err = reconciler.Client.List(context.Background(), services, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			assert.Len(t, services.Items, 0)

			configMaps := &corev1.ConfigMapList{}
			err = reconciler.Client.List(context.Background(), configMaps, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			assert.Len(t, configMaps.Items, 0)

			deployments := &appv1.DeploymentList{}
			err = reconciler.Client.List(context.Background(), deployments, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			require.Len(t, deployments.Items, 0)

			networkPolicies := &networkingv1.NetworkPolicyList{}
			err = reconciler.Client.List(context.Background(), networkPolicies, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			assert.Len(t, networkPolicies.Items, 0)

			vpas := &vpav1.VerticalPodAutoscalerList{}
			err = reconciler.Client.List(context.Background(), vpas, client.InNamespace(reconciler.TracesNamespace))
			require.NoError(t, err)
			require.Len(t, vpas.Items, 0)
		})
	}
}

func TestNamespaceControllerAlloyConfiguration(t *testing.T) {
	type mockGrafanaResponses struct {
		StackID int
		URL     string
		Error   error
	}
	tests := []struct {
		name                  string
		annotations           map[string]string
		labels                map[string]string
		objectLabels          map[string]string
		mockgc                mockGrafanaResponses
		expectedConfigMapData []string
	}{
		{
			name: "Alloy traces collector configmap, and secret, have the correct data for the Alloy config. The configmap must have a Grafana stack related to the annotation",
			annotations: map[string]string{
				Config.stackNameAnnotationKey: "adevintadummy",
				Config.tracesAnnotationKey:    "enabled",
			},
			labels: map[string]string{
				"app.kubernetes.io/name":       "alloy-team-test-dev",
				"app.kubernetes.io/managed-by": "grafana-cloud-operator",
				"app.kubernetes.io/version":    "v1.2.1",
			},
			objectLabels: map[string]string{
				"app.kubernetes.io/name":       "alloy-team-test-dev",
				"app.kubernetes.io/managed-by": "grafana-cloud-operator",
				"app.kubernetes.io/version":    "v1.2.1",
			},
			mockgc: mockGrafanaResponses{
				URL: "https://test-otlp-endpoint-adevintadummy.com",
			},
			expectedConfigMapData: []string{"https://test-otlp-endpoint-adevintadummy.com/otlp"},
		},
		{
			name: "Alloy traces collector configmap, and secret, must have the correct data for the Alloy config. The configmap should be configured with a Grafana stack despite not having an annotation",
			annotations: map[string]string{
				Config.tracesAnnotationKey:    "enabled",
				Config.stackNameAnnotationKey: "adevintatest",
			},
			objectLabels: map[string]string{
				"app.kubernetes.io/name":       "alloy-team-test-dev",
				"app.kubernetes.io/managed-by": "grafana-cloud-operator",
				"app.kubernetes.io/version":    "v1.2.1",
			},
			mockgc: mockGrafanaResponses{
				URL: "https://test-otlp-endpoint-adevintateamtest.com",
			},
			expectedConfigMapData: []string{"https://test-otlp-endpoint-adevintateamtest.com/otlp"},
		},
		{
			name: "Alloy traces collector configmap, must have custom resource attributes processor with k8s.cluster.name and k8s.namespace.name",
			annotations: map[string]string{
				Config.tracesAnnotationKey:    "enabled",
				Config.stackNameAnnotationKey: "adevintatest",
			},
			objectLabels: map[string]string{
				"app.kubernetes.io/name":       "alloy-team-test-dev",
				"app.kubernetes.io/managed-by": "grafana-cloud-operator",
				"app.kubernetes.io/version":    "v1.2.1",
			},
			mockgc: mockGrafanaResponses{
				URL: "https://test-otlp-endpoint-adevintadummy.com",
			},
			expectedConfigMapData: []string{
				"otelcol.processor.transform",
				"trace_statements",
				"set(attributes[\"k8s.cluster.name\"], \"clustername\"",
				"set(attributes[\"k8s.namespace.name\"], \"team-test-dev\"",
			},
		},
	}

	tracesNs := newNamespace("observability")
	ns := newNamespace("team-test-dev")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			ns.Labels = tt.labels
			ns.Annotations = tt.annotations

			reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs, createDefaultTenantPod(ns.Name))
			grafanaCloudClient := mockGrafanaCloudClient{
				GetTracesConnectionFunc: func(stack string) (int, string, error) {
					return tt.mockgc.StackID, tt.mockgc.URL, tt.mockgc.Error
				},
			}

			reconciler.GrafanaCloudClient = &grafanaCloudClient

			expectedObjectName := "alloy-" + ns.Name
			expectedTracesNamespace := reconciler.TracesNamespace
			expectedSecret := createDefaultTracesSecret(expectedTracesNamespace, expectedObjectName)

			_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
			require.NoError(t, err)

			secrets := &corev1.SecretList{}
			err = reconciler.Client.List(context.Background(), secrets, client.InNamespace(expectedTracesNamespace))
			require.NoError(t, err)
			require.Len(t, secrets.Items, 1)
			expectedSecret.ResourceVersion = secrets.Items[0].ResourceVersion
			assert.Contains(t, string(secrets.Items[0].Data["grafana-cloud-traces-token"]), "GCO_TRACES_TOKEN")

			assert.Equal(t, *expectedSecret, secrets.Items[0])

			configMaps := &corev1.ConfigMapList{}
			err = reconciler.Client.List(context.Background(), configMaps, client.InNamespace(expectedTracesNamespace))
			require.NoError(t, err)
			for _, expectedData := range tt.expectedConfigMapData {
				assert.Contains(t, configMaps.Items[0].Data["config.alloy"], expectedData)
			}
		})
	}
}

func TestNamespaceCollectorUpdatesObjectMeta(t *testing.T) {
	tracesNs := newNamespace("observability")
	ns := newNamespace("my-namespace-dev")

	ns.Annotations = map[string]string{
		Config.stackNameAnnotationKey: "adevintaobs",
		Config.tracesAnnotationKey:    "enabled",
	}

	grafanaCloudClient := mockGrafanaCloudClient{
		GetTracesConnectionFunc: func(stack string) (int, string, error) {
			return 0, "https://test-otlp-endpoint-" + stack + ".com", nil
		},
	}

	t.Run("Reconcile updates object when there are changes", func(t *testing.T) {

		reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs, createDefaultTenantPod(ns.Name))
		reconciler.EnableVPA = true
		reconciler.GrafanaCloudClient = &grafanaCloudClient

		expectedTracesNamespace := reconciler.TracesNamespace

		_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
		require.NoError(t, err)

		secrets := &corev1.SecretList{}
		err = reconciler.Client.List(context.Background(), secrets, client.InNamespace(expectedTracesNamespace))
		require.NoError(t, err)
		assert.Len(t, secrets.Items, 1)

		services := &corev1.ServiceList{}
		err = reconciler.Client.List(context.Background(), services, client.InNamespace(expectedTracesNamespace))
		require.NoError(t, err)
		assert.Len(t, services.Items, 1)

		configMaps := &corev1.ConfigMapList{}
		err = reconciler.Client.List(context.Background(), configMaps, client.InNamespace(expectedTracesNamespace))
		require.NoError(t, err)
		require.Len(t, configMaps.Items, 1)
		require.Contains(t, configMaps.Items[0].Data, "config.alloy")

		deployments := &appv1.DeploymentList{}
		err = reconciler.Client.List(context.Background(), deployments, client.InNamespace(reconciler.TracesNamespace))
		require.NoError(t, err)
		require.Len(t, deployments.Items, 1)

		networkPolicies := &networkingv1.NetworkPolicyList{}
		err = reconciler.Client.List(context.Background(), networkPolicies, client.InNamespace(expectedTracesNamespace))
		require.NoError(t, err)
		assert.Len(t, networkPolicies.Items, 1)

		vpas := &vpav1.VerticalPodAutoscalerList{}
		err = reconciler.Client.List(context.Background(), vpas, client.InNamespace(reconciler.TracesNamespace))
		require.NoError(t, err)
		require.Len(t, vpas.Items, 1)
		assert.Equal(t, vpas.Items[0].Spec.TargetRef.Kind, "Deployment")
		assert.Equal(t, vpas.Items[0].Spec.TargetRef.Name, deployments.Items[0].Name)
		assert.Equal(t, *vpas.Items[0].Spec.UpdatePolicy.UpdateMode, vpav1.UpdateMode("Auto"))

		originalConfig := configMaps.Items[0].Data["config.alloy"]

		existingNs := &corev1.Namespace{}
		err = reconciler.Client.Get(context.Background(), types.NamespacedName{Name: ns.Name}, existingNs)
		require.NoError(t, err)
		existingNs.Annotations[Config.stackNameAnnotationKey] = "adevintaotherstack"
		err = reconciler.Client.Update(context.Background(), existingNs)
		require.NoError(t, err)

		_, err = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
		require.NoError(t, err)

		secrets = &corev1.SecretList{}
		err = reconciler.Client.List(context.Background(), secrets, client.InNamespace(expectedTracesNamespace))
		require.NoError(t, err)
		assert.Len(t, secrets.Items, 1)

		services = &corev1.ServiceList{}
		err = reconciler.Client.List(context.Background(), services, client.InNamespace(expectedTracesNamespace))
		require.NoError(t, err)
		assert.Len(t, services.Items, 1)

		configMaps = &corev1.ConfigMapList{}
		err = reconciler.Client.List(context.Background(), configMaps, client.InNamespace(expectedTracesNamespace))
		require.NoError(t, err)
		require.Contains(t, configMaps.Items[0].Data, "config.alloy")
		assert.NotEqual(t, originalConfig, configMaps.Items[0].Data["config.alloy"])

		deployments = &appv1.DeploymentList{}
		err = reconciler.Client.List(context.Background(), deployments, client.InNamespace(reconciler.TracesNamespace))
		require.NoError(t, err)
		require.Len(t, deployments.Items, 1)

		networkPolicies = &networkingv1.NetworkPolicyList{}
		err = reconciler.Client.List(context.Background(), networkPolicies, client.InNamespace(expectedTracesNamespace))
		require.NoError(t, err)
		assert.Len(t, networkPolicies.Items, 1)

		vpas = &vpav1.VerticalPodAutoscalerList{}
		err = reconciler.Client.List(context.Background(), vpas, client.InNamespace(reconciler.TracesNamespace))
		require.NoError(t, err)
		require.Len(t, vpas.Items, 1)
		assert.Equal(t, vpas.Items[0].Spec.TargetRef.Kind, "Deployment")
		assert.Equal(t, vpas.Items[0].Spec.TargetRef.Name, deployments.Items[0].Name)
		assert.Equal(t, *vpas.Items[0].Spec.UpdatePolicy.UpdateMode, vpav1.UpdateMode("Auto"))
	})
}

func TestTracesCollectorReconcileDisableTenantIfTracesAnnotationChange(t *testing.T) {
	tracesNs := newNamespace("observability")
	ns := newNamespace("my-namespace-dev")

	ns.Annotations = map[string]string{
		Config.stackNameAnnotationKey: "adevintaobs",
		Config.tracesAnnotationKey:    "enabled",
	}

	verifyReconcile := func(reconciler *NamespaceReconciler, namespace string, length int) {
		secrets := &corev1.SecretList{}
		err := reconciler.Client.List(context.Background(), secrets, client.InNamespace(namespace))
		require.NoError(t, err)
		assert.Len(t, secrets.Items, length)

		services := &corev1.ServiceList{}
		err = reconciler.Client.List(context.Background(), services, client.InNamespace(namespace))
		require.NoError(t, err)
		assert.Len(t, services.Items, length)

		configMaps := &corev1.ConfigMapList{}
		err = reconciler.Client.List(context.Background(), configMaps, client.InNamespace(namespace))
		require.NoError(t, err)
		assert.Len(t, configMaps.Items, length)

		deployments := &appv1.DeploymentList{}
		err = reconciler.Client.List(context.Background(), deployments, client.InNamespace(reconciler.TracesNamespace))
		require.NoError(t, err)
		require.Len(t, deployments.Items, length)

		networkPolicies := &networkingv1.NetworkPolicyList{}
		err = reconciler.Client.List(context.Background(), networkPolicies, client.InNamespace(namespace))
		require.NoError(t, err)
		assert.Len(t, networkPolicies.Items, length)

		vpas := &vpav1.VerticalPodAutoscalerList{}
		err = reconciler.Client.List(context.Background(), vpas, client.InNamespace(reconciler.TracesNamespace))
		require.NoError(t, err)
		require.Len(t, vpas.Items, length)
	}

	t.Run("Reconcile must create the traces resources when annotation is enabled and delete it when it is different from enabled", func(t *testing.T) {
		reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs, createDefaultTenantPod(ns.Name))
		reconciler.EnableVPA = true

		expectedTracesNamespace := reconciler.TracesNamespace

		_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
		require.NoError(t, err)
		verifyReconcile(reconciler, expectedTracesNamespace, 1)

		existingNs := &corev1.Namespace{}
		err = reconciler.Client.Get(context.Background(), types.NamespacedName{Name: ns.Name}, existingNs)
		require.NoError(t, err)
		existingNs.Annotations[Config.tracesAnnotationKey] = "whatevervalue"

		err = reconciler.Client.Update(context.Background(), existingNs)
		require.NoError(t, err)

		_, err = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
		require.NoError(t, err)
		verifyReconcile(reconciler, expectedTracesNamespace, 0)
	})

	t.Run("Reconcile must create the traces resources when annotation is enabled and delete it when it is not set", func(t *testing.T) {
		reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs, createDefaultTenantPod(ns.Name))
		reconciler.EnableVPA = true

		expectedTracesNamespace := reconciler.TracesNamespace

		_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
		require.NoError(t, err)
		verifyReconcile(reconciler, expectedTracesNamespace, 1)

		existingNs := &corev1.Namespace{}
		err = reconciler.Client.Get(context.Background(), types.NamespacedName{Name: ns.Name}, existingNs)
		require.NoError(t, err)
		delete(existingNs.Annotations, Config.tracesAnnotationKey)

		err = reconciler.Client.Update(context.Background(), existingNs)
		require.NoError(t, err)

		_, err = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
		require.NoError(t, err)
		verifyReconcile(reconciler, expectedTracesNamespace, 0)
	})
}

func TestTracesCollectorDeploymentIsAnnotatedForObservability(t *testing.T) {
	tracesNs := newNamespace("observability")
	ns := newNamespace("my-namespace-dev")

	ns.Annotations = map[string]string{
		Config.stackNameAnnotationKey: "adevintaobs",
		Config.tracesAnnotationKey:    "enabled",
	}

	t.Run("Alloy Pod is annotated to be able to gather metrics from it", func(t *testing.T) {
		expectedAnnotations := map[string]string{
			"prometheus.io/path":   "/metrics",
			"prometheus.io/port":   "12345",
			"prometheus.io/scrape": "true",
		}

		reconciler := newDefaultNamespaceReconciler(t, ns, tracesNs, createDefaultTenantPod(ns.Name))

		expectedTracesNamespace := reconciler.TracesNamespace

		_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}})
		require.NoError(t, err)

		deployments := &appv1.DeploymentList{}
		err = reconciler.Client.List(context.Background(), deployments, client.InNamespace(expectedTracesNamespace))
		require.NoError(t, err)

		require.Len(t, deployments.Items, 1)
		assert.Equal(t, expectedAnnotations, deployments.Items[0].Spec.Template.Annotations)
	})
}
