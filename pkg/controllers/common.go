package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	types "k8s.io/apimachinery/pkg/types"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var Config *config = NewConfig("adevinta.com")

type config struct {
	// Destination GrafanaCloud stack selection
	stackNameAnnotationKey string
	// Namespace feature toggles
	metricsLabelKey     string
	logsLabelKey        string
	tracesAnnotationKey string
	// Advanced user settings - Prometheus RemoteWrite
	referencedSecretAnnotationKeys string
	remoteWriteAnnotationKey       string
	// Advanced user settings - Prometheus ingestion metrics rate limit
	podSampleLimitAnnotation string
	// Internal interfaces - PodMonitor
	storageAnnotationKey      string
	alertManagerAnnotationKey string
	accountAnnotationKey      string
	// This is expected at tenant namespace level
	accountLabelKey string
	// Interface with Prometheus Operator?
	podMonitorAnnotationKey string
	podMonitorLabelKey      string
	// Finalizers
	podmonitorFinalizer string
}

func NewConfig(baseDomain string) *config {
	return &config{
		stackNameAnnotationKey:         "grafanacloud." + baseDomain + "/stack-name",
		metricsLabelKey:                "grafanacloud." + baseDomain + "/metrics",
		logsLabelKey:                   "grafanacloud." + baseDomain + "/logs",
		tracesAnnotationKey:            "grafanacloud." + baseDomain + "/traces",
		referencedSecretAnnotationKeys: "monitoring." + baseDomain + "/referenced-secrets",
		remoteWriteAnnotationKey:       "metrics.monitoring." + baseDomain + "/remote-write",
		podSampleLimitAnnotation:       "monitor." + baseDomain + "/pod-sample-limit",
		storageAnnotationKey:           "monitor." + baseDomain + "/storage",
		alertManagerAnnotationKey:      "monitor." + baseDomain + "/alert-manager",
		accountAnnotationKey:           "monitor." + baseDomain + "/account",
		accountLabelKey:                "monitor." + baseDomain + "/account",
		podMonitorAnnotationKey:        baseDomain + "/podmonitor",
		podMonitorLabelKey:             baseDomain + "/podmonitor",
		podmonitorFinalizer:            "finalizer.podmonitor." + baseDomain,
	}
}

func getObjectName(prometheus metav1.Object) string {
	return "prometheus-" + prometheus.GetName()
}

func NewScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	_ = monitoringv1.AddToScheme(scheme)
	_ = vpav1.AddToScheme(scheme)

	return scheme
}

func newPrometheusObjectDef(namespace, prometheusNamespace string) *monitoringv1.Prometheus {
	return &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namespace,
			Namespace: prometheusNamespace,
		},
		Spec: monitoringv1.PrometheusSpec{},
	}
}

func isGrafanaCloudStorageEnabled(objectMeta metav1.ObjectMeta) bool {
	value, ok := getStorageAnnotation(objectMeta.Annotations)
	if ok {
		availableStorages := strings.Split(value, ",")
		return findInAnnotation(availableStorages, "grafanacloud")
	}
	return false
}

func publishPrometheusErrEvent(client ctrlclient.Client, eventName, eventNamespace string, prometheus *monitoringv1.Prometheus, e error) error {
	eventToPublish := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      eventName,
			Namespace: eventNamespace,
		},
		ReportingController: "NamespaceController",
		ReportingInstance:   prometheus.Name,
		InvolvedObject: corev1.ObjectReference{
			Kind:      prometheus.Kind,
			Namespace: prometheus.Namespace,
			Name:      prometheus.Name,
		},
		LastTimestamp: metav1.NewTime(time.Now()),
		Type:          "Warning",
		EventTime:     metav1.NowMicro(),
		Action:        "ignore-relabeling",
		Source: corev1.EventSource{
			Component: "grafana-cloud-operator",
		},
		Reason: "invalid-remote-write",
	}

	_, err := ctrl.CreateOrUpdate(
		context.TODO(),
		client,
		eventToPublish,
		func() error {
			eventToPublish.Message = e.Error()
			eventToPublish.Count = eventToPublish.Count + 1
			eventToPublish.LastTimestamp = metav1.NewTime(time.Now())
			eventToPublish.InvolvedObject = corev1.ObjectReference{
				Kind:      prometheus.Kind,
				Namespace: prometheus.Namespace,
				Name:      prometheus.Name,
			}
			eventToPublish.Type = "Warning"
			if eventToPublish.EventTime.IsZero() {
				eventToPublish.EventTime = metav1.NowMicro()
			}
			eventToPublish.Action = "ignore-relabeling"
			eventToPublish.Source = corev1.EventSource{
				Component: "grafana-cloud-operator",
			}
			eventToPublish.Reason = "invalid-remote-write"
			return nil
		},
	)
	return err
}

func getSecret(client ctrlclient.Client, secret types.NamespacedName) (*corev1.Secret, error) {
	secretObject := corev1.Secret{}

	if err := client.Get(context.Background(), secret, &secretObject); err != nil {
		return nil, err
	}

	return &secretObject, nil
}

func checkNamespaceHasActionableWorkloads(k8sClient ctrlclient.Client, log logr.Logger, namespace string, filters ...podFilter) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}

	ctx := context.Background()
	if err := k8sClient.List(ctx, podList, ctrlclient.InNamespace(namespace)); err != nil {
		log.Error(err, "unable to list pods in tenant namespace")
		return []corev1.Pod{}, err
	}

	out := podList.Items
	for _, f := range filters {
		out = f(out)
	}
	return out, nil
}

type podFilter func(p []corev1.Pod) []corev1.Pod

func filterBySelector(selector labels.Selector) podFilter {
	return func(p []corev1.Pod) []corev1.Pod {
		var filteredPods []corev1.Pod
		for _, pod := range p {
			if !selector.Matches(labels.Set(pod.Labels)) {
				filteredPods = append(filteredPods, pod)
			}
		}
		return filteredPods
	}
}

func excludePodsOnLabel(label, value string) podFilter {
	return func(p []corev1.Pod) []corev1.Pod {
		var filteredPods []corev1.Pod
		for _, pod := range p {
			if !strings.Contains(pod.Labels[label], value) {
				filteredPods = append(filteredPods, pod)
			}
		}
		return filteredPods
	}
}

func lookupGrafanaStacks(k8sclient client.Client, namespace string) ([]string, error) {
	ns := corev1.Namespace{}
	err := k8sclient.Get(context.Background(), client.ObjectKey{Name: namespace}, &ns)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return []string{}, nil
		}
		return []string{}, err
	}

	stack, ok := ns.GetAnnotations()[Config.stackNameAnnotationKey]
	if !ok {
		return []string{}, fmt.Errorf("the annotation %s is not set on the namespace %s", Config.stackNameAnnotationKey, namespace)
	}

	split := strings.Split(stack, ",")

	var trimmed []string
	for _, part := range split {
		trimmed = append(trimmed, strings.TrimSpace(part))
	}

	return trimmed, nil

}
