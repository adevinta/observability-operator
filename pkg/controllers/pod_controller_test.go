package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/adevinta/observability-operator/pkg/grafanacloud"
	"github.com/adevinta/observability-operator/pkg/test_helpers"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/relabel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	appv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apilabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	validatingfield "k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func assertLabelValue(t *testing.T, name, value string, labelset labels.Labels) {
	t.Helper()
	for _, label := range labelset {
		if label.Name == name {
			assert.Equal(t, value, label.Value)
			return
		}
	}
	t.Errorf("did not find label with name %s", name)
}

func TestRelabeling(t *testing.T) {
	b, err := json.Marshal(standardRelabeling)
	require.NoError(t, err)
	// hack to translate from very similar prometheus-operator format to prometheus-config one
	b = bytes.Replace(b, []byte(`"sourceLabels"`), []byte(`"source_labels"`), -1)
	b = bytes.Replace(b, []byte(`"targetLabel"`), []byte(`"target_label"`), -1)
	cfg := []*relabel.Config{}
	require.NoError(t, yaml.NewDecoder(bytes.NewReader(b)).Decode(&cfg))

	assertLabelValue(t, "__address__", "127.0.0.1:6400", relabel.Process(labels.Labels{{Name: "__address__", Value: "127.0.0.1:6400"}}, cfg...))
	assertLabelValue(t, "__address__", "127.0.0.1:2021", relabel.Process(labels.Labels{{Name: "__address__", Value: "127.0.0.1:6400"}, {Name: "__meta_kubernetes_pod_annotation_prometheus_io_port", Value: "2021"}}, cfg...))
	assertLabelValue(t, "__address__", "127.0.0.1:6400", relabel.Process(labels.Labels{{Name: "__address__", Value: "127.0.0.1:6400"}, {Name: "__meta_kubernetes_pod_annotation_prometheus_io_port", Value: "some-port"}}, cfg...))
	assertLabelValue(t, "__address__", "127.0.0.1:6400", relabel.Process(labels.Labels{{Name: "__address__", Value: "127.0.0.1:6400"}, {Name: "__meta_kubernetes_pod_annotation_prometheus_io_port", Value: ""}}, cfg...))
	assertLabelValue(t, "some_label", "value", relabel.Process(labels.Labels{{Name: "__meta_kubernetes_pod_label_some_label", Value: "value"}}, cfg...))
}

func TestPodReconcilerRetrievesOwner(t *testing.T) {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bar",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-pod",
			Namespace: "bar",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "foo-v1",
					Controller: ptr.To(true),
				},
			},
		},
	}

	replicaSet := &appv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-v1",
			Namespace: "bar",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "foo",
					Controller: ptr.To(true),
				},
			},
		},
	}

	deploy := &appv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Spec: appv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"prometheus.io/scrape": "true",
					},
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"random_label": "true"},
			},
		},
	}
	t.Run("When the pod is owned by a deployment", func(t *testing.T) {
		reconciler := newDefaultPodReconciler(t, createSecretStub("prometheusesNamespace"), namespace, pod, replicaSet, deploy)

		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "foo-pod", Namespace: "bar"}})
		require.NoError(t, err)

		podMonitors := &monitoringv1.PodMonitorList{}
		require.NoError(t, reconciler.Client.List(context.Background(), podMonitors))
		require.Len(t, podMonitors.Items, 1)
		assert.Equal(t, "foo", podMonitors.Items[0].Name)
		assert.EqualValues(
			t,
			metav1.LabelSelector{
				MatchLabels: map[string]string{"random_label": "true"},
			},
			podMonitors.Items[0].Spec.Selector,
		)
	})
	t.Run("When the pod is owned by a statefulset", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo-pod",
				Namespace: "bar",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "StatefulSet",
						Name:       "foo",
						Controller: ptr.To(true),
					},
				},
			},
		}

		statefulSet := &appv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo",
				Namespace: "bar",
			},
			Spec: appv1.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"prometheus.io/scrape": "true",
						},
					},
				},
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"random_label": "true"},
				},
			},
		}

		reconciler := newDefaultPodReconciler(t, createSecretStub("prometheusesNamespace"), namespace, pod, statefulSet)

		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "foo-pod", Namespace: "bar"}})
		require.NoError(t, err)

		podMonitors := &monitoringv1.PodMonitorList{}
		require.NoError(t, reconciler.Client.List(context.Background(), podMonitors))
		require.Len(t, podMonitors.Items, 1)
		assert.Equal(t, "foo", podMonitors.Items[0].Name)
		assert.EqualValues(
			t,
			metav1.LabelSelector{
				MatchLabels: map[string]string{"random_label": "true"},
			},
			podMonitors.Items[0].Spec.Selector,
		)
	})
	t.Run("When the pod is owned by a non-existing statefulset", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo-pod",
				Namespace: "bar",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "StatefulSet",
						Name:       "foo",
						Controller: ptr.To(true),
					},
				},
			},
		}
		statefulSet := &appv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "other",
				Namespace: "bar",
			},
			Spec: appv1.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"prometheus.io/scrape": "true",
						},
					},
				},
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"random_label": "true"},
				},
			},
		}

		reconciler := newDefaultPodReconciler(t, createSecretStub("prometheusesNamespace"), namespace, pod, statefulSet)

		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "foo-pod", Namespace: "bar"}})
		require.NoError(t, err)

		podMonitors := &monitoringv1.PodMonitorList{}
		require.NoError(t, reconciler.Client.List(context.Background(), podMonitors))
		require.Len(t, podMonitors.Items, 0)
	})
	t.Run("When the pod is owned by a non-existing deployment", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo-pod",
				Namespace: "bar",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "ReplicaSet",
						Name:       "foo-v1",
						Controller: ptr.To(true),
					},
				},
			},
		}
		replicaSet := &appv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo-v1",
				Namespace: "bar",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "foo",
						Controller: ptr.To(true),
					},
				},
			},
		}
		reconciler := newDefaultPodReconciler(t, createSecretStub("prometheusesNamespace"), namespace, pod, replicaSet)

		podMonitors := &monitoringv1.PodMonitorList{}
		require.NoError(t, reconciler.Client.List(context.Background(), podMonitors))
		require.Len(t, podMonitors.Items, 0)
	})

	t.Run("When the pod is has no owner", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo-pod",
				Namespace: "bar",
				Annotations: map[string]string{
					"prometheus.io/scrape": "true",
				},
			},
		}
		replicaSet := &appv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo-v1",
				Namespace: "bar",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "foo",
						Controller: ptr.To(true),
					},
				},
			},
		}
		reconciler := newDefaultPodReconciler(t, createSecretStub("prometheusesNamespace"), namespace, pod, replicaSet)
		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "foo-pod", Namespace: "bar"}})
		require.NoError(t, err)

		podMonitors := &monitoringv1.PodMonitorList{}
		require.NoError(t, reconciler.Client.List(context.Background(), podMonitors))
		require.Len(t, podMonitors.Items, 0)
	})
}

func TestDeploymentCreatesPodMonitorWithNamespaceStorageAnnotations(t *testing.T) {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bar",
		},
	}

	t.Run("when the namespace is not annotated", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo-pod",
				Namespace: "bar",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "ReplicaSet",
						Name:       "foo-v1",
						Controller: ptr.To(true),
					},
				},
			},
		}
		replicaSet := &appv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo-v1",
				Namespace: "bar",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "foo",
						Controller: ptr.To(true),
					},
				},
			},
		}
		deploy := &appv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo",
				Namespace: "bar",
			},
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"prometheus.io/scrape": "true",
						},
					},
				},
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"random_label": "true"},
				},
			},
		}
		reconciler := newDefaultPodReconciler(t, createSecretStub("prometheusesNamespace"), namespace, pod, replicaSet, deploy)

		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "bar", Name: "foo-pod"}})
		assert.Nil(t, err)
		assert.NotNil(t, result)

		podMonitors := &monitoringv1.PodMonitorList{}
		require.NoError(t, reconciler.Client.List(context.Background(), podMonitors))
		require.Len(t, podMonitors.Items, 1)
		assert.Equal(t, "grafanacloud", podMonitors.Items[0].Annotations[Config.storageAnnotationKey])
	})

	t.Run("when the namespace is annotated", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo-pod",
				Namespace: "bar",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "ReplicaSet",
						Name:       "foo-v1",
						Controller: ptr.To(true),
					},
				},
			},
		}
		replicaSet := &appv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo-v1",
				Namespace: "bar",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "foo",
						Controller: ptr.To(true),
					},
				},
			},
		}
		deploy := &appv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo",
				Namespace: "bar",
			},
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"prometheus.io/scrape": "true",
						},
					},
				},
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"random_label": "true"},
				},
			},
		}
		reconciler := newDefaultPodReconciler(t, createSecretStub("prometheusesNamespace"), namespace, pod, replicaSet, deploy)
		namespace.Annotations = map[string]string{
			Config.storageAnnotationKey: "new-storage",
		}
		require.NoError(t, reconciler.Client.Update(context.Background(), namespace))
		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "bar", Name: "foo-pod"}})
		assert.Nil(t, err)
		assert.NotNil(t, result)

		podMonitors := &monitoringv1.PodMonitorList{}
		require.NoError(t, reconciler.Client.List(context.Background(), podMonitors))
		require.Len(t, podMonitors.Items, 1)
		assert.Equal(t, "new-storage", podMonitors.Items[0].Annotations[Config.storageAnnotationKey])
	})
}

func generatePodObjectsWithLabels(namespaceName string, prometheusScrape string, labels map[string]string) []runtime.Object {
	var annotations map[string]string

	namespace := newNamespace(namespaceName)

	if prometheusScrape != "" {
		annotations = map[string]string{
			"prometheus.io/scrape": prometheusScrape,
		}
	}

	deployment := &appv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deploymentName",
			Namespace: namespace.Name,
			Labels:    labels,
		},
		Spec: appv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: annotations,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"random_label": "true"},
			},
		},
	}

	replicaSet := &appv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-v1",
			Namespace: namespace.Name,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       deployment.Name,
					Controller: ptr.To(true),
				},
			},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "foo-pod",
			Namespace:   namespace.Name,
			Annotations: annotations,
			Labels:      labels,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       replicaSet.Name,
					Controller: ptr.To(true),
				},
			},
		},
	}

	return []runtime.Object{namespace, deployment, replicaSet, pod}

}

func generatePodObjects(namespaceName string, prometheusScrape string) []runtime.Object {
	return generatePodObjectsWithLabels(namespaceName, prometheusScrape, map[string]string{})
}

func TestPodMonitorReconcile(t *testing.T) {
	t.Run("when there is a Pod, a PodMonitor is created", func(t *testing.T) {
		objects := generatePodObjects("my-namespace", "true")
		reconciler := newDefaultPodReconciler(t, objects...)

		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "my-namespace", Name: "foo-pod"}})
		require.NoError(t, err)

		podMonitors := &monitoringv1.PodMonitorList{}
		err = reconciler.Client.List(context.Background(), podMonitors)
		require.NoError(t, err)
		assert.Len(t, podMonitors.Items, 1)
	})
	t.Run("when the scrape annotation is false, there are no PodMonitors created", func(t *testing.T) {
		objects := generatePodObjects("my-namespace", "false")
		reconciler := newDefaultPodReconciler(t, objects...)

		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "my-namespace", Name: "foo-pod"}})
		require.NoError(t, err)

		podMonitors := &monitoringv1.PodMonitorList{}
		err = reconciler.Client.List(context.Background(), podMonitors)
		require.NoError(t, err)
		assert.Len(t, podMonitors.Items, 0)

	})
	t.Run("when there is no scrape annotation, there are no PodMonitors created", func(t *testing.T) {
		objects := generatePodObjects("my-namespace", "")
		reconciler := newDefaultPodReconciler(t, objects...)

		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "my-namespace", Name: "foo-pod"}})
		require.NoError(t, err)

		podMonitors := &monitoringv1.PodMonitorList{}
		err = reconciler.Client.List(context.Background(), podMonitors)
		require.NoError(t, err)
		assert.Len(t, podMonitors.Items, 0)

	})
	t.Run("when the namespace is ignored, there are no PodMonitors created", func(t *testing.T) {
		objects := generatePodObjects("my-namespace", "true")
		reconciler := newDefaultPodReconciler(t, objects...)
		reconciler.IgnoreNamespaces = []string{"my-namespace"}

		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "my-namespace", Name: "foo-pod"}})
		require.NoError(t, err)

		podMonitors := &monitoringv1.PodMonitorList{}
		err = reconciler.Client.List(context.Background(), podMonitors)
		require.NoError(t, err)
		assert.Len(t, podMonitors.Items, 0)
	})

	t.Run("when the labels are ignored, there are no PodMonitors created", func(t *testing.T) {
		objects := generatePodObjectsWithLabels("my-namespace", "true", map[string]string{"appLabel": "skip"})
		reconciler := newDefaultPodReconciler(t, objects...)
		path := validatingfield.NewPath("metadata", "labels")
		filter, err := apilabels.Parse("appLabel=skip", validatingfield.WithPath(path))
		require.NoError(t, err)
		reconciler.ExcludeWorkloadLabelSelector = filter

		_, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "my-namespace", Name: "foo-pod"}})
		require.NoError(t, err)

		podMonitors := &monitoringv1.PodMonitorList{}
		err = reconciler.Client.List(context.Background(), podMonitors)
		require.NoError(t, err)
		assert.Len(t, podMonitors.Items, 0)
	})

}

func TestDeployment(t *testing.T) {
	deployment := &appv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			UID:       "123",
		},
		Spec: appv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"random_label": "true"},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-pod",
			Namespace: "bar",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       deployment.Name,

					Controller: ptr.To(true),
				},
			},
		},
	}
	podMonitor := &monitoringv1.PodMonitor{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "foo",
				UID:        "123",
			}},
			Labels: map[string]string{
				Config.podMonitorLabelKey: "bar-foo",
				Config.accountLabelKey:    "bar",
			},
			Annotations: map[string]string{
				Config.storageAnnotationKey: "grafanacloud",
				Config.accountAnnotationKey: "bar",
			},
		},
		Spec: monitoringv1.PodMonitorSpec{
			PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{
				{
					Path:           "/metrics",
					HonorLabels:    true,
					RelabelConfigs: standardRelabeling,
				},
			},
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"random_label": "true",
				},
			},
			NamespaceSelector: monitoringv1.NamespaceSelector{MatchNames: []string{"bar"}},
			SampleLimit:       9000,
		},
	}
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bar",
		},
	}
	t.Run("when the annotation for scrape is not properly set, the podmonitor instance is not created", func(t *testing.T) {
		reconciler := newDefaultPodReconciler(t, pod, namespace)
		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "bar", Name: "foo"}})
		require.NoError(t, err)
		assert.NotNil(t, result)
		podMonitors := &monitoringv1.PodMonitorList{}
		require.NoError(t, reconciler.Client.List(context.Background(), podMonitors))
		assert.Len(t, podMonitors.Items, 0)
	})

	t.Run("once the annotation for scrape is set, the podmonitor instance is created", func(t *testing.T) {
		reconciler := newDefaultPodReconciler(t, pod, namespace, podMonitor)

		deployment.Spec.Template.ObjectMeta.Annotations = map[string]string{
			"prometheus.io/scrape":          "true",
			Config.podSampleLimitAnnotation: "9000",
		}
		err := reconciler.Client.Create(context.Background(), deployment)
		require.NoError(t, err)
		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "bar", Name: "foo-pod"}})
		require.NoError(t, err)
		assert.NotNil(t, result)

		podMonitors := &monitoringv1.PodMonitorList{}
		require.NoError(t, reconciler.Client.List(context.Background(), podMonitors))
		require.Len(t, podMonitors.Items, 1)
		assert.Equal(t, deployment.Name, podMonitors.Items[0].Name)
		assert.Equal(t, deployment.Namespace, podMonitors.Items[0].Namespace)
		assert.Equal(t, *deployment.Spec.Selector, podMonitors.Items[0].Spec.Selector)
		assert.Len(t, podMonitors.Items[0].OwnerReferences, 1)
		assert.Equal(t, podMonitors.Items[0].GetOwnerReferences()[0].APIVersion, "apps/v1")
		assert.Equal(t, podMonitors.Items[0].GetOwnerReferences()[0].Kind, "Deployment")
		assert.Equal(t, podMonitors.Items[0].GetOwnerReferences()[0].Name, "foo")
		require.Len(t, podMonitors.Items[0].Spec.PodMetricsEndpoints, 1)
		assert.Equal(t, "/metrics", podMonitors.Items[0].Spec.PodMetricsEndpoints[0].Path)
		assert.True(t, podMonitors.Items[0].Spec.PodMetricsEndpoints[0].HonorLabels)
		assert.Equal(t, podMonitors.Items[0].Spec.SampleLimit, uint64(9000))
	})

	t.Run("that if the annotation is changed to not match, the podmonitor is removed", func(t *testing.T) {
		reconciler := newDefaultPodReconciler(t, pod, namespace, podMonitor, deployment)

		deployment.Spec.Template.ObjectMeta.Annotations = map[string]string{
			"prometheus.io/scrape": "false",
		}
		require.NoError(t, reconciler.Client.Update(context.Background(), deployment))
		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "bar", Name: "foo-pod"}})
		assert.Nil(t, err)
		assert.NotNil(t, result)
		podMonitors := &monitoringv1.PodMonitorList{}
		require.NoError(t, reconciler.Client.List(context.Background(), podMonitors))
		assert.Len(t, podMonitors.Items, 0)
	})
}

func runGetPathTest(t *testing.T, expected string, object client.Object) {
	t.Helper()
	assert.Equal(t, expected, getPath(object))
}

func TestGetPath(t *testing.T) {
	t.Run("with a Deployment and custom path", func(t *testing.T) {
		runGetPathTest(t, "/mypath", &appv1.Deployment{
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"prometheus.io/path": "/mypath",
						},
					},
				},
			},
		})
	})
	t.Run("with a Deployment and other annotation", func(t *testing.T) {
		runGetPathTest(t, "/metrics", &appv1.Deployment{
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"other": "annotation",
						},
					},
				},
			},
		})
	})
	t.Run("with a Deployment with no annotation", func(t *testing.T) {
		runGetPathTest(t, "/metrics", &appv1.Deployment{
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{},
				},
			},
		})
	})
	t.Run("with a DaemonSet and custom path", func(t *testing.T) {
		runGetPathTest(t, "/mypath", &appv1.DaemonSet{
			Spec: appv1.DaemonSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"prometheus.io/path": "/mypath",
						},
					},
				},
			},
		})
	})
	t.Run("with a DaemonSet and other annotation", func(t *testing.T) {
		runGetPathTest(t, "/metrics", &appv1.DaemonSet{
			Spec: appv1.DaemonSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"other": "annotation",
						},
					},
				},
			},
		})
	})
	t.Run("with a DaemonSet with no annotation", func(t *testing.T) {
		runGetPathTest(t, "/metrics", &appv1.DaemonSet{
			Spec: appv1.DaemonSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{},
				},
			},
		})
	})
	t.Run("with a Pod and custom path", func(t *testing.T) {
		runGetPathTest(t, "/mypath", &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"prometheus.io/path": "/mypath",
				},
			},
		})
	})
	t.Run("with a Pod and other annotation", func(t *testing.T) {
		runGetPathTest(t, "/metrics", &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"other": "annotation",
				},
			},
		})
	})
	t.Run("with a Pod with no annotation", func(t *testing.T) {
		runGetPathTest(t, "/metrics", &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{},
		})
	})

	t.Run("with an unstructured StatefulSet and custom path", func(t *testing.T) {
		runGetPathTest(t, "/mypath", &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind": "StatefulSet",
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"metadata": map[string]interface{}{
							"annotations": map[string]interface{}{
								"prometheus.io/path": "/mypath",
							},
						},
					},
				},
			},
		})
	})
	t.Run("with an unstructured StatefulSet and other annotation", func(t *testing.T) {
		runGetPathTest(t, "/metrics", &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind": "StatefulSet",
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"metadata": map[string]interface{}{
							"annotations": map[string]interface{}{
								"other": "annotation",
							},
						},
					},
				},
			},
		})
	})
	t.Run("with an unstructured StatefulSet with no annotation", func(t *testing.T) {
		runGetPathTest(t, "/metrics", &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind": "StatefulSet",
			},
		})
	})

	t.Run("with an unstructured Pod and custom path", func(t *testing.T) {
		runGetPathTest(t, "/mypath", &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind": "Pod",
				"metadata": map[string]interface{}{
					"annotations": map[string]interface{}{
						"prometheus.io/path": "/mypath",
					},
				},
			},
		})
	})
	t.Run("with an unstructured Pod and other annotation", func(t *testing.T) {
		runGetPathTest(t, "/metrics", &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind": "Pod",
				"metadata": map[string]interface{}{
					"annotations": map[string]interface{}{
						"other": "annotation",
					},
				},
			},
		})
	})
	t.Run("with an unstructured Pod with no annotation", func(t *testing.T) {
		runGetPathTest(t, "/metrics", &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind": "Pod",
			},
		})
	})
}

func runGetParamsTest(t *testing.T, expected map[string][]string, object client.Object) {
	t.Helper()
	assert.Equal(t, expected, getParams(object))
}

func TestGetParams(t *testing.T) {
	// only testing deployments here, as other objects' parsing is already tested in TestGetPath
	t.Run("with a Deployment and custom parameter", func(t *testing.T) {
		runGetParamsTest(t, map[string][]string{"format": {"prometheus"}}, &appv1.Deployment{
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"prometheus.io/param_format": "prometheus",
						},
					},
				},
			},
		})
	})
	t.Run("with a Deployment and two custom parameters", func(t *testing.T) {
		runGetParamsTest(t, map[string][]string{"format": {"prometheus"}, "foo": {"bar"}}, &appv1.Deployment{
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"prometheus.io/param_format": "prometheus",
							"prometheus.io/param_foo":    "bar",
						},
					},
				},
			},
		})
	})
	t.Run("with a Deployment and other annotation", func(t *testing.T) {
		runGetParamsTest(t, map[string][]string{}, &appv1.Deployment{
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"other": "annotation",
						},
					},
				},
			},
		})
	})
	t.Run("with a Deployment with no annotation", func(t *testing.T) {
		runGetParamsTest(t, map[string][]string{}, &appv1.Deployment{
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{},
				},
			},
		})
	})
}

func TestGetLabels(t *testing.T) {
	deployment := appv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deployName",
			Namespace: "deployNamespace",
			Labels: map[string]string{
				"foo": "bar",
			},
		},
	}
	podmonitor := &monitoringv1.PodMonitor{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "monitorName",
			Namespace: "monitorNamespace",
		},
	}
	assert.EqualValues(t, map[string]string{
		Config.accountLabelKey: "monitorNamespace",
		"foo":                  "bar",
	}, getLabels(&deployment, podmonitor))
}

func runGetPodMonitorsSampleLimitTest(t *testing.T, expected int, object client.Object) {
	reconciler := newDefaultTestPodMonitorReconciler(t)
	t.Helper()
	val := getPodMonitorSampleLimit(reconciler.Client, object, reconciler.Log)
	assert.Equal(t, expected, val)
}

func TestGetPodMonitorSampleLimit(t *testing.T) {
	// only testing deployments here, as other objects' parsing is already tested in TestGetPath
	t.Run("with a Deployment and custom parameter", func(t *testing.T) {
		runGetPodMonitorsSampleLimitTest(t, 9000, &appv1.Deployment{
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							Config.podSampleLimitAnnotation: "9000",
						},
					},
				},
			},
		})
	})
	t.Run("with a Deployment and empty annotation", func(t *testing.T) {
		runGetPodMonitorsSampleLimitTest(t, 4500, &appv1.Deployment{
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							Config.podSampleLimitAnnotation: "",
						},
					},
				},
			},
		})
	})
	t.Run("with a Deployment with no annotation", func(t *testing.T) {
		runGetPodMonitorsSampleLimitTest(t, 4500, &appv1.Deployment{
			Spec: appv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{},
				},
			},
		})
	})
	t.Run("with a Pod with the annotation", func(t *testing.T) {
		runGetPodMonitorsSampleLimitTest(t, 9000, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					Config.podSampleLimitAnnotation: "9000",
				},
			},
		})
	})
}

func newDefaultPodReconciler(t *testing.T, initialObjects ...runtime.Object) *PodReconciler {
	scheme := runtime.NewScheme()
	_ = monitoringv1.AddToScheme(scheme)
	_ = appv1.AddToScheme(scheme)

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

	return &PodReconciler{
		Log:                     ctrl.Log.WithName("controllers").WithName("Namespace"),
		Client:                  fakeClient,
		Scheme:                  scheme,
		ClusterName:             "clustername",
		TracesNamespace:         "observability",
		GrafanaCloudClient:      &grafanaCloudClient,
		GrafanaCloudTracesToken: "GCO_TRACES_TOKEN",
	}
}

func TestReconcileTracesCollector(t *testing.T) {
	tracesNs := newNamespace("observability")
	tenantNs := newNamespace("teamtest-dev")

	tenantNs.Annotations = map[string]string{
		Config.stackNameAnnotationKey: "adevintaobs",
		Config.tracesAnnotationKey:    "enabled",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-pod",
			Namespace: tenantNs.Name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "foo-v1",
					Controller: ptr.To(true),
				},
			},
		},
	}
	replicaSet := &appv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-v1",
			Namespace: tenantNs.Name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "foo",
					Controller: ptr.To(true),
				},
			},
		},
	}
	deploy := &appv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: tenantNs.Name,
		},
		Spec: appv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"prometheus.io/scrape": "true",
					},
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"random_label": "true"},
			},
		},
	}

	verifyReconcile := func(reconciler *PodReconciler, namespace string, length int) {
		secrets := &corev1.SecretList{}
		err := reconciler.Client.List(context.Background(), secrets, client.InNamespace(namespace))
		require.NoError(t, err)
		require.Len(t, secrets.Items, length)

		services := &corev1.ServiceList{}
		err = reconciler.Client.List(context.Background(), services, client.InNamespace(namespace))
		require.NoError(t, err)
		require.Len(t, services.Items, length)

		configMaps := &corev1.ConfigMapList{}
		err = reconciler.Client.List(context.Background(), configMaps, client.InNamespace(namespace))
		require.NoError(t, err)
		require.Len(t, configMaps.Items, length)

		networkPolicies := &networkingv1.NetworkPolicyList{}
		err = reconciler.Client.List(context.Background(), networkPolicies, client.InNamespace(namespace))
		require.NoError(t, err)
		require.Len(t, networkPolicies.Items, length)
	}

	t.Run("there is a collector when a pod is present in the tenant namespace", func(t *testing.T) {
		reconciler := newDefaultPodReconciler(t, tenantNs, tracesNs, pod, replicaSet, deploy)

		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: tenantNs.Name, Name: "foo-pod"}})
		require.NoError(t, err)

		verifyReconcile(reconciler, tracesNs.Name, 1)
	})
	t.Run("there is no collector when no pod is present anymore in the tenant namespace", func(t *testing.T) {
		reconciler := newDefaultPodReconciler(t, tenantNs, tracesNs, pod, replicaSet, deploy)

		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: tenantNs.Name, Name: "foo-pod"}})
		require.NoError(t, err)

		verifyReconcile(reconciler, tracesNs.Name, 1)

		err = reconciler.Client.Delete(context.Background(), pod)
		require.NoError(t, err)
		err = reconciler.Client.Delete(context.Background(), replicaSet)
		require.NoError(t, err)
		err = reconciler.Client.Delete(context.Background(), deploy)
		require.NoError(t, err)

		_, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: tenantNs.Name, Name: "foo-pod"}})
		require.NoError(t, err)

		verifyReconcile(reconciler, tracesNs.Name, 0)
	})
}
