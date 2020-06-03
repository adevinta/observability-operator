package controllers

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/adevinta/observability-operator/pkg/grafanacloud"
	"github.com/adevinta/observability-operator/pkg/test_helpers"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apilabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	types "k8s.io/apimachinery/pkg/types"
	validatingfield "k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type GrafanaCloudUpdaterFunc struct {
	InjectFluentdLokiConfigurationCalls int
	InjectFluentdLokiConfigurationFunc  func(context.Context) error
}

func (u *GrafanaCloudUpdaterFunc) InjectFluentdLokiConfiguration(ctx context.Context) error {
	u.InjectFluentdLokiConfigurationCalls++
	if u.InjectFluentdLokiConfigurationFunc != nil {
		return u.InjectFluentdLokiConfigurationFunc(ctx)
	}
	return errors.New("not implemented")
}

func newDefaultNamespaceReconciler(t *testing.T, initialObjects ...runtime.Object) *NamespaceReconciler {
	scheme := runtime.NewScheme()
	_ = monitoringv1.AddToScheme(scheme)

	fakeClient := test_helpers.NewFakeClient(t, initialObjects...)

	os.Setenv("GRAFANA_CLOUD_TOKEN", "zzzz")
	grafanaCloudClient := mockGrafanaCloudClient{
		GetStackFunc: func(tenant string) (*grafanacloud.Stack, error) {
			return nil, nil
		},
		GetTracesConnectionFunc: func(stack string) (int, string, error) {
			return 0, "", nil
		},
	}

	return &NamespaceReconciler{
		Log:                     ctrl.Log.WithName("controllers").WithName("Namespace"),
		Client:                  fakeClient,
		TracesNamespace:         "observability",
		ClusterName:             "clustername",
		GrafanaCloudClient:      &grafanaCloudClient,
		GrafanaCloudTracesToken: "GCO_TRACES_TOKEN",
		GrafanaCloudUpdater: &GrafanaCloudUpdaterFunc{
			InjectFluentdLokiConfigurationFunc: func(context.Context) error {
				return nil
			},
		},
	}
}

func TestNamespaceReconciliationFailsWhenGrafanaCloudConfigurationFails(t *testing.T) {
	ns := newNamespace("my-namespace")
	reconciler := newDefaultNamespaceReconciler(t, ns)
	testError := errors.New("namespace reconciliation should fail when grafana cloud configuration fails")
	gcUpdater := &GrafanaCloudUpdaterFunc{
		InjectFluentdLokiConfigurationFunc: func(context.Context) error {
			return testError
		},
	}
	reconciler.GrafanaCloudUpdater = gcUpdater
	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "my-namespace"}})
	assert.Equal(t, testError, err)
	assert.True(t, result.Requeue)
}

func TestNamespaceReconciliationSucceedsWhenGrafanaCloudConfigurationSucceeds(t *testing.T) {
	reconciler := newDefaultNamespaceReconciler(t)
	gcUpdater := &GrafanaCloudUpdaterFunc{
		InjectFluentdLokiConfigurationFunc: func(context.Context) error {
			return nil
		},
	}
	reconciler.GrafanaCloudUpdater = gcUpdater
	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "my-namespace"}})
	assert.Nil(t, err)
	assert.False(t, result.Requeue)
}

func TestShouldDoNothingWhenNamespaceIsIgnored(t *testing.T) {
	reconciler := newDefaultNamespaceReconciler(t)
	reconciler.IgnoreNamespaces = []string{"my-ignored-namespace"}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-ignored-namespace"}})
	assert.Nil(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestShouldHonorExcludeNamespaceSelectors(t *testing.T) {
	pm1 := createPodMonitor("ignore-me", "test-pm")
	annotations1 := map[string]string{Config.stackNameAnnotationKey: "wont-be-changed"}
	pm1.Annotations = annotations1

	pm2 := createPodMonitor("dont-ignore-me", "test-pm")
	annotations2 := map[string]string{Config.stackNameAnnotationKey: "will-be-changed"}
	pm2.Annotations = annotations2

	pm3 := createPodMonitor("empty-selector-dont-ignore-me", "test-pm")
	annotations3 := map[string]string{Config.stackNameAnnotationKey: "will-be-changed"}
	pm3.Annotations = annotations3

	ns1 := newNamespace("ignore-me")
	ns1.Labels = map[string]string{"label": "ignoreme"}
	ns1.Annotations = map[string]string{Config.stackNameAnnotationKey: "gcstack"}
	ns2 := newNamespace("dont-ignore-me")
	ns2.Labels = map[string]string{"label": "ignoreyou"}
	ns2.Annotations = map[string]string{Config.stackNameAnnotationKey: "gcstack"}
	ns3 := newNamespace("empty-selector-dont-ignore-me")
	ns3.Labels = map[string]string{"label": "ignoreyou"}
	ns3.Annotations = map[string]string{Config.stackNameAnnotationKey: "gcstack"}

	reconciler := newDefaultNamespaceReconciler(t, ns1, ns2, ns3, pm1, pm2, pm3)

	path := validatingfield.NewPath("metadata", "labels")
	filter, err := apilabels.Parse("label=ignoreme", validatingfield.WithPath(path))
	assert.NoError(t, err)
	reconciler.ExcludeNamespaceLabelSelector = filter

	t.Run(" Should ignore the namespace when the selector matches", func(t *testing.T) {
		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ignore-me"}})
		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
		pm := &monitoringv1.PodMonitor{}
		require.NoError(t, reconciler.Client.Get(context.Background(), types.NamespacedName{Name: "test-pm", Namespace: "ignore-me"}, pm))
	})
	t.Run(" Should reconcile the namespace when the selector doesn't match, and update the podMonitor annotation", func(t *testing.T) {
		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "dont-ignore-me"}})
		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
		pm := &monitoringv1.PodMonitor{}
		require.NoError(t, reconciler.Client.Get(context.Background(), types.NamespacedName{Name: "test-pm", Namespace: "dont-ignore-me"}, pm))
		require.Equal(t, "gcstack", pm.Annotations[Config.stackNameAnnotationKey])
	})

	t.Run(" Should ignore empty Selectors and reconcile the namespace, and update the podMonitor annotation", func(t *testing.T) {
		// empty selector
		filter, err := apilabels.Parse("", validatingfield.WithPath(path))
		assert.NoError(t, err)
		reconciler.ExcludeNamespaceLabelSelector = filter

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "empty-selector-dont-ignore-me"}})
		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
		pm := &monitoringv1.PodMonitor{}
		require.NoError(t, reconciler.Client.Get(context.Background(), types.NamespacedName{Name: "test-pm", Namespace: "empty-selector-dont-ignore-me"}, pm))
		require.Equal(t, "gcstack", pm.Annotations[Config.stackNameAnnotationKey])
	})

}

func TestShouldDoNothingWhenLabelsAreIgnored(t *testing.T) {
	reconciler := newDefaultNamespaceReconciler(t)
	reconciler.IgnoreNamespaces = []string{"my-ignored-namespace"}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-ignored-namespace"}})
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestShouldDoNotCreatePrometheusWhenMetricsAnnotationIsDisabled(t *testing.T) {
	ns := newNamespace("my-namespace")

	ns.Annotations = map[string]string{Config.storageAnnotationKey: "grafanacloud"}
	ns.Labels = map[string]string{Config.metricsLabelKey: "disabled"}

	reconciler := newDefaultNamespaceReconciler(t, ns, createSecretStub("platform-services"), createPodStub("test-pod", "my-namespace"))
	gcUpdater := &GrafanaCloudUpdaterFunc{
		InjectFluentdLokiConfigurationFunc: func(context.Context) error {
			return nil
		},
	}
	reconciler.GrafanaCloudUpdater = gcUpdater

	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "my-namespace"}})
	require.Nil(t, err)
	assert.NotNil(t, result)

	prometheuses := &monitoringv1.PrometheusList{}

	require.NoError(t, reconciler.Client.List(context.Background(), prometheuses))
	require.Len(t, prometheuses.Items, 0)
	assert.Equal(t, 1, gcUpdater.InjectFluentdLokiConfigurationCalls)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestShouldDoNotInjectLogsAnnotationIsDisabled(t *testing.T) {
	reconciler := newDefaultNamespaceReconciler(
		t,
		&corev1.Namespace{
			ObjectMeta: ctrl.ObjectMeta{
				Name: "my-namespace",
				Labels: map[string]string{
					Config.logsLabelKey: "disabled",
				},
			},
		})
	gcUpdater := &GrafanaCloudUpdaterFunc{
		InjectFluentdLokiConfigurationFunc: func(context.Context) error {
			return nil
		},
	}
	reconciler.GrafanaCloudUpdater = gcUpdater
	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-namespace"}})
	assert.Nil(t, err)
	assert.Equal(t, 0, gcUpdater.InjectFluentdLokiConfigurationCalls)
}

func TestShouldDoNothingWhenNamespaceIsNotFound(t *testing.T) {
	reconciler := newDefaultNamespaceReconciler(
		t,
		&corev1.Namespace{
			ObjectMeta: ctrl.ObjectMeta{
				Name: "my-namespace",
			},
		})

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "not-found"}})
	assert.Nil(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestNamespaceReconciliationShouldNotUpdateOtherNamespacesPodMonitors(t *testing.T) {
	reconciler := newDefaultNamespaceReconciler(
		t,
		&corev1.Namespace{
			ObjectMeta: ctrl.ObjectMeta{
				Name: "my-namespace",
				Annotations: map[string]string{
					Config.storageAnnotationKey: "new-storage",
				},
			},
		},
		&monitoringv1.PodMonitor{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "some-pod-monitor",
				Namespace: "other-namespace",
				Annotations: map[string]string{
					Config.storageAnnotationKey: "some-storage",
				},
			},
		},
		&monitoringv1.PodMonitor{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "some-pod-monitor",
				Namespace: "my-namespace",
				Annotations: map[string]string{
					Config.storageAnnotationKey: "some-storage",
				},
			},
		},
	)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-namespace"}})
	assert.Nil(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	podMonitor := &monitoringv1.PodMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "my-namespace",
			Name:      "some-pod-monitor",
		},
	}
	require.NoError(t, reconciler.Client.Get(context.Background(), client.ObjectKeyFromObject(podMonitor), podMonitor))
	assert.Equal(t, "new-storage", podMonitor.Annotations[Config.storageAnnotationKey])

	podMonitor = &monitoringv1.PodMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "other-namespace",
			Name:      "some-pod-monitor",
		},
	}
	require.NoError(t, reconciler.Client.Get(context.Background(), client.ObjectKeyFromObject(podMonitor), podMonitor))
	assert.Equal(t, "some-storage", podMonitor.Annotations[Config.storageAnnotationKey])
}

func TestShouldUpdatePodMonitorAnnotationWhenNamespaceChanges(t *testing.T) {
	type TestCase struct {
		Description                   string
		Namespace                     corev1.Namespace
		GivenPodMonitorAnnotations    map[string]string
		ExpectedPodMonitorAnnotations map[string]string
	}

	testCases := []TestCase{
		{
			Description: "When the podmonitor and the namespace have no storage annotation but others",
			Namespace: corev1.Namespace{
				ObjectMeta: ctrl.ObjectMeta{
					Annotations: map[string]string{
						"should-not-add-this": "irrelevant",
					},
				},
			},
			GivenPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-1",
			},
			ExpectedPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-1",
			},
		},
		{
			Description: "When the pod monitor has more annotations and the namespace has no storage, but additional, annotations",
			Namespace: corev1.Namespace{
				ObjectMeta: ctrl.ObjectMeta{
					Annotations: map[string]string{
						"should-not-add-this": "irrelevant",
					},
				},
			},
			GivenPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-2",
				Config.storageAnnotationKey:        "storage",
			},
			ExpectedPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-2",
				Config.storageAnnotationKey:        "storage",
			},
		},
		{
			Description: "When the pod monitor has more annotations and the neither the namespace nor the pod monitor have no storage annotation",
			Namespace: corev1.Namespace{
				ObjectMeta: ctrl.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			GivenPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-3",
			},
			ExpectedPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-3",
			},
		},
		{
			Description: "When the pod monitor has more annotations and the namespace has no storage annotation",
			Namespace: corev1.Namespace{
				ObjectMeta: ctrl.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			GivenPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-4",
				Config.storageAnnotationKey:        "storage",
			},
			ExpectedPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-4",
				Config.storageAnnotationKey:        "storage",
			},
		},
		{
			Description: "When the pod monitor has more annotations and the namespace has a storage annotation",
			Namespace: corev1.Namespace{
				ObjectMeta: ctrl.ObjectMeta{
					Annotations: map[string]string{
						Config.storageAnnotationKey: "federation,grafanacloud",
					},
				},
			},
			GivenPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-5",
				Config.storageAnnotationKey:        "old-value",
			},
			ExpectedPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-5",
				Config.storageAnnotationKey:        "federation,grafanacloud",
			},
		},
		{
			Description: "When the namespace defines an empty storage",
			Namespace: corev1.Namespace{
				ObjectMeta: ctrl.ObjectMeta{
					Annotations: map[string]string{
						Config.storageAnnotationKey: "",
					},
				},
			},
			GivenPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-5",
				Config.storageAnnotationKey:        "old-value",
			},
			ExpectedPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-5",
				Config.storageAnnotationKey:        "",
			},
		},
		{
			Description: "When adding the annotation for alertmanager",
			Namespace: corev1.Namespace{
				ObjectMeta: ctrl.ObjectMeta{
					Annotations: map[string]string{
						Config.storageAnnotationKey:      "new-storage",
						Config.alertManagerAnnotationKey: "test",
					},
				},
			},
			GivenPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-5",
				Config.storageAnnotationKey:        "new-storage",
			},
			ExpectedPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-5",
				Config.storageAnnotationKey:        "new-storage",
				Config.alertManagerAnnotationKey:   "test",
			},
		},
		{
			Description: "When adding the annotation for grafanacloud",
			Namespace: corev1.Namespace{
				ObjectMeta: ctrl.ObjectMeta{
					Annotations: map[string]string{
						Config.stackNameAnnotationKey: "adevintastacktest",
					},
				},
			},
			GivenPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-5",
			},
			ExpectedPodMonitorAnnotations: map[string]string{
				"should-not-touch-this-annotation": "my-value-5",
				Config.stackNameAnnotationKey:      "adevintastacktest",
			},
		},
	}

	for _, item := range testCases {
		t.Run(item.Description, func(t *testing.T) {
			item.Namespace.Name = "my-namespace"
			podmonitor := createPodMonitor("my-namespace", "podmonitor")
			podmonitor.ObjectMeta.Annotations = item.GivenPodMonitorAnnotations

			reconciler := newDefaultNamespaceReconciler(t, podmonitor, &item.Namespace)

			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-namespace"}})
			assert.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)
			podMonitorList := &monitoringv1.PodMonitorList{}
			require.NoError(t, reconciler.Client.List(context.Background(), podMonitorList))
			require.Len(t, podMonitorList.Items, 1)
			assert.Equal(t, item.ExpectedPodMonitorAnnotations, podMonitorList.Items[0].ObjectMeta.Annotations)
		})
	}
}

func createConfigMapRulesStub(name, namespace string) *corev1.ConfigMap {
	relabelConfigs := `
    - regex: ^prometheus$
      action: labeldrop
    - regex: ^node_exporter$
      action: labeldrop
    `

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": name},
		},
		Data: map[string]string{
			"relabel-configs": relabelConfigs,
		},
	}
}

func createConfigMapBrokenRulesStub(name, namespace string) *corev1.ConfigMap {
	relabelConfigs := `
    - regex: ^prometheus$
      action:labeldrop
    - regex: ^node_exporter$
      action labeldrop
    `

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": name},
		},
		Data: map[string]string{
			"relabel-configs": relabelConfigs,
		},
	}
}

func TestGrafanaStackChangesTriggerNamespaceReconciler(t *testing.T) {
	genEvent := func(s string) *event.GenericEvent {
		return &event.GenericEvent{
			Object: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: s,
				},
			},
		}
	}
	genNamespace := func(name, stackAnnotation string) *corev1.Namespace {
		return &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Annotations: map[string]string{
					Config.stackNameAnnotationKey: stackAnnotation,
				},
			},
		}
	}

	stackName := "mystack"
	namespaceName := stackName + "-dev"
	t.Run("when there are no namespaces, does not trigger reconcile", func(t *testing.T) {
		client := test_helpers.NewFakeClient(t)
		source := grafanaStackChangesSource{
			Client: client,
		}
		event := genEvent(stackName)

		reconcileEvents := source.Map(event.Object)
		require.Len(t, reconcileEvents, 0)
	})
	t.Run("when the namespace is annotated for one stack and it doesn't match, does not trigger reconcile", func(t *testing.T) {
		client := test_helpers.NewFakeClient(t, genNamespace(namespaceName, stackName+"1"))
		source := grafanaStackChangesSource{
			Client: client,
		}
		event := genEvent(stackName)

		reconcileEvents := source.Map(event.Object)
		require.Len(t, reconcileEvents, 0)
	})
	t.Run("when the namespace is annotated for one stack and it matches, triggers reconcile", func(t *testing.T) {
		client := test_helpers.NewFakeClient(t, genNamespace(namespaceName, stackName))
		source := grafanaStackChangesSource{
			Client: client,
		}
		event := genEvent(stackName)

		reconcileEvents := source.Map(event.Object)
		require.Len(t, reconcileEvents, 1)
		assert.Equal(t, namespaceName, reconcileEvents[0].Name)
	})
	t.Run("when the namespace is annotated for multiple stacks and none matches, does not trigger reconcile", func(t *testing.T) {
		client := test_helpers.NewFakeClient(t, genNamespace(namespaceName, stackName+"2,"+stackName+"1"))
		source := grafanaStackChangesSource{
			Client: client,
		}
		event := genEvent(stackName)

		reconcileEvents := source.Map(event.Object)
		require.Len(t, reconcileEvents, 0)
	})
	t.Run("when the namespace is annotated for multiple stacks and one matches, triggers reconcile", func(t *testing.T) {
		client := test_helpers.NewFakeClient(t, genNamespace(namespaceName, stackName+","+stackName+"1"))
		source := grafanaStackChangesSource{
			Client: client,
		}
		event := genEvent(stackName)

		reconcileEvents := source.Map(event.Object)
		require.Len(t, reconcileEvents, 1)
		assert.Equal(t, namespaceName, reconcileEvents[0].Name)
	})
}
