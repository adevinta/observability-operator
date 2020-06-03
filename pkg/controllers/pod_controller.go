package controllers

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	types "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"k8s.io/apimachinery/pkg/labels"
)

var (
	standardRelabeling = []*monitoringv1.RelabelConfig{
		{
			Action:      "labelmap",
			Regex:       "__meta_kubernetes_pod_label_(.+)",
			Replacement: "${1}",
		},
		{
			// In the legacy prometheus-local-container deployment, the presence of prometheus.io/port label was overriding the port discovery done by default by prometheus.
			// This was done by our configuration instead of prometheus itself.
			// In most cases, not having this configuration is good enoguh. Prometheus will scrape, by default, all defined container ports: https://github.com/prometheus/prometheus/blob/04145344991acd7571c715bdbe1c2c6dfec9f871/discovery/kubernetes/pod.go#L242
			// We though, discovered a case where this is not working. When the port is not registered inside the pod definition. There are occurences of this setup in our clusters.
			// In order to be backward compatible, we introduce the same configuration as in prometheus-local-container and honor the prometheus.io/port annotation contract.
			// In future releases we may want to deprecate this behaviour
			Action: "replace",
			// Prometheus drops the replacement if the value does not match:
			// https://github.com/prometheus/prometheus/blob/04145344991acd7571c715bdbe1c2c6dfec9f871/pkg/relabel/relabel.go#L215
			// so, if the label is absent, empty, or a string, the regexp will not match and the original __address__ will be left intact
			Regex:       `(.+):(?:\d+);(\d+)`,
			Replacement: "${1}:${2}",
			Separator:   ";",
			SourceLabels: []string{
				"__address__",
				"__meta_kubernetes_pod_annotation_prometheus_io_port",
			},
			TargetLabel: "__address__",
		},
	}
)

// PodReconciler reconciles a Pod object
type PodReconciler struct {
	client.Client
	Log                           logr.Logger
	Scheme                        *runtime.Scheme
	ExcludeWorkloadLabelSelector  labels.Selector
	ExcludeNamespaceLabelSelector labels.Selector
	IgnoreApps                    []string
	IgnoreNamespaces              []string
	PrometheusNamespace           string
	TracesNamespace               string
	GrafanaCloudClient            GrafanaCloudClient
	GrafanaCloudTracesToken       string
	ClusterName                   string
	EnableVPA                     bool
}

func getOwner(obj client.Object) client.Object {
	for _, owner := range obj.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			u := &unstructured.Unstructured{}
			u.SetKind(owner.Kind)
			u.SetAPIVersion(owner.APIVersion)
			u.SetName(owner.Name)
			u.SetNamespace(obj.GetNamespace())
			return u
		}
	}
	return obj
}

func isExcludedByLabelSelector(selector labels.Selector, lbs map[string]string) bool {
	if selector != nil && !selector.Empty() {
		if selector.Matches(labels.Set(lbs)) {
			return true
		}
	}
	return false
}

func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod := corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			ns := corev1.Namespace{}
			if err := r.Get(ctx, types.NamespacedName{Name: req.Namespace}, &ns); err != nil {
				r.Log.Error(err, fmt.Sprintf("Namespace %s not found for pod %s", req.Namespace, req.Name))
				return ctrl.Result{}, err
			}
			if ns.Annotations[Config.tracesAnnotationKey] == "enabled" {
				if _, err := reconcileTracesCollector(ctx, r.Client, r.Log, r.GrafanaCloudClient, r.GrafanaCloudTracesToken, r.ClusterName, r.TracesNamespace, req.Namespace, r.IgnoreApps, r.ExcludeWorkloadLabelSelector, r.EnableVPA); err != nil {
					r.Log.Error(err, "Unable to reconcile traces collector")
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	ns := corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: req.Namespace}, &ns); err != nil {
		r.Log.Error(err, fmt.Sprintf("Namespace %s not found for pod %s", req.Namespace, req.Name))
		return ctrl.Result{}, err
	}

	if isExcludedByLabelSelector(r.ExcludeNamespaceLabelSelector, ns.Labels) {
		r.Log.V(10).Info("Namespace is excluded by label selector")
		return ctrl.Result{}, nil
	}

	owner := getOwner(&pod)
	if err := r.Get(ctx, client.ObjectKeyFromObject(owner), owner); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if owner.GetObjectKind().GroupVersionKind().Kind == "ReplicaSet" {
		owner = getOwner(owner)
		if err := r.Get(ctx, client.ObjectKeyFromObject(owner), owner); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	if owner.GetObjectKind().GroupVersionKind().Kind == "Pod" {
		// avoid creating one pod monitor per pod in the namespace.
		// we will review the strategy for those pods in the future.
		// After a quick analysis, we don't have any pod that would cause us any trouble here
		r.Log.Info("pod has no owner, skipping", "namespace", pod.Namespace, "name", pod.Name)
		return ctrl.Result{}, nil
	}

	err := r.reconcilePodMonitor(owner)
	if err != nil {
		return ctrl.Result{}, err
	}

	if ns.Annotations[Config.tracesAnnotationKey] == "enabled" {
		if _, err := reconcileTracesCollector(ctx, r.Client, r.Log, r.GrafanaCloudClient, r.GrafanaCloudTracesToken, r.ClusterName, r.TracesNamespace, req.Namespace, r.IgnoreApps, r.ExcludeWorkloadLabelSelector, r.EnableVPA); err != nil {
			r.Log.Error(err, "Unable to reconcile traces collector")
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *PodReconciler) reconcilePodMonitor(owner client.Object) error {
	monitor := &monitoringv1.PodMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      owner.GetName(),
			Namespace: owner.GetNamespace(),
		},
	}
	gvk := owner.GetObjectKind().GroupVersionKind()
	log := r.Log.WithValues("apiVersion", gvk.GroupVersion().String(), "kind", gvk.Kind, "namespace", owner.GetNamespace(), "name", owner.GetName())

	if r.needsToBeScrapped(owner) {
		r.createOrUpdatePodMonitor(log, owner, monitor)
		return nil
	}

	log.Info("does not need scrapping")
	log = log.WithValues("needsToBeScraped", false)
	if r.monitorExists(monitor) {
		if r.monitorIsManaged(monitor, owner) {
			if err := r.deleteMonitor(monitor); err != nil {
				log.Error(err, "failed to delete no longer needed monitor")
				return err
			}
			log.Info("deleting no longer needed monitor")
			return nil
		}
		log.Info("Monitor is not managed by the controller, skipping")
		return nil
	}
	return nil
}

func (r *PodReconciler) createOrUpdatePodMonitor(log logr.Logger, owner client.Object, monitor *monitoringv1.PodMonitor) {
	storage, err := getAvailableStorageFromNamespace(r.Client, monitor.Namespace, r.Log)
	if err != nil {
		r.Log.Info("Could not determinate the Storage from the Namespace %s, skipping PodMonitor sync", monitor.Namespace)
		return
	}
	sampleLimit := getPodMonitorSampleLimit(r.Client, owner, r.Log)
	ctx := context.Background()
	result, err := ctrl.CreateOrUpdate(
		ctx,
		r.Client,
		monitor,
		func() error {
			monitor.ObjectMeta.Labels = getLabels(owner, monitor)
			monitor.ObjectMeta.Annotations = map[string]string{
				Config.storageAnnotationKey:    storage,
				Config.accountAnnotationKey:    monitor.Namespace,
				Config.podMonitorAnnotationKey: owner.GetNamespace() + "-" + owner.GetName(),
			}
			err := controllerutil.SetOwnerReference(owner, monitor, r.Scheme)
			if err != nil {
				log.Error(err, "failed to assign owner reference to deployment")
			}

			u := &unstructured.Unstructured{}
			switch o := owner.(type) {
			case *unstructured.Unstructured:
				u = o
			default:
				u.Object, err = runtime.DefaultUnstructuredConverter.ToUnstructured(owner)
				if err != nil {
					return err
				}
			}
			unstructuredSelector, ok, err := unstructured.NestedMap(u.Object, "spec", "selector")
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("could not find spec.selector in pod monitor owner")
			}
			selector := metav1.LabelSelector{}
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredSelector, &selector)
			if err != nil {
				return err
			}

			monitor.Spec = monitoringv1.PodMonitorSpec{
				Selector:    selector,
				SampleLimit: uint64(sampleLimit),
				PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{
					{
						Path:           getPath(owner),
						Params:         getParams(owner),
						HonorLabels:    true,
						RelabelConfigs: standardRelabeling,
					},
				},
				NamespaceSelector: monitoringv1.NamespaceSelector{
					MatchNames: []string{owner.GetNamespace()},
				},
			}
			return updatePodMonitorAnnotation(ctx, r.Client, monitor)
		},
	)
	if err != nil {
		log.Error(err, "failed to create or update pod monitor")
	} else {
		log.WithValues("result", result).Info("created pod monitor")
	}
}
func (r *PodReconciler) monitorIsManaged(monitor *monitoringv1.PodMonitor, owner client.Object) bool {
	ctx := context.Background()
	m := monitoringv1.PodMonitor{}
	_ = r.Get(ctx, types.NamespacedName{Namespace: monitor.Namespace, Name: monitor.Name}, &m)
	gvk, err := apiutil.GVKForObject(owner, r.Scheme)
	if err != nil {
		// TODO handle the error (log? return an actual error?)
		return false
	}
	for _, ownerReferenced := range m.GetOwnerReferences() {
		if ownerReferenced.Kind == gvk.Kind && ownerReferenced.Name == owner.GetName() && ownerReferenced.UID == owner.GetUID() {
			return true
		}
	}
	return false
}
func (r *PodReconciler) deleteMonitor(monitor *monitoringv1.PodMonitor) error {
	ctx := context.Background()
	return r.Delete(ctx, monitor)
}

func (r *PodReconciler) monitorExists(monitor *monitoringv1.PodMonitor) bool {
	ctx := context.Background()
	m := monitoringv1.PodMonitor{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: monitor.Namespace, Name: monitor.Name}, &m); err != nil {
		return false
	}
	return true
}

func (r *PodReconciler) needsToBeScrapped(object client.Object) bool {
	if slices.Contains(r.IgnoreNamespaces, object.GetNamespace()) {
		return false
	}

	for _, ignoredApp := range r.IgnoreApps {
		if object.GetLabels()["app"] == ignoredApp {
			return false
		}
	}

	if isExcludedByLabelSelector(r.ExcludeWorkloadLabelSelector, object.GetLabels()) {
		return false
	}

	scrape, ok := getPodAnnotations(object)["prometheus.io/scrape"].(string)
	return ok && scrape == "true"
}

func getPath(object client.Object) string {
	pathIntf, ok := getPodAnnotations(object)["prometheus.io/path"]
	if ok {
		if path, ok := pathIntf.(string); ok {
			return path
		}
	}
	return "/metrics"
}

// getParams will extract parameters from prometheus.io/param_* annotations
func getParams(object client.Object) map[string][]string {
	params := map[string][]string{}
	for annotation, valueIntf := range getPodAnnotations(object) {
		if strings.HasPrefix(annotation, "prometheus.io/param_") {
			param := strings.TrimPrefix(annotation, "prometheus.io/param_")
			if value, ok := valueIntf.(string); ok {
				// k8s doesn't support duplicate keys, so only one value is supported
				params[param] = []string{value}
			}
		}
	}
	return params
}

func getPodAnnotations(object client.Object) map[string]interface{} {
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		return map[string]interface{}{}
	}
	path := []string{"spec", "template", "metadata", "annotations"}
	switch o := object.(type) {
	case *corev1.Pod:
		path = []string{"metadata", "annotations"}
	case *unstructured.Unstructured:
		if o.GetKind() == "Pod" {
			path = []string{"metadata", "annotations"}
		}
	}
	annotations, _, _ := unstructured.NestedMap(u, path...)
	if annotations == nil {
		return map[string]interface{}{}
	}
	return annotations
}

func getLabels(owner client.Object, monitor *monitoringv1.PodMonitor) map[string]string {
	labels := map[string]string{
		Config.accountLabelKey: monitor.Namespace,
	}
	for k, v := range owner.GetLabels() {
		labels[k] = v
	}
	return labels
}

func getPodMonitorSampleLimit(k8sclient client.Client, object client.Object, log logr.Logger) int {
	value, ok := getPodAnnotations(object)[Config.podSampleLimitAnnotation]
	if !ok {
		return 4500
	}
	sampleLimitInt, err := strconv.Atoi(value.(string))
	if err != nil {
		log.Error(err, "Could not convert value in "+Config.podSampleLimitAnnotation+" annotation to integer, setting it to default 4500")
		return 4500
	}
	return sampleLimitInt

}

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}
