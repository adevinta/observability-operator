package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type GrafanaCloudUpdater interface {
	InjectFluentdLokiConfiguration(context.Context) error
}

type NamespaceReconciler struct {
	client.Client
	Log logr.Logger

	ExcludeWorkloadLabelSelector  labels.Selector
	ExcludeNamespaceLabelSelector labels.Selector
	IgnoreApps                    []string
	IgnoreNamespaces              []string
	TracesNamespace               string
	GrafanaCloudUpdater           GrafanaCloudUpdater
	GrafanaCloudClient            GrafanaCloudClient
	GrafanaCloudTracesToken       string
	ClusterName                   string
	EnableVPA                     bool
}

func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("namespace", req.Name)
	if r.isIgnoredNamespace(req.Name) {
		log.Info("Namespace is in the ignore list")
		return ctrl.Result{}, nil
	}
	ns := corev1.Namespace{}
	if err := r.Get(ctx, req.NamespacedName, &ns); err != nil {
		log.Error(err, fmt.Sprintf("Namespace not found for %s ", req.Name))
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if isExcludedByLabelSelector(r.ExcludeNamespaceLabelSelector, ns.Labels) {
		return ctrl.Result{}, nil
	}
	if value, ok := ns.Labels[Config.logsLabelKey]; !ok || value != "disabled" {
		err := r.GrafanaCloudUpdater.InjectFluentdLokiConfiguration(ctx)
		if err != nil {
			return ctrl.Result{Requeue: true, RequeueAfter: 30 * time.Second}, err
		}
	}

	if ns.Annotations[Config.tracesAnnotationKey] == "enabled" {
		if _, err := reconcileTracesCollector(ctx, r.Client, log, r.GrafanaCloudClient, r.GrafanaCloudTracesToken, r.ClusterName, r.TracesNamespace, req.Name, r.IgnoreApps, r.ExcludeWorkloadLabelSelector, r.EnableVPA); err != nil {
			log.Error(err, "Unable to reconcile traces collector")
			return ctrl.Result{}, err
		}
	} else {
		if _, err := r.deleteTracesCollector(ctx, req); err != nil {
			log.Error(err, "Unable to delete traces collector")
			return ctrl.Result{}, err
		}
	}

	if value, ok := ns.Labels[Config.metricsLabelKey]; !ok || value != "disabled" {
		log.Info("Reconciling pod monitors")
		return r.reconcilePodMonitors(ctx, req)
	}
	return ctrl.Result{}, nil
}

func (r *NamespaceReconciler) reconcilePodMonitors(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("namespace", req.Name)
	ns := corev1.Namespace{}
	if err := r.Get(ctx, req.NamespacedName, &ns); err != nil {
		log.Info("Namespace object not found. Skipping...")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	podMonitors := &monitoringv1.PodMonitorList{}
	if err := r.Client.List(ctx, podMonitors, client.InNamespace(req.Name)); err != nil {
		log.Error(err, "Unable to list pod monitors")
		return ctrl.Result{}, err
	}
	log.Info("listing podmonitors in the namespace")
	for _, podMonitor := range podMonitors.Items {
		err := r.reconcileNamespace(ctx, log, ns, podMonitor)
		if err != nil {
			log.Error(err, "Unable to update PodMonitor object", "PodMonitor", podMonitor.Name, "Namespace", podMonitor.Namespace)
			return ctrl.Result{}, err
		} else {
			log.WithValues("podMonitorNamespace", podMonitor.Namespace, "podMonitorName", podMonitor.Name).Info("reconciled pod monitor")
		}
	}

	return ctrl.Result{}, nil
}

func (r *NamespaceReconciler) isIgnoredNamespace(namespace string) bool {
	for _, toFind := range r.IgnoreNamespaces {
		if namespace == toFind {
			return true
		}
	}
	return false
}

func (r *NamespaceReconciler) reconcileNamespace(ctx context.Context, log logr.Logger, namespace corev1.Namespace, podMonitor *monitoringv1.PodMonitor) error {
	log = log.WithValues("namespace", podMonitor.Namespace, "name", podMonitor.Name)
	result, err := ctrl.CreateOrUpdate(
		ctx,
		r.Client,
		podMonitor,
		func() error {
			return updatePodMonitorAnnotation(ctx, r.Client, podMonitor)
		},
	)
	if err != nil {
		podMonitorsErrors.WithLabelValues(podMonitor.Name, podMonitor.Namespace).Inc()
		log.Error(err, "failed to create or update pod monitor")
		return err
	}
	log.WithValues("result", result).Info("created or updated pod monitor")

	return nil
}

func updatePodMonitorAnnotation(ctx context.Context, cl client.Client, podMonitor *monitoringv1.PodMonitor) error {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: podMonitor.Namespace,
		},
	}
	err := cl.Get(ctx, types.NamespacedName{Name: podMonitor.Namespace}, namespace)
	if err != nil {
		return err
	}
	// We should just override this known annotation and keep the rest as it is
	annotations := []string{Config.storageAnnotationKey, Config.alertManagerAnnotationKey, Config.remoteWriteAnnotationKey, Config.stackNameAnnotationKey}
	for _, annotation := range annotations {
		element, ok := namespace.Annotations[annotation]
		if ok {
			podMonitor.ObjectMeta.Annotations[annotation] = element
		}

	}
	return nil
}

func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager, grafanaStackChangeEvents chan event.GenericEvent) error {
	grafanaSource := &grafanaStackChangesSource{
		Client:  r.Client,
		changes: grafanaStackChangeEvents,
		log:     ctrl.Log.WithName("grafanaStackChangeWatchMap"),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		WatchesRawSource(
			grafanaSource,
		).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(
				func(ctx context.Context, a client.Object) []reconcile.Request {
					return []reconcile.Request{{
						NamespacedName: types.NamespacedName{
							Name: a.GetNamespace(),
						},
					}}
				},
			),
		).
		Complete(r)
}

type grafanaStackChangesSource struct {
	Client  client.Client
	changes chan event.GenericEvent
	log     logr.Logger
}

func (m *grafanaStackChangesSource) Start(ctx context.Context, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	go func() {
		for e := range m.changes {
			for _, req := range m.Map(e.Object) {
				wq.Add(req)
			}
		}
	}()
	return nil
}

// Map checks if the "stack changed event" matches one of the stacks
// configured in any of our namespaces, and enqueues a namespace
// reconciliation for the affected ones.
func (m *grafanaStackChangesSource) Map(event client.Object) []reconcile.Request {
	var requests []reconcile.Request

	namespaces := corev1.NamespaceList{}
	if err := m.Client.List(context.Background(), &namespaces); err != nil {
		m.log.Error(err, "failed to list namespaces")
		return requests
	}

	changedStack := event.GetName()
	for _, ns := range namespaces.Items {
		stacks, ok := ns.GetAnnotations()[Config.stackNameAnnotationKey]
		if !ok {
			m.log.Info("namespace does not have stack annotation", "namespace", ns.Name)
			continue
		}

		for _, configuredStack := range strings.Split(stacks, ",") {
			if configuredStack == changedStack {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name: ns.Name,
					},
				})
			}
		}
	}
	return requests
}
