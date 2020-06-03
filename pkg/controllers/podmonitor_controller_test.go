package controllers

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/adevinta/observability-operator/pkg/grafanacloud"
	"github.com/adevinta/observability-operator/pkg/test_helpers"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

func newDefaultTestPodMonitorReconciler(t *testing.T, initialObjects ...runtime.Object) *PodMonitorReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = monitoringv1.AddToScheme(scheme)
	os.Setenv("GRAFANA_CLOUD_TOKEN", "zzzz")
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			return nil, nil
		},
	}

	fakeClient := test_helpers.NewFakeClient(t, initialObjects...)
	return &PodMonitorReconciler{
		Client: fakeClient,
		Region: "eu-fake-1",
		PrometheusDockerImage: DockerImage{
			Name: "foo",
			Tag:  "v0.0.1",
		},
		PrometheusNamespace:          "prometheusesNamespace",
		PrometheusExposedDomain:      "clustername.clusterdomain",
		ClusterName:                  "clustername",
		Log:                          ctrl.Log.WithName("controllers").WithName("Deployment"),
		Scheme:                       scheme,
		GrafanaCloudCredentials:      "observability-operator-grafana-cloud-credentials",
		GrafanaCloudClient:           &grafanaCloudClient,
		PrometheusServiceAccountName: "prometheus-tenant",
		PrometheusMonitoringTarget:   "monitoring-target",
		PrometheusExtraExternalLabels: map[string]string{
			"mycustomkey": "mycustomvalue",
		},
	}
}

func createCustomStorageSecretStub(namespace string) (*corev1.Secret, *corev1.Secret, *corev1.Secret) {
	remoteWrite := `
    basicAuth:
      password:
        key: password
        name: secret2
      username:
        key: username
        name: secret1
    url: https://victoria-metrics-url`

	remoteWriteSecret := &corev1.Secret{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "secret-storage",
			Namespace: namespace,
			Annotations: map[string]string{
				Config.referencedSecretAnnotationKeys: "secret1,secret2",
			},
		},
		Data: map[string][]uint8{
			"remote-write": []byte(remoteWrite),
		},
	}
	secret1 := &corev1.Secret{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "secret1",
			Namespace: namespace,
		},
		Data: map[string][]uint8{
			"username": []byte("foo"),
		},
	}

	secret2 := &corev1.Secret{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "secret2",
			Namespace: namespace,
		},
		Data: map[string][]uint8{
			"password": []byte("bar"),
		},
	}

	return remoteWriteSecret, secret1, secret2
}

func createSecretStub(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "observability-operator-grafana-cloud-credentials",
			Namespace: namespace,
		},
		Data: map[string][]uint8{
			"grafana-cloud-api-key":      []byte("xxx"),
			"grafana-cloud-traces-token": []byte("yyy"),
		},
	}
}

func getScrappingConfig() (PrometheusAdditionalScrapeConfig, error) {
	var scrap PrometheusAdditionalScrapeConfig
	b, err := os.ReadFile("fixtures/additional_scrapping_config")
	if err != nil {
		return scrap, err
	}
	err = yaml.Unmarshal(b, &scrap)
	if err != nil {
		return scrap, err
	}
	return scrap, err
}

func createPodStub(name, namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": name},
		},
		Spec: corev1.PodSpec{},
	}
}

func createPodMonitor(namespace string, name string) *monitoringv1.PodMonitor {
	return &monitoringv1.PodMonitor{
		ObjectMeta: ctrl.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}
}

func createPrometheusObject(namespace string, name string) *monitoringv1.Prometheus {
	replicas := int32(1)
	shards := int32(1)
	return &monitoringv1.Prometheus{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				Config.storageAnnotationKey: "grafanacloud",
				Config.accountAnnotationKey: name,
			},
		},
		Spec: monitoringv1.PrometheusSpec{
			AdditionalScrapeConfigs: &corev1.SecretKeySelector{
				Key:                  "prometheus",
				LocalObjectReference: corev1.LocalObjectReference{Name: fmt.Sprintf("prometheus-%s-monitoring-target", name)},
			},
			PodMetadata: &monitoringv1.EmbeddedObjectMetadata{
				Labels: map[string]string{
					Config.accountLabelKey: name,
				},
			},
			ServiceMonitorSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{Config.accountLabelKey: name},
			},
			PodMonitorSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{Config.accountLabelKey: name},
			},
			RuleNamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": name,
				},
			},
			ServiceMonitorNamespaceSelector: &metav1.LabelSelector{},
			PodMonitorNamespaceSelector:     &metav1.LabelSelector{},
			RoutePrefix:                     "/",
			ServiceAccountName:              "prometheus-tenant",
			PortName:                        "web",
			Version:                         "v0.0.1",
			BaseImage:                       "foo",
			Replicas:                        &replicas,
			Shards:                          &shards,
			LogLevel:                        "warn",
			LogFormat:                       "logfmt",
			Retention:                       "30m",
			Resources:                       defaultResourceRequirements,
			ExternalLabels: map[string]string{
				"cluster":     "clustername",
				"region":      "eu-fake-1",
				"account":     name,
				"monitor":     "prometheus-local",
				"mycustomkey": "mycustomvalue",
			},
			ExternalURL: fmt.Sprintf("http://%s-metrics.clustername.clusterdomain", name),
			RemoteWrite: []monitoringv1.RemoteWriteSpec{{
				URL: "https://prometheus-us-central1.grafana.net/api/prom/push",
				BasicAuth: &monitoringv1.BasicAuth{
					Username: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "observability-operator-grafana-cloud-credentials",
						},
						Key: "adevintaruntime",
					},
					Password: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "observability-operator-grafana-cloud-credentials",
						},
						Key: "grafana-cloud-api-key",
					},
				},
				WriteRelabelConfigs: []monitoringv1.RelabelConfig{
					{
						Action:       "drop",
						SourceLabels: []string{"__name__"},
						Regex:        "prometheus_.*",
					},
				},
			}},
			RuleSelector: &metav1.LabelSelector{},
		},
	}
}

func createIngressServiceMonitor() *monitoringv1.ServiceMonitor {
	return &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prometheus-podmonitorNamespace-adevinta-ingress-metrics",
			Namespace: "prometheusesNamespace",
			Labels: map[string]string{
				Config.accountLabelKey: "podmonitorNamespace",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "monitoring.coreos.com/v1",
					Kind:               "Prometheus",
					Name:               "podmonitorNamespace",
					UID:                "",
					Controller:         nil,
					BlockOwnerDeletion: nil,
				},
			},
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Endpoints: []monitoringv1.Endpoint{
				{
					Interval: "30s",
					Params: map[string][]string{
						"match[]": {"{federate=\"true\", namespace=\"podmonitorNamespace\"}"},
					},
					Path: "/federate",
					RelabelConfigs: []*monitoringv1.RelabelConfig{
						{
							Action: "labeldrop",
							Regex:  "pod",
						},
						{
							Action: "labeldrop",
							Regex:  "node",
						},
						{
							Action: "labeldrop",
							Regex:  "namespace",
						},
					},
					MetricRelabelConfigs: []*monitoringv1.RelabelConfig{
						{
							Action: "labeldrop",
							Regex:  "federate",
						},
					},
					HonorLabels: true,
				},
			},
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"release": "adevinta-ingress-metrics",
				},
			},
		},
	}
}

func createRemoteWriteBehindSecondsPrometheusRule() *monitoringv1.PrometheusRule {
	return &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prometheus-podmonitorNamespace-prometheus-remote-write-behind-seconds",
			Namespace: "prometheusesNamespace",
			Labels: map[string]string{
				Config.accountLabelKey: "podmonitorNamespace",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "monitoring.coreos.com/v1",
					Kind:               "Prometheus",
					Name:               "podmonitorNamespace",
					UID:                "",
					Controller:         nil,
					BlockOwnerDeletion: nil,
				},
			},
		},
		Spec: monitoringv1.PrometheusRuleSpec{
			Groups: []monitoringv1.RuleGroup{
				{
					Name: "prometheus-podmonitorNamespace-prometheus-remote-write-behind-seconds",
					Rules: []monitoringv1.Rule{
						{
							Record: "prometheus_remote_write_behind_seconds",
							Expr: intstr.FromString(
								"max_over_time(prometheus_remote_storage_highest_timestamp_in_seconds{job=\"prometheus-scraper\"}[2m]) " +
									"- ignoring(remote_name, url) group_right " +
									"max_over_time(prometheus_remote_storage_queue_highest_sent_timestamp_seconds{job=\"prometheus-scraper\"}[2m])"),
						},
					},
				},
			},
		},
	}
}

func createRemoteWriteStorageFailuresPercentPrometheusRule() *monitoringv1.PrometheusRule {
	return &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prometheus-podmonitorNamespace-prometheus-remote-write-storage-failures-percentage",
			Namespace: "prometheusesNamespace",
			Labels: map[string]string{
				Config.accountLabelKey: "podmonitorNamespace",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "monitoring.coreos.com/v1",
					Kind:               "Prometheus",
					Name:               "podmonitorNamespace",
					UID:                "",
					Controller:         nil,
					BlockOwnerDeletion: nil,
				},
			},
		},
		Spec: monitoringv1.PrometheusRuleSpec{
			Groups: []monitoringv1.RuleGroup{
				{
					Name: "prometheus-podmonitorNamespace-prometheus-remote-write-storage-failures-percentage",
					Rules: []monitoringv1.Rule{
						{
							Record: "prometheus_remote_write_storage_failures_percentage",
							Expr:   intstr.FromString("(rate(prometheus_remote_storage_failed_samples_total{job=\"prometheus-scraper\"}[2m])/(rate(prometheus_remote_storage_failed_samples_total{job=\"prometheus-scraper\"}[2m])+rate(prometheus_remote_storage_succeeded_samples_total{job=\"prometheus-scraper\"}[2m])))* 100"),
						},
					},
				},
			},
		},
	}
}

func createPrometheusRulesWithoutSampleLimit() []*monitoringv1.PrometheusRule {
	return []*monitoringv1.PrometheusRule{
		createRemoteWriteBehindSecondsPrometheusRule(),
		createRemoteWriteStorageFailuresPercentPrometheusRule(),
	}
}

func getRecordingRuleByRecordName(prometheusRules []*monitoringv1.PrometheusRule, recordName string) monitoringv1.Rule {
	for _, promRules := range prometheusRules {
		for _, groups := range promRules.Spec.Groups {
			for _, rule := range groups.Rules {
				if rule.Record == recordName {
					return rule
				}
			}
		}
	}
	return monitoringv1.Rule{}
}

func TestPodMonitorWithoutStorageDoesNotCreatePrometheus(t *testing.T) {
	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	reconciler := newDefaultTestPodMonitorReconciler(t, podmonitor)
	reconciler.EnableMetricsRemoteWrite = true

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.Nil(t, err)
	require.NotNil(t, result)

	prometheuses := &monitoringv1.PrometheusList{}

	require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
	assert.Len(t, prometheuses.Items, 0)
}

func TestPodMonitorWithoutStorageDoesNotCreatePrometheusRules(t *testing.T) {
	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	reconciler := newDefaultTestPodMonitorReconciler(t, podmonitor)
	reconciler.EnableMetricsRemoteWrite = true

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.Nil(t, err)
	require.NotNil(t, result)
	prometheusRules := &monitoringv1.PrometheusRuleList{}

	require.NoError(t, reconciler.Client.List(context.Background(), prometheusRules))
	assert.Len(t, prometheusRules.Items, 0)
}

func TestPodMonitorWithoutStorageDoesNotCreateIngressServiceMonitor(t *testing.T) {
	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	reconciler := newDefaultTestPodMonitorReconciler(t, podmonitor)
	reconciler.EnableMetricsRemoteWrite = true

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.Nil(t, err)
	require.NotNil(t, result)
	serviceMonitors := &monitoringv1.ServiceMonitorList{}

	require.NoError(t, reconciler.Client.List(context.Background(), serviceMonitors))
	assert.Len(t, serviceMonitors.Items, 0)
}

func TestPodMonitorWithGrafanaCloudStorageCreatesPrometheus(t *testing.T) {
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			stack := grafanacloud.Stack{
				PromURL: "https://prometheus-us-central1.grafana.net",
			}
			return &stack, nil
		},
	}

	t.Run("it should create the prometheus object", func(t *testing.T) {
		podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
		podmonitor.Annotations = map[string]string{
			Config.storageAnnotationKey: "grafanacloud",
		}
		podMonitorNamespace := newNamespace(podmonitor.Namespace)
		podMonitorNamespace.SetAnnotations(map[string]string{
			Config.stackNameAnnotationKey: "adevintaruntime",
		})
		reconciler := newDefaultTestPodMonitorReconciler(
			t,
			podmonitor, createSecretStub("prometheusesNamespace"),
			newNamespace("prometheusesNamespace"),
			podMonitorNamespace,
		)
		reconciler.PrometheusPodPriorityClassName = "prometheus-priority-class"
		reconciler.EnableMetricsRemoteWrite = true
		reconciler.PrometheusNamespace = "prometheusesNamespace"
		reconciler.GrafanaCloudClient = &grafanaCloudClient

		expectedPrometheus := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")
		expectedPrometheus.Spec.PriorityClassName = "prometheus-priority-class"

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
		require.Nil(t, err)
		assert.NotNil(t, result)

		prometheuses := &monitoringv1.PrometheusList{}

		require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
		require.Len(t, prometheuses.Items, 1)
		expectedPrometheus.Spec.PodMetadata.Annotations = prometheuses.Items[0].Spec.PodMetadata.Annotations
		expectedPrometheus.ResourceVersion = prometheuses.Items[0].ResourceVersion
		assert.Equal(t, *expectedPrometheus, *prometheuses.Items[0])
	})

	t.Run("it should fail if the grafana API returns a non-200 listing the stacks.", func(t *testing.T) {
		grafanaCloudClient := mockGrafanaCloudClient{
			GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
				return nil, fmt.Errorf("not found")
			},
		}

		podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
		podmonitor.Annotations = map[string]string{
			Config.storageAnnotationKey: "grafanacloud",
		}
		reconciler := newDefaultTestPodMonitorReconciler(
			t,
			podmonitor, createSecretStub("prometheusesNamespace"),
			newNamespace("prometheusesNamespace"),
			newNamespace(podmonitor.Namespace),
		)
		reconciler.EnableMetricsRemoteWrite = true
		reconciler.PrometheusNamespace = "prometheusesNamespace"
		reconciler.GrafanaCloudClient = &grafanaCloudClient

		expectedPrometheus := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")

		// Declaring the []monitoringv1.RemoteWriteSpec slice at assignment makes the value nil instead of an empty slice.
		// -> var remoteWriteConfig []monitoringv1.RemoteWriteSpec creates an empty slice.
		// -> expectedPrometheus.Spec.RemoteWrite = []monitoringv1.RemoteWriteSpec{} creates a nil slice.
		// The test expects an empty slice.
		var remoteWriteConfig []monitoringv1.RemoteWriteSpec
		expectedPrometheus.Spec.RemoteWrite = remoteWriteConfig

		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
		assert.Error(t, err)

		prometheuses := &monitoringv1.PrometheusList{}

		require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
		assert.Equal(t, len(prometheuses.Items), 0)
	})

	t.Run("if a Prometheus existed, the remoteWriteconfig should be kept when the grafana API fails listing the stacks with a non-200 return code.", func(t *testing.T) {
		grafanaCloudClient := mockGrafanaCloudClient{
			GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
				return nil, fmt.Errorf("not found")
			},
		}

		podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
		podmonitor.Annotations = map[string]string{
			Config.storageAnnotationKey: "grafanacloud",
		}

		prom := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")
		prom.Spec.RemoteWrite = []monitoringv1.RemoteWriteSpec{{
			Name: "a-test-connection-remote-write",
			URL:  "grafana-api-to-send-labels.com",
		}}
		expectedRemoteWrite := prom.Spec.RemoteWrite

		reconciler := newDefaultTestPodMonitorReconciler(
			t,
			podmonitor, createSecretStub("prometheusesNamespace"),
			newNamespace("prometheusesNamespace"),
			newNamespace(podmonitor.Namespace),
			prom,
		)
		reconciler.EnableMetricsRemoteWrite = true
		reconciler.PrometheusNamespace = "prometheusesNamespace"
		reconciler.GrafanaCloudClient = &grafanaCloudClient

		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
		assert.Error(t, err)

		prometheuses := &monitoringv1.PrometheusList{}

		require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
		assert.Equal(t, len(prometheuses.Items), 1)
		assert.Equal(t, expectedRemoteWrite, prometheuses.Items[0].Spec.RemoteWrite)
	})
}

func TestPodMonitorWithCustomRelabelConfigs(t *testing.T) {
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			stack := grafanacloud.Stack{
				PromURL: "https://prometheus-us-central1.grafana.net",
			}
			return &stack, nil
		},
	}
	t.Run("it should create the prometheus object with a relabel config", func(t *testing.T) {
		podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
		podmonitor.Annotations = map[string]string{
			Config.storageAnnotationKey: "grafanacloud",
		}
		podMonitorNamespace := newNamespace(podmonitor.Namespace)
		podMonitorNamespace.SetAnnotations(map[string]string{
			Config.stackNameAnnotationKey: "adevintaruntime",
		})
		reconciler := newDefaultTestPodMonitorReconciler(
			t,
			podmonitor, createSecretStub("prometheusesNamespace"),
			newNamespace("prometheusesNamespace"),
			podMonitorNamespace,
			createConfigMapRulesStub("custom-relabel-configs", podmonitor.Namespace),
		)
		reconciler.EnableMetricsRemoteWrite = true
		reconciler.PrometheusNamespace = "prometheusesNamespace"
		reconciler.GrafanaCloudClient = &grafanaCloudClient

		expectedPrometheus := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
		require.Nil(t, err)
		assert.NotNil(t, result)

		prometheuses := &monitoringv1.PrometheusList{}

		require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
		require.Len(t, prometheuses.Items, 1)
		expectedPrometheus.Spec.PodMetadata.Annotations = prometheuses.Items[0].Spec.PodMetadata.Annotations
		expectedPrometheus.ResourceVersion = prometheuses.Items[0].ResourceVersion
		expectedPrometheus.Spec.RemoteWrite[0].WriteRelabelConfigs = []monitoringv1.RelabelConfig{
			{
				Action: "labeldrop",
				Regex:  "^prometheus$",
			},
			{
				Action: "labeldrop",
				Regex:  "^node_exporter$",
			},
			{
				Action:       "drop",
				SourceLabels: []string{"__name__"},
				Regex:        "prometheus_.*",
			},
		}
		assert.Equal(t, *expectedPrometheus, *prometheuses.Items[0])
	})

	t.Run("it should not update the prometheus object with the previous relabel config if the new configMap is wrong", func(t *testing.T) {
		podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
		podmonitor.Annotations = map[string]string{
			Config.storageAnnotationKey: "grafanacloud",
		}
		podMonitorNamespace := newNamespace(podmonitor.Namespace)
		podMonitorNamespace.SetAnnotations(map[string]string{
			Config.stackNameAnnotationKey: "adevintaruntime",
		})
		existingPrometheus := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")
		existingPrometheus.Spec.RemoteWrite[0].WriteRelabelConfigs = []monitoringv1.RelabelConfig{
			{
				Action:       "labeldrop",
				SourceLabels: []string{"__name__"},
				Regex:        "^prometheus$",
			},
		}

		reconciler := newDefaultTestPodMonitorReconciler(
			t,
			podmonitor, createSecretStub("prometheusesNamespace"),
			newNamespace("prometheusesNamespace"),
			podMonitorNamespace,
			createConfigMapBrokenRulesStub("custom-relabel-configs", podmonitor.Namespace),
			existingPrometheus,
		)
		reconciler.EnableMetricsRemoteWrite = true
		reconciler.PrometheusNamespace = "prometheusesNamespace"
		reconciler.GrafanaCloudClient = &grafanaCloudClient

		expectedPrometheus := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
		require.Nil(t, err)
		assert.NotNil(t, result)

		prometheuses := &monitoringv1.PrometheusList{}

		require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
		require.Len(t, prometheuses.Items, 1)
		expectedPrometheus.Spec.PodMetadata.Annotations = prometheuses.Items[0].Spec.PodMetadata.Annotations
		expectedPrometheus.ResourceVersion = prometheuses.Items[0].ResourceVersion
		expectedPrometheus.Spec.RemoteWrite[0].WriteRelabelConfigs = []monitoringv1.RelabelConfig{
			{
				Action:       "labeldrop",
				SourceLabels: []string{"__name__"},
				Regex:        "^prometheus$",
			},
		}
		assert.Equal(t, expectedPrometheus.Spec.RemoteWrite[0].WriteRelabelConfigs, prometheuses.Items[0].Spec.RemoteWrite[0].WriteRelabelConfigs)
	})

	t.Run("it should create the prometheus object without errors if no relabel config is defined", func(t *testing.T) {
		podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
		podmonitor.Annotations = map[string]string{
			Config.storageAnnotationKey: "grafanacloud",
		}
		podMonitorNamespace := newNamespace(podmonitor.Namespace)
		podMonitorNamespace.SetAnnotations(map[string]string{
			Config.stackNameAnnotationKey: "adevintaruntime",
		})

		// ns.Annotations[stackNameAnnotationKey] = "adevintastack1"
		reconciler := newDefaultTestPodMonitorReconciler(
			t,
			podmonitor, createSecretStub("prometheusesNamespace"),
			newNamespace("prometheusesNamespace"),
			podMonitorNamespace,
		)
		reconciler.EnableMetricsRemoteWrite = true
		reconciler.PrometheusNamespace = "prometheusesNamespace"
		reconciler.GrafanaCloudClient = &grafanaCloudClient

		expectedPrometheus := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
		require.Nil(t, err)
		assert.NotNil(t, result)

		prometheuses := &monitoringv1.PrometheusList{}

		require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
		require.Len(t, prometheuses.Items, 1)
		expectedPrometheus.Spec.PodMetadata.Annotations = prometheuses.Items[0].Spec.PodMetadata.Annotations
		expectedPrometheus.ResourceVersion = prometheuses.Items[0].ResourceVersion
		assert.Equal(t, *expectedPrometheus, *prometheuses.Items[0])
	})

	t.Run("it should create the prometheus object and don't remove the remoteWriteConfig if configMap info is wrong", func(t *testing.T) {
		podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
		podmonitor.Annotations = map[string]string{
			Config.storageAnnotationKey: "grafanacloud",
		}
		podMonitorNamespace := newNamespace(podmonitor.Namespace)
		podMonitorNamespace.SetAnnotations(map[string]string{
			Config.stackNameAnnotationKey: "adevintaruntime",
		})
		reconciler := newDefaultTestPodMonitorReconciler(
			t,
			podmonitor, createSecretStub("prometheusesNamespace"),
			newNamespace("prometheusesNamespace"),
			podMonitorNamespace,
			createConfigMapBrokenRulesStub("custom-relabel-configs", podmonitor.Namespace),
		)
		reconciler.EnableMetricsRemoteWrite = true
		reconciler.PrometheusNamespace = "prometheusesNamespace"
		reconciler.GrafanaCloudClient = &grafanaCloudClient

		expectedPrometheus := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
		require.Nil(t, err)
		assert.NotNil(t, result)

		prometheuses := &monitoringv1.PrometheusList{}

		require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
		require.Len(t, prometheuses.Items, 1)
		expectedPrometheus.Spec.PodMetadata.Annotations = prometheuses.Items[0].Spec.PodMetadata.Annotations
		expectedPrometheus.ResourceVersion = prometheuses.Items[0].ResourceVersion
		expectedPrometheus.Spec.RemoteWrite[0].WriteRelabelConfigs = []monitoringv1.RelabelConfig{
			{
				Action:       "drop",
				SourceLabels: []string{"__name__"},
				Regex:        "prometheus_.*",
			},
		}
		assert.Equal(t, *expectedPrometheus, *prometheuses.Items[0])
	})
}

func TestPodMonitorWithGrafanaCloudStorageAndMultipleStacksCreatesPrometheus(t *testing.T) {
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			switch tenant {
			case "adevintastack1":
				return &grafanacloud.Stack{
					Slug:              "adevintastack1",
					MetricsInstanceID: 14111,
					PromURL:           "https://prometheus-us-west1.grafana.net",
				}, nil
			case "adevintastack2":
				return &grafanacloud.Stack{
					Slug:              "adevintastack2",
					MetricsInstanceID: 14818,
					PromURL:           "https://prometheus-us-central1.grafana.net",
				}, nil
			case "stack3":
				return &grafanacloud.Stack{
					Slug:              "stack3",
					MetricsInstanceID: 14820,
					PromURL:           "https://prometheus-us-east1.grafana.net",
				}, nil
			}

			stack := grafanacloud.Stack{}
			return &stack, nil
		},
	}

	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.storageAnnotationKey: "grafanacloud",
	}

	podMonitorNamespace := newNamespace(podmonitor.Namespace)
	podMonitorNamespace.SetAnnotations(map[string]string{
		Config.stackNameAnnotationKey: "adevintastack1,adevintastack2,stack3",
	})

	reconciler := newDefaultTestPodMonitorReconciler(
		t,
		podmonitor, createSecretStub("prometheusesNamespace"),
		newNamespace("prometheusesNamespace"),
		podMonitorNamespace,
	)
	reconciler.EnableMetricsRemoteWrite = true
	reconciler.PrometheusNamespace = "prometheusesNamespace"
	reconciler.GrafanaCloudClient = &grafanaCloudClient

	expectedPrometheus := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.Nil(t, err)
	assert.NotNil(t, result)

	prometheuses := &monitoringv1.PrometheusList{}

	require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
	require.Len(t, prometheuses.Items, 1)
	expectedPrometheus.Spec.RemoteWrite = []monitoringv1.RemoteWriteSpec{
		{
			URL: "https://prometheus-us-west1.grafana.net/api/prom/push",
			BasicAuth: &monitoringv1.BasicAuth{
				Username: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "observability-operator-grafana-cloud-credentials",
					},
					Key: "adevintastack1",
				},
				Password: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "observability-operator-grafana-cloud-credentials",
					},
					Key: "grafana-cloud-api-key",
				},
			},
			WriteRelabelConfigs: []monitoringv1.RelabelConfig{
				{
					Action:       "drop",
					SourceLabels: []string{"__name__"},
					Regex:        "prometheus_.*",
				},
			},
		},
		{
			URL: "https://prometheus-us-central1.grafana.net/api/prom/push",
			BasicAuth: &monitoringv1.BasicAuth{
				Username: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "observability-operator-grafana-cloud-credentials",
					},
					Key: "adevintastack2",
				},
				Password: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "observability-operator-grafana-cloud-credentials",
					},
					Key: "grafana-cloud-api-key",
				},
			},
			WriteRelabelConfigs: []monitoringv1.RelabelConfig{
				{
					Action:       "drop",
					SourceLabels: []string{"__name__"},
					Regex:        "prometheus_.*",
				},
			},
		},
		{
			URL: "https://prometheus-us-east1.grafana.net/api/prom/push",
			BasicAuth: &monitoringv1.BasicAuth{
				Username: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "observability-operator-grafana-cloud-credentials",
					},
					Key: "stack3",
				},
				Password: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "observability-operator-grafana-cloud-credentials",
					},
					Key: "grafana-cloud-api-key",
				},
			},
			WriteRelabelConfigs: []monitoringv1.RelabelConfig{
				{
					Action:       "drop",
					SourceLabels: []string{"__name__"},
					Regex:        "prometheus_.*",
				},
			},
		},
	}
	expectedPrometheus.Spec.PodMetadata.Annotations = prometheuses.Items[0].Spec.PodMetadata.Annotations
	expectedPrometheus.ResourceVersion = prometheuses.Items[0].ResourceVersion
	assert.ElementsMatch(t, expectedPrometheus.Spec.RemoteWrite, prometheuses.Items[0].Spec.RemoteWrite)
}

func TestCreatesPrometheusWithSelectorAndToleration(t *testing.T) {
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			stack := grafanacloud.Stack{
				PromURL: "https://prometheus-us-central1.grafana.net",
			}
			return &stack, nil
		},
	}

	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.storageAnnotationKey: "grafanacloud",
	}
	podMonitorNamespace := newNamespace(podmonitor.Namespace)
	podMonitorNamespace.SetAnnotations(map[string]string{
		Config.stackNameAnnotationKey: "adevintaruntime",
	})

	reconciler := newDefaultTestPodMonitorReconciler(
		t,
		podmonitor, createSecretStub("prometheusesNamespace"),
		newNamespace("prometheusesNamespace"),
		podMonitorNamespace,
	)
	reconciler.EnableMetricsRemoteWrite = true
	reconciler.PrometheusNamespace = "prometheusesNamespace"
	reconciler.NodeSelectorTarget = "my-dedicated-prometheus-pool"
	reconciler.GrafanaCloudClient = &grafanaCloudClient
	expectedPrometheus := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.Nil(t, err)
	assert.NotNil(t, result)

	prometheuses := &monitoringv1.PrometheusList{}

	require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
	require.Len(t, prometheuses.Items, 1)
	expectedPrometheus.Spec.PodMetadata.Annotations = prometheuses.Items[0].Spec.PodMetadata.Annotations
	expectedPrometheus.ResourceVersion = prometheuses.Items[0].ResourceVersion
	expectedPrometheus.Spec.Tolerations = []corev1.Toleration{
		{
			Key:      reconciler.NodeSelectorTarget,
			Operator: "Equal",
			Value:    "true",
			Effect:   "NoSchedule",
		},
	}
	expectedPrometheus.Spec.NodeSelector = map[string]string{
		reconciler.NodeSelectorTarget: "true",
	}
	assert.Equal(t, *expectedPrometheus, *prometheuses.Items[0])
}

func TestPodMonitorWithGrafanaCloudStorageDoesNotCreateServiceMonitor(t *testing.T) {
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			stack := grafanacloud.Stack{
				PromURL: "https://prometheus-us-central1.grafana.net",
			}
			return &stack, nil
		},
	}

	secret := createSecretStub("prometheusNamespace")
	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.storageAnnotationKey: "grafanacloud",
	}
	podMonitorNamespace := newNamespace(podmonitor.Namespace)
	podMonitorNamespace.SetAnnotations(map[string]string{
		Config.stackNameAnnotationKey: "adevintaruntime",
	})
	reconciler := newDefaultTestPodMonitorReconciler(t, secret, podmonitor, podMonitorNamespace)
	reconciler.EnableMetricsRemoteWrite = true
	reconciler.PrometheusNamespace = "prometheusNamespace"
	reconciler.GrafanaCloudClient = &grafanaCloudClient

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.NoError(t, err)
	assert.NotNil(t, result)

	prometheuses := &monitoringv1.PrometheusList{}
	require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
	assert.Len(t, prometheuses.Items, 1)

	serviceMonitors := &monitoringv1.ServiceMonitorList{}
	require.NoError(t, reconciler.Client.List(context.Background(), serviceMonitors))
	assert.Len(t, serviceMonitors.Items, 0)
}

func TestPodMonitorWithGrafanaCloudStorageCreatesASecretWithAdditionalScrapingConfig(t *testing.T) {
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			stack := grafanacloud.Stack{
				PromURL: "https://prometheus-us-central1.grafana.net",
			}
			return &stack, nil
		},
	}

	promNamespace := "prometheusesNamespace"
	secret := createSecretStub(promNamespace)

	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.storageAnnotationKey: "grafanacloud",
	}
	podMonitorNamespace := newNamespace(podmonitor.Namespace)
	podMonitorNamespace.SetAnnotations(map[string]string{
		Config.stackNameAnnotationKey: "adevintaruntime",
	})
	expectedIngressServiceMonitor := createIngressServiceMonitor()

	reconciler := newDefaultTestPodMonitorReconciler(t, secret, expectedIngressServiceMonitor, podmonitor, podMonitorNamespace)
	reconciler.EnableMetricsRemoteWrite = true
	reconciler.PrometheusNamespace = promNamespace
	reconciler.GrafanaCloudClient = &grafanaCloudClient

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.Nil(t, err)
	assert.NotNil(t, result)
	targetScrappingConfig, err := getScrappingConfig()
	require.NoError(t, err)

	prometheusConfigSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "prometheusesNamespace",
			Name:      "prometheus-podmonitorNamespace-monitoring-target",
		},
	}
	require.NoError(t, reconciler.Client.Get(context.Background(), client.ObjectKeyFromObject(prometheusConfigSecret), prometheusConfigSecret))
	actualScrappingConfig := PrometheusAdditionalScrapeConfig{}
	err = yaml.Unmarshal([]byte(prometheusConfigSecret.StringData["prometheus"]), &actualScrappingConfig)
	require.Nil(t, err)
	assert.Equal(t, targetScrappingConfig, actualScrappingConfig)
}

func TestPodMonitorWithGrafanaCloudStorageCreatesTwoPrometheusRulesIfNoSampleLimit(t *testing.T) {
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			stack := grafanacloud.Stack{
				PromURL: "https://prometheus-us-central1.grafana.net",
			}
			return &stack, nil
		},
	}

	promNamespace := "prometheusesNamespace"
	secret := createSecretStub(promNamespace)

	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.accountAnnotationKey: "podmonitorNamespace",
		Config.storageAnnotationKey: "grafanacloud",
	}
	podMonitorNamespace := newNamespace(podmonitor.Namespace)
	podMonitorNamespace.SetAnnotations(map[string]string{
		Config.stackNameAnnotationKey: "adevintaruntime",
	})
	expectedRules := createPrometheusRulesWithoutSampleLimit()

	reconciler := newDefaultTestPodMonitorReconciler(t, secret, podmonitor, podMonitorNamespace)
	reconciler.EnableMetricsRemoteWrite = true
	reconciler.PrometheusNamespace = promNamespace
	reconciler.GrafanaCloudClient = &grafanaCloudClient

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.NoError(t, err)
	assert.NotNil(t, result)

	promRules := monitoringv1.PrometheusRuleList{}
	require.NoError(t, reconciler.Client.List(context.Background(), &promRules))
	for i, rule := range promRules.Items {
		if i < len(expectedRules) {
			expectedRules[i].ResourceVersion = rule.ResourceVersion
		}
	}
	assert.Equal(t, expectedRules, promRules.Items)
}

func TestPodMonitorWithGrafanaCloudStorageCreatesThreePrometheusRulesIfSampleLimitIsSet(t *testing.T) {
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			stack := grafanacloud.Stack{
				PromURL: "https://prometheus-us-central1.grafana.net",
			}
			return &stack, nil
		},
	}

	promNamespace := "promNamespace"
	secret := createSecretStub(promNamespace)

	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	sampleLimit := 10 + rand.Intn(50000)
	podmonitor.Spec.SampleLimit = uint64(sampleLimit)
	podmonitor.Annotations = map[string]string{
		Config.accountAnnotationKey: "podmonitorNamespace",
		Config.storageAnnotationKey: "grafanacloud",
	}
	podMonitorNamespace := newNamespace(podmonitor.Namespace)
	podMonitorNamespace.SetAnnotations(map[string]string{
		Config.stackNameAnnotationKey: "adevintaruntime",
	})

	reconciler := newDefaultTestPodMonitorReconciler(t, secret, podmonitor, podMonitorNamespace)
	reconciler.EnableMetricsRemoteWrite = true
	reconciler.PrometheusNamespace = promNamespace
	reconciler.GrafanaCloudClient = &grafanaCloudClient

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.NoError(t, err)
	assert.NotNil(t, result)

	promRules := monitoringv1.PrometheusRuleList{}
	require.NoError(t, reconciler.Client.List(context.Background(), &promRules))

	assert.Equal(t, intstr.FromInt(sampleLimit), getRecordingRuleByRecordName(promRules.Items, "scrape_config_sample_limit").Expr)
}

func TestTwoPodMonitorInTheSameNamespaceOnlyCreateOnePrometheus(t *testing.T) {
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			stack := grafanacloud.Stack{
				PromURL: "https://prometheus-us-central1.grafana.net",
			}
			return &stack, nil
		},
	}

	promNamespace := "prometheusNamespace"
	secret := createSecretStub(promNamespace)

	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.storageAnnotationKey: "grafanacloud",
	}
	podMonitorNamespace := newNamespace(podmonitor.Namespace)
	podMonitorNamespace.SetAnnotations(map[string]string{
		Config.stackNameAnnotationKey: "adevintaruntime",
	})
	podmonitor2 := podmonitor.DeepCopy()
	podmonitor2.Name = "podmonitorName2"

	expectedPrometheus := createPrometheusObject(promNamespace, "podmonitorNamespace")

	reconciler := newDefaultTestPodMonitorReconciler(t, secret, podmonitor, podmonitor2, podMonitorNamespace)
	reconciler.EnableMetricsRemoteWrite = true
	reconciler.PrometheusNamespace = promNamespace
	reconciler.GrafanaCloudClient = &grafanaCloudClient

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.Nil(t, err)
	require.NotNil(t, result)

	// The first reconcile should work to provide the expected context for the actual test case
	result, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor2.Namespace, Name: podmonitor2.Name}})
	require.NoError(t, err)
	require.NotNil(t, result)

	prometheuses := &monitoringv1.PrometheusList{}
	require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
	require.Len(t, prometheuses.Items, 1)
	assert.NotNil(t, prometheuses.Items[0])
	expectedPrometheus.Spec.PodMetadata.Annotations = prometheuses.Items[0].Spec.PodMetadata.Annotations
	expectedPrometheus.ResourceVersion = prometheuses.Items[0].ResourceVersion
	assert.Equal(t, *expectedPrometheus, *prometheuses.Items[0])

	podmonitors := &monitoringv1.PodMonitorList{}
	require.NoError(t, reconciler.Client.List(context.Background(), podmonitors))
	require.Len(t, podmonitors.Items, 2)
}

func TestReconcileHonoursResources(t *testing.T) {
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			stack := grafanacloud.Stack{
				PromURL: "https://prometheus-us-central1.grafana.net",
			}
			return &stack, nil
		},
	}

	prom := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")
	secret := createSecretStub(prom.Namespace)
	somegigabytes, _ := resource.ParseQuantity("2Gi")
	somecpu, _ := resource.ParseQuantity("1500m")
	resources := &corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			"cpu":    somecpu,
			"memory": somegigabytes,
		},
		Requests: corev1.ResourceList{
			"cpu":    somecpu,
			"memory": somegigabytes,
		},
	}
	prom.Spec.Resources = *resources
	expectedPortName := prom.Spec.PortName
	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.storageAnnotationKey: "grafanacloud",
	}
	podMonitorNamespace := newNamespace(podmonitor.Namespace)
	podMonitorNamespace.SetAnnotations(map[string]string{
		Config.stackNameAnnotationKey: "adevintaruntime",
	})

	prom.Spec.PortName = "wrongPortName"

	reconciler := newDefaultTestPodMonitorReconciler(t, secret, prom, podmonitor, newNamespace(prom.Namespace), podMonitorNamespace)
	reconciler.EnableMetricsRemoteWrite = true
	reconciler.PrometheusNamespace = prom.Namespace
	reconciler.GrafanaCloudClient = &grafanaCloudClient

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.NoError(t, err)
	require.NotNil(t, result)

	prometheuses := &monitoringv1.PrometheusList{}
	require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
	require.Len(t, prometheuses.Items, 1)
	assert.NotNil(t, prometheuses.Items[0])

	assert.Equal(t, expectedPortName, prometheuses.Items[0].Spec.PortName)
	assert.Equal(t, *resources, prometheuses.Items[0].Spec.Resources)
}

func TestDeleteOnePodMonitorDoesNotDeletePrometheus(t *testing.T) {
	prom := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")
	secret := createSecretStub(prom.Namespace)

	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.storageAnnotationKey: "grafanacloud",
	}
	podmonitor2 := podmonitor.DeepCopy()
	podmonitor2.Name = "podmonitorName2"
	now := metav1.NewTime(time.Now())

	podmonitor.DeletionTimestamp = &now
	podmonitor.Finalizers = append(podmonitor.Finalizers, Config.podmonitorFinalizer)

	reconciler := newDefaultTestPodMonitorReconciler(t, secret, prom, podmonitor, podmonitor2, newNamespace(prom.Namespace))
	reconciler.EnableMetricsRemoteWrite = true
	reconciler.PrometheusNamespace = prom.Namespace

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})

	prometheuses := &monitoringv1.PrometheusList{}
	require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
	require.Len(t, prometheuses.Items, 1)
	assert.NotNil(t, prometheuses.Items[0])
	prom.Spec.PodMetadata.Annotations = prometheuses.Items[0].Spec.PodMetadata.Annotations
	require.Nil(t, err)
	require.NotNil(t, result)
	assert.Equal(t, *prom, *prometheuses.Items[0])
}

func TestDeleteLastPodMonitorDeletesPrometheus(t *testing.T) {
	prom := createPrometheusObject("prometheusesNamespace", "podmonitorNamespace")

	secret := createSecretStub(prom.Namespace)

	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.storageAnnotationKey: "grafanacloud",
	}
	now := metav1.NewTime(time.Now())

	podmonitor.ObjectMeta.Finalizers = append(podmonitor.ObjectMeta.Finalizers, Config.podmonitorFinalizer)
	podmonitor.DeletionTimestamp = &now

	reconciler := newDefaultTestPodMonitorReconciler(t, secret, prom, podmonitor, newNamespace(prom.Namespace))
	reconciler.EnableMetricsRemoteWrite = true

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.Nil(t, err)
	require.NotNil(t, result)
	prometheuses := &monitoringv1.PrometheusList{}
	require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
	assert.Len(t, prometheuses.Items, 0)
}

func TestPodMonitorDeFederateNamespace(t *testing.T) {
	// The result should be a Prometheus Object whose storage is only `grafanacloud`
	// Even though EnableFederation = true
	ns := &corev1.Namespace{
		ObjectMeta: ctrl.ObjectMeta{
			Name: "podmonitorNamespace",
			Annotations: map[string]string{
				Config.storageAnnotationKey:   "grafanacloud",
				Config.stackNameAnnotationKey: "adevintaruntime",
			},
		},
	}

	var replicas int32 = 1
	var shards int32 = 1
	prom := &monitoringv1.Prometheus{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "podmonitorNamespace",
			Namespace: "prometheusesNamespace",
			Annotations: map[string]string{
				Config.storageAnnotationKey: "grafanacloud",
				Config.accountAnnotationKey: "podmonitorNamespace",
			},
		},
		Spec: monitoringv1.PrometheusSpec{
			AdditionalScrapeConfigs: &corev1.SecretKeySelector{
				Key:                  "prometheus",
				LocalObjectReference: corev1.LocalObjectReference{Name: "prometheus-podmonitorNamespace-monitoring-target"},
			},
			PodMetadata: &monitoringv1.EmbeddedObjectMetadata{
				Labels: map[string]string{
					Config.accountLabelKey: "podmonitorNamespace",
				},
			},
			ServiceMonitorSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{Config.accountLabelKey: "podmonitorNamespace"},
			},
			PodMonitorSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{Config.accountLabelKey: "podmonitorNamespace"},
			},
			RuleNamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": "podmonitorNamespace",
				},
			},
			ServiceMonitorNamespaceSelector: &metav1.LabelSelector{},
			PodMonitorNamespaceSelector:     &metav1.LabelSelector{},
			RoutePrefix:                     "/",
			ServiceAccountName:              "prometheus-tenant",
			PortName:                        "web",
			Version:                         "v0.0.1",
			BaseImage:                       "foo",
			Replicas:                        &replicas,
			Shards:                          &shards,
			LogLevel:                        "warn",
			LogFormat:                       "logfmt",
			Retention:                       "30m",
			Resources:                       defaultResourceRequirements,
			ExternalLabels: map[string]string{
				"cluster":     "clustername",
				"region":      "eu-fake-1",
				"account":     "podmonitorNamespace",
				"monitor":     "prometheus-local",
				"mycustomkey": "mycustomvalue",
			},
			ExternalURL: "http://podmonitorNamespace-metrics.clustername.clusterdomain",
			RemoteWrite: []monitoringv1.RemoteWriteSpec{{
				URL: "https://prometheus-us-central1.grafana.net/api/prom/push",
				BasicAuth: &monitoringv1.BasicAuth{
					Username: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "observability-operator-grafana-cloud-credentials",
						},
						Key: "adevintaruntime",
					},
					Password: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "observability-operator-grafana-cloud-credentials",
						},
						Key: "grafana-cloud-api-key",
					},
				},
				WriteRelabelConfigs: []monitoringv1.RelabelConfig{
					{
						Action:       "drop",
						SourceLabels: []string{"__name__"},
						Regex:        "prometheus_.*",
					},
				},
			}},
			RuleSelector: &metav1.LabelSelector{},
		},
	}

	podmonitor := monitoringv1.PodMonitor{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "podmonitorName",
			Namespace: "podmonitorNamespace",
			Labels: map[string]string{
				Config.podMonitorLabelKey: "podmonitorNamespace-podmonitorName",
				Config.accountLabelKey:    "podmonitorNamespace",
			},
		},
		Spec: monitoringv1.PodMonitorSpec{
			PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{
				{
					Path:        "/metrics",
					HonorLabels: true,
				},
			},
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"random_label": "true",
				},
			},
			NamespaceSelector: monitoringv1.NamespaceSelector{MatchNames: []string{"bar"}},
			SampleLimit:       1500,
		},
	}
	secret := createSecretStub(prom.Namespace)

	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			stack := grafanacloud.Stack{
				PromURL: "https://prometheus-us-central1.grafana.net",
			}
			return &stack, nil
		},
	}

	reconciler := newDefaultTestPodMonitorReconciler(t, ns, &podmonitor, secret, prom)
	reconciler.EnableMetricsRemoteWrite = true
	reconciler.PrometheusNamespace = prom.Namespace
	reconciler.GrafanaCloudClient = &grafanaCloudClient

	annotation := map[string]string{
		Config.storageAnnotationKey: "grafanacloud",
		Config.accountAnnotationKey: "accountName",
	}

	podmonitor.ObjectMeta.Annotations = annotation
	require.NoError(t, reconciler.Client.Update(context.Background(), &podmonitor))

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.NoError(t, err)
	assert.NotNil(t, result)

	prometheuses := &monitoringv1.PrometheusList{}
	require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
	require.Len(t, prometheuses.Items, 1)
	require.NotNil(t, prometheuses.Items[0])

	prom.Spec.PodMetadata.Annotations = prometheuses.Items[0].Spec.PodMetadata.Annotations
	prom.ResourceVersion = prometheuses.Items[0].ResourceVersion
	prom.TypeMeta = prometheuses.Items[0].TypeMeta
	assert.Equal(t, *prom, *prometheuses.Items[0])
}

func TestPodMonitorWithAlternativeStorageEnabled(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "observability-operator-grafana-cloud-credentials",
			Namespace: "platform-services",
		},
		Data: map[string][]uint8{
			"grafana-cloud-api-key": []byte("xxx"),
		},
	}
	podmonitor := monitoringv1.PodMonitor{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "podmonitorName",
			Namespace: "podmonitorNamespace",
			Labels: map[string]string{
				Config.podMonitorLabelKey: "podmonitorNamespace-podmonitorName",
				Config.accountLabelKey:    "podmonitorNamespace",
			},
			Annotations: map[string]string{
				Config.storageAnnotationKey: "federation",
				Config.accountAnnotationKey: "podmonitorNamespace",
			},
		},
		Spec: monitoringv1.PodMonitorSpec{
			PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{
				{
					Path:        "/metrics",
					HonorLabels: true,
				},
			},
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"random_label": "true",
				},
			},
			NamespaceSelector: monitoringv1.NamespaceSelector{MatchNames: []string{"bar"}},
			SampleLimit:       1500,
		},
	}
	ns := newNamespace(podmonitor.Namespace)
	ns.Annotations = map[string]string{
		Config.storageAnnotationKey: "federation",
	}
	reconciler := newDefaultTestPodMonitorReconciler(t, &podmonitor, secret, ns)
	reconciler.EnableMetricsRemoteWrite = false

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	assert.Nil(t, err)
	assert.NotNil(t, result)

	prometheuses := &monitoringv1.PrometheusList{}
	require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
	assert.Len(t, prometheuses.Items, 1)
	assert.Empty(t, prometheuses.Items[0].Spec.RemoteWrite)
}

func TestVpaEnabledCreatesVpaObject(t *testing.T) {
	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.storageAnnotationKey: "grafanacloud",
	}
	podmonitor.Name = "podmonitorName"
	podmonitor.Namespace = "my-podmonitor-namespace-pro"
	reconciler := newDefaultTestPodMonitorReconciler(t, podmonitor, newNamespace(podmonitor.Namespace))
	reconciler.EnableVpa = true

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.NoError(t, err)
	assert.NotNil(t, result)

	vpas := &vpav1.VerticalPodAutoscalerList{}
	require.NoError(t, reconciler.Client.List(context.Background(), vpas))
	require.Len(t, vpas.Items, 1)
	require.NotNil(t, vpas.Items[0].Spec.UpdatePolicy.UpdateMode)
	assert.Equal(t, vpav1.UpdateMode("Auto"), *vpas.Items[0].Spec.UpdatePolicy.UpdateMode)
	assert.Equal(t, podmonitor.Namespace, vpas.Items[0].Name)
	assert.Equal(t, reconciler.PrometheusNamespace, vpas.Items[0].Namespace)
	require.Len(t, vpas.Items[0].OwnerReferences, 1)
	assert.Equal(t, monitoringv1.SchemeGroupVersion.Identifier(), vpas.Items[0].OwnerReferences[0].APIVersion)
	assert.Equal(t, "Prometheus", vpas.Items[0].OwnerReferences[0].Kind)
	assert.Equal(t, "my-podmonitor-namespace-pro", vpas.Items[0].OwnerReferences[0].Name)

	result, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	require.NoError(t, err)
	assert.NotNil(t, result)

	vpas = &vpav1.VerticalPodAutoscalerList{}
	require.NoError(t, reconciler.Client.List(context.Background(), vpas))
	require.Len(t, vpas.Items, 1)
	assert.Equal(t, "monitoring.coreos.com/v1", vpas.Items[0].Spec.TargetRef.APIVersion)
	assert.Equal(t, "Prometheus", vpas.Items[0].Spec.TargetRef.Kind)
	assert.Equal(t, "my-podmonitor-namespace-pro", vpas.Items[0].Spec.TargetRef.Name)
	require.NotNil(t, vpas.Items[0].Spec.UpdatePolicy.UpdateMode)
	assert.Equal(t, vpav1.UpdateMode("Auto"), *vpas.Items[0].Spec.UpdatePolicy.UpdateMode)
	assert.Equal(t, podmonitor.Namespace, vpas.Items[0].Name)
	assert.Equal(t, reconciler.PrometheusNamespace, vpas.Items[0].Namespace)
	require.Len(t, vpas.Items[0].OwnerReferences, 1)
	assert.Equal(t, monitoringv1.SchemeGroupVersion.Identifier(), vpas.Items[0].OwnerReferences[0].APIVersion)
	assert.Equal(t, "Prometheus", vpas.Items[0].OwnerReferences[0].Kind)
	assert.Equal(t, "my-podmonitor-namespace-pro", vpas.Items[0].OwnerReferences[0].Name)
}

func TestVpaDisabledSkipsVpaObjectCreation(t *testing.T) {
	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.storageAnnotationKey: "grafanacloud",
	}
	podmonitor.Name = "podmonitorName"
	podmonitor.Namespace = "my-podmonitor-namespace-pro"
	reconciler := newDefaultTestPodMonitorReconciler(t)
	reconciler.EnableVpa = false

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
	assert.Nil(t, err)
	assert.NotNil(t, result)

	vpas := &vpav1.VerticalPodAutoscalerList{}
	require.NoError(t, reconciler.Client.List(context.Background(), vpas))
	require.Len(t, vpas.Items, 0)
}

func TestGetAvailableStorageFromNamespace(t *testing.T) {
	type TestCase struct {
		Description    string
		Name           string
		Namespace      corev1.Namespace
		ExpectedResult string
		ExpectedError  error
	}

	testCases := []TestCase{
		{
			Description: "when the namespace exists and has the storage annotation",
			Name:        "mynamespace",
			Namespace: corev1.Namespace{
				ObjectMeta: ctrl.ObjectMeta{
					Name: "mynamespace",
					Annotations: map[string]string{
						Config.storageAnnotationKey: "grafanacloud",
					},
				},
			},
			ExpectedResult: "grafanacloud",
			ExpectedError:  nil,
		},
		{
			Description: "when the namespace exists and does not have the storage annotation",
			Name:        "subito-pro",
			Namespace: corev1.Namespace{
				ObjectMeta: ctrl.ObjectMeta{
					Name:        "subito-pro",
					Annotations: map[string]string{},
				},
			},
			ExpectedResult: "grafanacloud",
			ExpectedError:  nil,
		},
		{
			Description: "when the namespace exists and does not have an empty storage annotation",
			Name:        "some-namespace",
			Namespace: corev1.Namespace{
				ObjectMeta: ctrl.ObjectMeta{
					Name: "some-namespace",
					Annotations: map[string]string{
						Config.storageAnnotationKey: "",
					},
				},
			},
			ExpectedResult: "",
			ExpectedError:  nil,
		},
		{
			Description: "when the namespace does not exist an error is returned",
			Name:        "notfound",
			Namespace: corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "other-namespace"},
			},
			ExpectedResult: "",
			ExpectedError:  k8serrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "notfound"),
		},
	}
	for _, item := range testCases {
		t.Run(item.Description, func(t *testing.T) {
			reconciler := newDefaultTestPodMonitorReconciler(t, &item.Namespace)
			expectedResult, expectedErr := getAvailableStorageFromNamespace(reconciler.Client, item.Name, reconciler.Log)
			assert.Equal(t, item.ExpectedResult, expectedResult)
			assert.Equal(t, item.ExpectedError, expectedErr)
		})
	}
}

func TestPodMonitorWithCustomStorageCreatesSecretInPromNamespaceAndRemoteWrite(t *testing.T) {
	promNamespace := "prometheusesNamespace"

	ns := newNamespace("podmonitorNamespace")
	ns.Annotations = map[string]string{
		Config.remoteWriteAnnotationKey: "othernamespace/secret-storage",
	}

	remoteWrite, secret1, secret2 := createCustomStorageSecretStub("othernamespace")

	podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
	podmonitor.Annotations = map[string]string{
		Config.storageAnnotationKey: "custom",
	}
	reconciler := newDefaultTestPodMonitorReconciler(t, podmonitor, newNamespace(promNamespace), ns, newNamespace("othernamespace"), remoteWrite, secret1, secret2)
	reconciler.PrometheusNamespace = promNamespace

	t.Run("Reconcile wont fail", func(t *testing.T) {
		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
		require.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("Secrets are copied to prometheus namespace", func(t *testing.T) {
		createdSecret1 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "prometheusesNamespace",
				Name:      "custom-remote-write-secret1-othernamespace",
			},
		}
		require.NoError(t, reconciler.Client.Get(context.Background(), client.ObjectKeyFromObject(createdSecret1), createdSecret1))
		require.Equal(t, string(createdSecret1.Data["username"]), "foo")

		createdSecret2 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "prometheusesNamespace",
				Name:      "custom-remote-write-secret2-othernamespace",
			},
		}
		require.NoError(t, reconciler.Client.Get(context.Background(), client.ObjectKeyFromObject(createdSecret2), createdSecret2))
		require.Equal(t, string(createdSecret2.Data["password"]), "bar")
	})

	t.Run("prometheus object is created with the proper remote write ", func(t *testing.T) {
		prometheus := monitoringv1.Prometheus{
			ObjectMeta: metav1.ObjectMeta{Name: "podmonitorNamespace", Namespace: promNamespace},
		}

		require.NoError(t, reconciler.Client.Get(context.Background(), client.ObjectKeyFromObject(&prometheus), &prometheus))

		require.NotEmpty(t, prometheus.Spec.RemoteWrite)
		remotewriteSpec := monitoringv1.RemoteWriteSpec{
			URL: "https://victoria-metrics-url",
			BasicAuth: &monitoringv1.BasicAuth{
				Username: corev1.SecretKeySelector{
					Key:                  "username",
					LocalObjectReference: corev1.LocalObjectReference{Name: "custom-remote-write-secret1-othernamespace"},
				},
				Password: corev1.SecretKeySelector{
					Key:                  "password",
					LocalObjectReference: corev1.LocalObjectReference{Name: "custom-remote-write-secret2-othernamespace"},
				},
			},
		}
		require.Equal(t, prometheus.Spec.RemoteWrite[0], remotewriteSpec)
	})
}

func TestPodMonitorWithCustomStorageErrors(t *testing.T) {
	promNamespace := "prometheusesNamespace"
	resetRenconciler := func() *PodMonitorReconciler {
		ns := newNamespace("podmonitorNamespace")
		ns.Annotations = map[string]string{
			Config.remoteWriteAnnotationKey: "othernamespace/secret-storage",
		}

		secret1, secret2, secret3 := createCustomStorageSecretStub("othernamespace")

		podmonitor := createPodMonitor("podmonitorNamespace", "podmonitorName")
		podmonitor.Annotations = map[string]string{
			Config.storageAnnotationKey: "custom",
		}
		reconciler := newDefaultTestPodMonitorReconciler(t, secret1, secret2, secret3, podmonitor, newNamespace(promNamespace), ns, newNamespace("othernamespace"))
		reconciler.PrometheusNamespace = promNamespace
		return reconciler
	}

	t.Run("non existing referenced secrets will make it fail", func(t *testing.T) {
		reconciler := resetRenconciler()
		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "secret-storage",
				Namespace: "othernamespace",
			},
		}
		_, err := ctrl.CreateOrUpdate(context.Background(), reconciler.Client, &secret, func() error {
			secret.Annotations[Config.referencedSecretAnnotationKeys] = "undefinedsecret"
			return nil
		})
		require.NoError(t, err)

		_, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "podmonitorNamespace", Name: "podmonitorName"}})
		require.Error(t, err)
	})

	t.Run("Non existing secret will fail make it", func(t *testing.T) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "podmonitorNamespace",
			},
		}

		reconciler := resetRenconciler()
		_, err := ctrl.CreateOrUpdate(context.Background(), reconciler.Client, ns, func() error {
			ns.Annotations[Config.remoteWriteAnnotationKey] = "notfoundsecret"
			return nil
		})
		require.NoError(t, err)
		_, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "podmonitorNamespace", Name: "podmonitorName"}})
		require.Error(t, err)
	})

	t.Run("unmarshallable remote-write will make it fail and report an event", func(t *testing.T) {
		reconciler := resetRenconciler()
		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "secret-storage",
				Namespace: "othernamespace",
			},
		}
		_, err := ctrl.CreateOrUpdate(context.Background(), reconciler.Client, &secret, func() error {
			secret.Data["remote-write"] = []byte("foo")
			return nil
		})
		require.NoError(t, err)

		_, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "podmonitorNamespace", Name: "podmonitorName"}})
		require.Error(t, err)

		event := corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "GrafanaCloudOperatorEvent",
				Namespace: "podmonitorNamespace",
			},
		}
		err = reconciler.Client.Get(context.Background(), client.ObjectKeyFromObject(&event), &event)
		require.NoError(t, err)
	})
}

func createCustomAlertmanagerConfigMap(namespace, name, payload string) corev1.ConfigMap {
	return corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string]string{
			"alert-manager": payload,
		},
	}
}

func TestPodMonitorWithAlertManager(t *testing.T) {
	cmName := "alert-manager-config"
	namespace := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "user-namespace",
			Annotations: map[string]string{
				Config.storageAnnotationKey:      "grafanacloud",
				Config.alertManagerAnnotationKey: cmName,
			},
		},
	}
	podmonitor := monitoringv1.PodMonitor{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "podmonitorName",
			Namespace: namespace.Name,
			Annotations: map[string]string{
				Config.storageAnnotationKey:      "grafanacloud",
				Config.alertManagerAnnotationKey: cmName,
			},
		},
	}

	t.Run("Fail with invalid Alertmanager configuration", func(t *testing.T) {
		cm := createCustomAlertmanagerConfigMap(namespace.Name, cmName, "is this not a valid json/yaml object?")

		reconciler := newDefaultTestPodMonitorReconciler(
			t,
			&podmonitor,
			&namespace,
			&cm,
			newNamespace("prometheusesNamespace"),
		)
		event := corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "GrafanaCloudOperatorEvent",
				Namespace: namespace.Name,
			},
		}
		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
		require.Error(t, err)
		require.NotNil(t, result)
		require.NoError(t, reconciler.Client.Get(context.Background(), client.ObjectKeyFromObject(&event), &event))
	})
	t.Run("Correct Alertmanager configuration in YAML", func(t *testing.T) {
		cm := createCustomAlertmanagerConfigMap(
			namespace.Name,
			cmName,
			`
                         - namespace: javi-test
                           name: alertmanager-example-test
                           port: web
                         - namespace: javi-test
                           name: alertmanager-example-test-2
                           port: web`,
		)

		reconciler := newDefaultTestPodMonitorReconciler(
			t,
			&podmonitor,
			&namespace,
			&cm,
			newNamespace("prometheusesNamespace"),
		)

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
		require.NoError(t, err)
		require.NotNil(t, result)

		prometheuses := &monitoringv1.PrometheusList{}
		expectedPrometheus := createPrometheusObject("prometheusesNamespace", namespace.Name)

		require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
		require.Len(t, prometheuses.Items, 1)
		expectedPrometheus.Spec.Alerting = &monitoringv1.AlertingSpec{
			Alertmanagers: []monitoringv1.AlertmanagerEndpoints{
				{
					Namespace: "javi-test",
					Name:      "alertmanager-example-test",
					Port:      intstr.FromString("web"),
				},
				{
					Namespace: "javi-test",
					Name:      "alertmanager-example-test-2",
					Port:      intstr.FromString("web"),
				},
			},
		}
		assert.Equal(t, *expectedPrometheus.Spec.Alerting, *prometheuses.Items[0].Spec.Alerting)
	})
	t.Run("Correct Alertmanager configuration in JSON", func(t *testing.T) {
		cm := createCustomAlertmanagerConfigMap(
			namespace.Name,
			cmName,
			`[
                          {
                            "namespace": "javi-test",
                            "name": "alertmanager-example-test",
                            "port": "web"
                          },
                          {
                            "namespace": "javi-test",
                            "name": "alertmanager-example-test-2",
                            "port": "web"
                          }
                        ]`,
		)

		reconciler := newDefaultTestPodMonitorReconciler(
			t,
			&podmonitor,
			&namespace,
			&cm,
			newNamespace("prometheusesNamespace"),
		)

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: podmonitor.Namespace, Name: podmonitor.Name}})
		require.NoError(t, err)
		require.NotNil(t, result)

		prometheuses := &monitoringv1.PrometheusList{}
		expectedPrometheus := createPrometheusObject("prometheusesNamespace", namespace.Name)

		require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
		require.Len(t, prometheuses.Items, 1)
		expectedPrometheus.Spec.Alerting = &monitoringv1.AlertingSpec{
			Alertmanagers: []monitoringv1.AlertmanagerEndpoints{
				{
					Namespace: "javi-test",
					Name:      "alertmanager-example-test",
					Port:      intstr.FromString("web"),
				},
				{
					Namespace: "javi-test",
					Name:      "alertmanager-example-test-2",
					Port:      intstr.FromString("web"),
				},
			},
		}
		assert.Equal(t, *expectedPrometheus.Spec.Alerting, *prometheuses.Items[0].Spec.Alerting)
	})
}
