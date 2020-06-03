package controllers

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/labels"
)

const (
	httpPort     = int32(12345)
	otelGrpcPort = int32(4317)
	otelHttpPort = int32(4318)
)

func reconcileTracesCollector(ctx context.Context, client client.Client, log logr.Logger, grafanaCloudClient GrafanaCloudClient, grafanaCloudTracesToken, clusterName, tracesNamespace, tenantNamespace string, ignoreApps []string, excludeLabelsSelector labels.Selector, enableVPA bool) (ctrl.Result, error) {
	creds, err := getTracesCredentials(client, grafanaCloudClient, log, tenantNamespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	collector, err := NewTracesCollector(
		tenantNamespace,
		tracesNamespace,
		clusterName,
		enableVPA,
		WithHTTPPort(httpPort),
		WithServicePorts(
			servicePort{Name: "otel-http", Port: otelHttpPort},
			servicePort{Name: "otel-grpc", Port: otelGrpcPort},
		),
		AlloyWithOTelCredentials(creds...),
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	var filters []podFilter
	for _, ignoredApp := range ignoreApps {
		filters = append(filters, excludePodsOnLabel("app", ignoredApp))
	}

	if excludeLabelsSelector != nil && !excludeLabelsSelector.Empty() {
		filters = append(filters, filterBySelector(excludeLabelsSelector))
	}

	pods, err := checkNamespaceHasActionableWorkloads(client, log, tenantNamespace, filters...)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(pods) == 0 {
		// No valid workload, remove the collector. If it does
		// not exist this won't fail and will be a noop.
		for _, obj := range collector.ObjectsToDelete() {
			log := log.WithValues("object", fmt.Sprintf("%T", obj), "name", obj.GetName(), "namespace", obj.GetNamespace())

			err := client.Delete(ctx, obj)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					log.Error(err, "Failed to delete object in namespace")
					return ctrl.Result{Requeue: true, RequeueAfter: 30 * time.Second}, err
				}
				// The object is not found we ignore it
			} else {
				log.Info("Object %T deleted in namespace %s", obj, obj.GetNamespace())
			}
		}

		return ctrl.Result{}, nil
	}

	if err := collector.CreateOrUpdateSecret(ctx, client, grafanaCloudTracesToken); err != nil {
		log.Error(err, "Error when reconciling secret")
		return ctrl.Result{}, err
	}

	if err := collector.CreateOrUpdateConfigMap(ctx, client); err != nil {
		log.Error(err, "Error when reconciling configmap")
		return ctrl.Result{}, err
	}

	if err := collector.CreateOrUpdateService(ctx, client); err != nil {
		log.Error(err, "Error when reconciling service")
		return ctrl.Result{}, err
	}

	if err := collector.CreateOrUpdateDeployment(ctx, client); err != nil {
		log.Error(err, "Error when reconciling deployment")
		return ctrl.Result{}, err
	}

	if err := collector.CreateOrUpdateNetworkPolicy(ctx, client); err != nil {
		log.Error(err, "Error when reconciling network policy")
		return ctrl.Result{}, err
	}

	if enableVPA {
		if err := collector.CreateOrUpdateVPA(ctx, client); err != nil {
			log.Error(err, "Error when reconciling VPA")
			return ctrl.Result{}, err
		}
	}

	log.Info("All traces objects successfully reconciled")
	return ctrl.Result{}, nil
}

func (r *NamespaceReconciler) deleteTracesCollector(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tc, _ := NewTracesCollector(
		req.Name, // tenantNamespace
		r.TracesNamespace,
		r.ClusterName,
		r.EnableVPA,
		WithHTTPPort(httpPort),
		WithServicePorts(
			servicePort{Name: "otel-http", Port: otelHttpPort},
			servicePort{Name: "otel-grpc", Port: otelGrpcPort},
		),
	)
	objs := tc.ObjectsToDelete()

	for _, obj := range objs {
		log := r.Log.WithValues("object", fmt.Sprintf("%T", obj), "name", obj.GetName(), "namespace", obj.GetNamespace())

		err := r.Delete(ctx, obj)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete object in namespace")
				return ctrl.Result{Requeue: true, RequeueAfter: 30 * time.Second}, err
			}
			// The object is not found we ignore it
		} else {
			log.Info("Object deleted in namespace")
		}
	}
	return ctrl.Result{}, nil
}

func getTracesCredentials(client client.Client, grafanaCloudClient GrafanaCloudClient, log logr.Logger, namespace string) ([]OTelCredentials, error) {
	grafanaStackNames, err := lookupGrafanaStacks(client, namespace)
	if err != nil || len(grafanaStackNames) < 1 {
		log.Error(err, "Failed to lookup grafana stacks")
		return []OTelCredentials{}, err
	}

	var out []OTelCredentials
	for _, stack := range grafanaStackNames {
		otlpId, otlpUrl, err := grafanaCloudClient.GetTracesConnection(stack)
		if err != nil {
			log.Error(err, "Failed to get Grafana stack connections info for stack", "grafana_stacks", stack)
			continue
		}

		out = append(out, OTelCredentials{
			User:     strconv.Itoa(otlpId),
			Endpoint: fmt.Sprintf("\"%s/otlp\"", otlpUrl),
		})
	}
	if len(out) == 0 {
		err := fmt.Errorf("no grafana stack credentials retrieved")
		return []OTelCredentials{}, err
	}
	return out, nil
}
