package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/adevinta/observability-operator/pkg/grafanacloud"
	"github.com/go-logr/logr"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	promcommonconfig "github.com/prometheus/common/config"
	promconfig "github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/discovery/kubernetes"
	"gopkg.in/yaml.v3"
	autoscaling "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// PrometheusInstances is the structure definition to set DD metrics
type PrometheusInstances struct {
	PrometheusURL string   `json:"prometheus_url"`
	Namespace     string   `json:"namespace"`
	Tags          []string `json:"tags"`
	Metrics       []string `json:"metrics"`
}

type PrometheusAdditionalScrapeConfig []*promconfig.ScrapeConfig

var defaultResourceRequirements corev1.ResourceRequirements = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("100m"),
		corev1.ResourceMemory: resource.MustParse("128Mi"),
	},
	Limits: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("200m"),
		corev1.ResourceMemory: resource.MustParse("256Mi"),
	},
}

// DockerImage holds docker image registry reference
type DockerImage struct {
	Name string
	Tag  string
}

// PodMonitorReconciler reconciles a PodMonitor object
type PodMonitorReconciler struct {
	client.Client
	Log                            logr.Logger
	Scheme                         *runtime.Scheme
	ClusterName                    string
	Region                         string
	PrometheusNamespace            string
	PrometheusExposedDomain        string
	GrafanaCloudCredentials        string
	NodeSelectorTarget             string
	PrometheusDockerImage          DockerImage
	GrafanaCloudClient             GrafanaCloudClient
	EnableMetricsRemoteWrite       bool
	EnableVpa                      bool
	PrometheusServiceAccountName   string
	PrometheusPodPriorityClassName string
	PrometheusMonitoringTarget     string
	PrometheusExtraExternalLabels  map[string]string
}

func (r *PodMonitorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	monitor := monitoringv1.PodMonitor{}

	if err := r.Get(ctx, req.NamespacedName, &monitor); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := r.Log.WithValues("podmonitor", monitor.Name, "namespace", monitor.Namespace)
	stop, err := r.handleFinalizers(&monitor)
	if stop {
		if err == nil {
			return ctrl.Result{}, nil
		}
		// If there was an error retry after 5 minutes again
		return ctrl.Result{Requeue: true, RequeueAfter: 5}, err
	}
	err = r.reconcilePodMonitor(log, monitor)
	if err != nil {
		return ctrl.Result{Requeue: true, RequeueAfter: 5}, err
	}

	return ctrl.Result{}, err
}

func isPodMonitorBeingDeleted(monitor monitoringv1.PodMonitor) bool {
	return !monitor.ObjectMeta.DeletionTimestamp.IsZero()
}

func (r *PodMonitorReconciler) deletePrometheusIfNotRequired(monitor monitoringv1.PodMonitor) error {
	ctx := context.Background()
	monitors := &monitoringv1.PodMonitorList{}
	// TODO: Theoretically we can use client.MatchingFields() as listOpts to match annotations, so we would
	// not need the for loop afterwards
	if err := r.List(ctx, monitors, client.InNamespace(monitor.Namespace)); err != nil {
		return err
	}
	for _, m := range monitors.Items {
		if isGrafanaCloudStorageEnabled(m.ObjectMeta) && !isPodMonitorBeingDeleted(*m) {
			return nil
		}
	}

	// There is no monitor to scrape, so we need to remove the prometheus
	prometheus := newPrometheusObjectDef(monitor.Namespace, r.PrometheusNamespace)
	return r.Delete(ctx, prometheus)
}

func findInAnnotation(slice []string, sliceItem string) bool {
	for _, item := range slice {
		if item == sliceItem {
			return true
		}
	}
	return false
}

func getStorageAnnotation(annotations map[string]string) (string, bool) {
	value, ok := annotations[Config.storageAnnotationKey]
	if ok {
		return value, true
	}
	return "", false
}

func hasStorageDefined(monitor monitoringv1.PodMonitor) bool {
	_, ok := getStorageAnnotation(monitor.ObjectMeta.Annotations)
	return ok
}

func (r *PodMonitorReconciler) getNamespace(namespacename string) (*corev1.Namespace, error) {
	ns := corev1.Namespace{}

	if err := r.Get(context.Background(), types.NamespacedName{Name: namespacename}, &ns); err != nil {
		r.Log.Error(err, "Namespace could not be fetched")
		return nil, err
	}

	return &ns, nil
}

func customRemoteWriteSecretName(secret corev1.Secret) string {
	return fmt.Sprintf("custom-remote-write-%s-%s", secret.Name, secret.Namespace)
}

// Returns a namespacedname referencing a configMap, bool whether we could retrieve all the data,  and error if any
func (r *PodMonitorReconciler) getNamespacedNameObjectFromAnnotation(monitor monitoringv1.PodMonitor, annotation string) (*types.NamespacedName, bool, error) {
	ns, err := r.getNamespace(monitor.Namespace)
	if err != nil {
		return nil, false, err
	}

	customAlertManagerConfigMap, ok := ns.ObjectMeta.Annotations[annotation]
	if ok {
		parts := strings.Split(customAlertManagerConfigMap, "/")
		namespacedName := types.NamespacedName{}
		if len(parts) == 2 { //we have name and namespace
			namespacedName.Name = parts[1]
			namespacedName.Namespace = parts[0]
		} else {
			namespacedName.Name = parts[0]
			namespacedName.Namespace = monitor.Namespace
		}
		return &namespacedName, true, nil
	}
	return nil, false, nil
}

func (r *PodMonitorReconciler) reconcilePodMonitor(log logr.Logger, monitor monitoringv1.PodMonitor) error {
	prometheus := newPrometheusObjectDef(monitor.Namespace, r.PrometheusNamespace)
	log = log.WithValues("prometheusNamespace", prometheus.Namespace, "prometheusName", prometheus.Name)

	if !hasStorageDefined(monitor) {
		log.Info("Skipping PodMonitor with no storage")
		return nil
	}

	grafanaStackTenantMapping := map[string]*grafanacloud.Stack{}
	if r.EnableMetricsRemoteWrite && isGrafanaCloudStorageEnabled(monitor.ObjectMeta) {
		stacks, err := lookupGrafanaStacks(r.Client, monitor.Namespace)
		if err != nil {
			return err
		}

		for _, stack := range stacks {
			grafanaCloudStack, err := r.GrafanaCloudClient.GetStack(stack)
			if err != nil {
				log.Error(err, fmt.Sprintf("Skipping PodMonitor for stack '%s'. Check error message", stack))
				return err
			}

			secret := &corev1.Secret{}
			if err := r.Client.Get(context.Background(), client.ObjectKey{
				Namespace: r.PrometheusNamespace,
				Name:      r.GrafanaCloudCredentials,
			}, secret); err != nil {
				log.Error(err, fmt.Sprintf("secret %s not found. The secret is needed to configure the remote write to Grafana Cloud.", r.GrafanaCloudCredentials))
				os.Exit(1)
			}

			secret.Data[stack] = []byte(fmt.Sprintf("%d", grafanaCloudStack.MetricsInstanceID))
			if err := r.Client.Update(context.Background(), secret); err != nil {
				log.Error(err, fmt.Sprintf("secret %s could not be updated. The secret is needed to configure the remote write to Grafana Cloud.", r.GrafanaCloudCredentials))
				return err
			}

			log.WithValues("grafanacloudTenant", stack, "grafanaCloudStack", grafanaCloudStack.StackID).Info("injected grafana cloud credentials")
			grafanaStackTenantMapping[stack] = grafanaCloudStack
		}
	}
	if secret, ok, _ := r.getNamespacedNameObjectFromAnnotation(monitor, Config.remoteWriteAnnotationKey); ok {
		err := r.syncCustomStorageSecret(secret, log, monitor)
		if err != nil {
			return err
		}
	}
	err := r.createOrUpdatePrometheus(monitor, prometheus, grafanaStackTenantMapping, r.NodeSelectorTarget)
	if err != nil {
		log.Error(err, "Can not create or update Prometheus.")
		return err
	}
	log.Info("created or updated prometheus")

	err = r.createOrUpdateAdditionalScrappingConfiguration(prometheus)
	if err != nil {
		log.Error(err, "Can not create or update the Secret with the additional scrapping configuration.")
		return err
	}
	log.Info("created or updated secret with additional scrapping config")
	err = r.createOrUpdatePrometheusRules(monitor, prometheus)
	if err != nil {
		log.Error(err, "Can not create or update PromethesRules.")
		return err
	}
	log.Info("created or updated prometheus rules")

	if r.EnableVpa {
		err = r.createOrUpdateVerticalPodAutoscaler(prometheus)
		if err != nil {
			log.Error(err, "Can not create or update VerticalPodAutoscaler.")
			return err
		}
		log.Info("created or updated prometheus VPA")
	}

	return nil
}

func (r *PodMonitorReconciler) syncSecret(secretName, secretNamespace, targetNamespace string) error {
	srcSecret := corev1.Secret{}
	ctx := context.Background()

	if err := r.Get(ctx, types.NamespacedName{Namespace: secretNamespace, Name: secretName}, &srcSecret); err != nil {
		r.Log.Error(err, "Error: secret %s not found. The secret is needed it to configure the remote write to the custom remote storage.", secretName)
		return err
	}

	dstSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      customRemoteWriteSecretName(srcSecret),
			Namespace: targetNamespace,
		},
	}

	_, err := ctrl.CreateOrUpdate(ctx, r.Client, &dstSecret, func() error {
		dstSecret.Data = srcSecret.Data
		return nil
	})
	return err

}

func (r *PodMonitorReconciler) syncCustomStorageSecret(namespacedName *types.NamespacedName, log logr.Logger, monitor monitoringv1.PodMonitor) error {
	secret := corev1.Secret{}
	ctx := context.Background()

	if err := r.Get(ctx, *namespacedName, &secret); err != nil {
		log.Error(err, "Error: secret %s not found. The secret is needed it to configure the remote write to the custom remote storage.", *namespacedName)
		return err
	}

	if referencedSecrets, ok := secret.ObjectMeta.Annotations[Config.referencedSecretAnnotationKeys]; ok {
		for _, ref := range strings.Split(referencedSecrets, ",") {
			err := r.syncSecret(ref, secret.Namespace, r.PrometheusNamespace)
			if err != nil {
				return err
			}

		}
	}
	return nil
}

func getAvailableStorageFromNamespace(k8sclient client.Client, namespace string, log logr.Logger) (string, error) {
	ns := corev1.Namespace{}
	err := k8sclient.Get(context.TODO(), types.NamespacedName{Name: namespace}, &ns)
	if err != nil {
		log.Error(err, "Failed to get Namespace")
		return "", err
	}

	availableStorage, ok := getStorageAnnotation(ns.ObjectMeta.Annotations)
	if ok {
		log.Info("using storage from namespace annotation: " + availableStorage)
		return availableStorage, nil
	}
	log.Info("using default storage: grafanacloud")

	return "grafanacloud", nil
}

func (r *PodMonitorReconciler) remoteWriteConfigFromSecret(secretRef types.NamespacedName) ([]monitoringv1.RemoteWriteSpec, error) {
	secret := corev1.Secret{}
	err := r.Get(context.TODO(), secretRef, &secret)
	if err != nil {
		r.Log.Error(err, "Failed to get secret %s", secretRef)
		return nil, err
	}
	spec := monitoringv1.RemoteWriteSpec{}
	remoteWrite, ok := secret.Data["remote-write"]
	if !ok {
		return nil, fmt.Errorf("'remote-write' entry doest not exist in %s", secretRef)

	}
	var body interface{}
	err = yaml.Unmarshal(remoteWrite, &body)
	if err != nil {
		return nil, err
	}
	jsonspec, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(jsonspec, &spec)
	if err != nil {
		return nil, err
	}

	if spec.URL == "" {
		return nil, fmt.Errorf("Missing URL in %s", string(jsonspec))
	}

	if spec.BasicAuth != nil {
		s, err := getSecret(r.Client, types.NamespacedName{Name: spec.BasicAuth.Password.Name, Namespace: secretRef.Namespace})
		if err != nil {
			return nil, err
		}
		spec.BasicAuth.Password.Name = customRemoteWriteSecretName(*s)
		s, err = getSecret(r.Client, types.NamespacedName{Name: spec.BasicAuth.Username.Name, Namespace: secretRef.Namespace})
		if err != nil {
			return nil, err
		}
		spec.BasicAuth.Username.Name = customRemoteWriteSecretName(*s)
	}

	return []monitoringv1.RemoteWriteSpec{spec}, nil

}

func (r *PodMonitorReconciler) alertManagerConfigFromConfigMap(configMapRef types.NamespacedName) (*monitoringv1.AlertingSpec, error) {
	configMap := corev1.ConfigMap{}
	err := r.Get(context.TODO(), configMapRef, &configMap)
	if err != nil {
		r.Log.Error(err, "Failed to get configmap %s", configMapRef)
		return nil, err
	}
	spec := monitoringv1.AlertingSpec{}

	alertManager, ok := configMap.Data["alert-manager"]
	if !ok {
		return nil, fmt.Errorf("'alert-manager' entry doest not exist in %s", configMapRef)

	}

	var body interface{}
	err = yaml.Unmarshal([]byte(alertManager), &body)
	if err != nil {
		return nil, err
	}
	jsonspec, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(jsonspec, &spec.Alertmanagers)
	if err != nil {
		return nil, err
	}

	return &spec, nil

}

func (r *PodMonitorReconciler) publishErrEvent(name string, podmonitor monitoringv1.PodMonitor, e error) error {
	event := corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "GrafanaCloudOperatorEvent",
			Namespace: podmonitor.Namespace,
		},
	}

	_, err := ctrl.CreateOrUpdate(
		context.TODO(),
		r.Client,
		&event,
		func() error {
			event.Message = e.Error()
			event.Count = event.Count + 1
			event.LastTimestamp = metav1.NewTime(time.Now())
			event.InvolvedObject = corev1.ObjectReference{
				Kind:      podmonitor.Kind,
				Namespace: podmonitor.Namespace,
				Name:      podmonitor.Name,
			}
			return nil
		},
	)
	return err

}

func (r *PodMonitorReconciler) getTenantCustomRelabelConfigs(namespace string, prometheus *monitoringv1.Prometheus) []monitoringv1.RelabelConfig {
	var existingTenantCustomRelabelConfigs []monitoringv1.RelabelConfig
	defaultPrometheusConfig := monitoringv1.RelabelConfig{
		Action:       "drop",
		SourceLabels: []string{"__name__"},
		Regex:        "prometheus_.*",
	}
	configMap := corev1.ConfigMap{}

	if len(prometheus.Spec.RemoteWrite) > 0 {
		existingTenantCustomRelabelConfigs = prometheus.Spec.RemoteWrite[0].WriteRelabelConfigs
	} else {
		existingTenantCustomRelabelConfigs = append(existingTenantCustomRelabelConfigs, defaultPrometheusConfig)
	}

	err := r.Client.Get(context.TODO(), client.ObjectKey{Namespace: namespace, Name: "custom-relabel-configs"}, &configMap)
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			r.Log.Info("ConfigMap not found, returning empty relabel configs")
			return existingTenantCustomRelabelConfigs
		}

		r.Log.Error(err, "Failed to get ConfigMap %s", "custom-relabel-configs")
		return existingTenantCustomRelabelConfigs
	}

	relabelConfigs, ok := configMap.Data["relabel-configs"]
	if !ok {
		r.Log.Error(err, "Failed to get ConfigMap key relabel-configs", "custom-relabel-configs")

		publishErr := publishPrometheusErrEvent(r.Client, "CustomRelabelConfigReadError", namespace, prometheus, err)
		if publishErr != nil {
			r.Log.Error(publishErr, "Error sending error Event to K8s")
		}

		return existingTenantCustomRelabelConfigs
	}

	var configs []monitoringv1.RelabelConfig
	err = yaml.Unmarshal([]byte(relabelConfigs), &configs)
	if err != nil {
		r.Log.Error(err, "Failed to unmarshal relabel-configs")

		publishErr := publishPrometheusErrEvent(r.Client, "CustomRelabelConfigUnmarshalError", namespace, prometheus, err)
		if publishErr != nil {
			r.Log.Error(publishErr, "Error sending error Event to K8s")
		}

		return existingTenantCustomRelabelConfigs
	}

	r.Log.Info(fmt.Sprintf("Successfully fetched Custom RelabelConfig ConfigMap Name for %s", namespace))

	configs = append(configs, defaultPrometheusConfig)

	return configs
}

func (r *PodMonitorReconciler) updatePrometheusObjectDef(monitor monitoringv1.PodMonitor, prometheus *monitoringv1.Prometheus, grafanaStackTenantMapping map[string]*grafanacloud.Stack, nodeSelectorTarget string) error {
	replicas := int32(1)
	shards := int32(1)
	// It is important not to overwrite the whole ObjectMeta in order not to break
	// the CreateOrUpdate function
	var availableStorage string
	availableStorage, err := getAvailableStorageFromNamespace(r.Client, monitor.Namespace, r.Log)
	if err != nil {
		return err
	}

	prometheus.ObjectMeta.Annotations = map[string]string{
		Config.storageAnnotationKey: availableStorage,
		Config.accountAnnotationKey: monitor.Namespace,
	}
	// If there were resources set up manually we keep them
	// This is now required because big namespaces like serenity need big prometheus
	// but we dont have a way (yet) to tell how much resources a given prometheus needs.
	// if parent object doesnt have any resource, we set a default one.
	resources := prometheus.Spec.Resources
	if resources.Size() == 0 {
		resources = defaultResourceRequirements
	}

	var remoteWriteConfig []monitoringv1.RemoteWriteSpec
	if r.EnableMetricsRemoteWrite && isGrafanaCloudStorageEnabled(monitor.ObjectMeta) {
		for stack, grafanaCloudStack := range grafanaStackTenantMapping {
			remoteWriteConfig = append(remoteWriteConfig, monitoringv1.RemoteWriteSpec{
				URL: fmt.Sprintf("%s/api/prom/push", grafanaCloudStack.PromURL),
				BasicAuth: &monitoringv1.BasicAuth{
					Username: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: r.GrafanaCloudCredentials,
						},
						Key: stack,
					},
					Password: corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: r.GrafanaCloudCredentials,
						},
						Key: "grafana-cloud-api-key",
					},
				},
				WriteRelabelConfigs: r.getTenantCustomRelabelConfigs(monitor.Namespace, prometheus),
			})
		}
	}

	if secret, ok, _ := r.getNamespacedNameObjectFromAnnotation(monitor, Config.remoteWriteAnnotationKey); ok {
		remoteWriteConfig, err = r.remoteWriteConfigFromSecret(*secret)
		if err != nil {
			publishErr := r.publishErrEvent("RemoteWriteConfigFromSecret", monitor, err)
			if publishErr != nil {
				r.Log.Error(publishErr, "Error sending error Event to K8s")
			}
			return err
		}
	}
	var alertingConfig *monitoringv1.AlertingSpec
	if alertingConfigMap, ok, _ := r.getNamespacedNameObjectFromAnnotation(monitor, Config.alertManagerAnnotationKey); ok {
		alertingConfig, err = r.alertManagerConfigFromConfigMap(*alertingConfigMap)
		if err != nil {
			publishErr := r.publishErrEvent("alertManagerconfigFromConfigMap", monitor, err)
			if publishErr != nil {
				r.Log.Error(publishErr, "Error sending  Event to K8s")
			}
			return err
		}
	}
	mergedLabels := map[string]string{
		"cluster": r.ClusterName,
		"monitor": "prometheus-local",
		"account": monitor.Namespace,
		"region":  r.Region,
	}

	for k, v := range r.PrometheusExtraExternalLabels {
		mergedLabels[k] = v
	}

	prometheus.Spec = monitoringv1.PrometheusSpec{
		ListenLocal:        false,
		Paused:             false,
		PortName:           "web",
		PriorityClassName:  r.PrometheusPodPriorityClassName,
		RoutePrefix:        "/",
		Retention:          "30m",
		ServiceAccountName: r.PrometheusServiceAccountName,
		LogFormat:          "logfmt",
		LogLevel:           "warn",
		Replicas:           &replicas,
		Shards:             &shards,
		Version:            r.PrometheusDockerImage.Tag,
		BaseImage:          r.PrometheusDockerImage.Name,
		ExternalURL:        "http://" + monitor.Namespace + "-metrics." + r.PrometheusExposedDomain,
		RemoteWrite:        remoteWriteConfig,
		Alerting:           alertingConfig,
		ExternalLabels:     mergedLabels,
		PodMetadata: &monitoringv1.EmbeddedObjectMetadata{
			Labels: map[string]string{
				Config.accountLabelKey: monitor.Namespace,
			},
		},
		PodMonitorNamespaceSelector: &metav1.LabelSelector{},
		PodMonitorSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				Config.accountLabelKey: monitor.Namespace,
			},
		},
		RuleNamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"name": monitor.Namespace,
			},
		},
		RuleSelector:                    &metav1.LabelSelector{},
		ServiceMonitorNamespaceSelector: &metav1.LabelSelector{},
		ServiceMonitorSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				Config.accountLabelKey: monitor.Namespace,
			},
		},
		Resources: resources,
	}

	if r.PrometheusMonitoringTarget != "" {
		prometheus.Spec.AdditionalScrapeConfigs = &corev1.SecretKeySelector{
			Key:                  "prometheus",
			LocalObjectReference: corev1.LocalObjectReference{Name: r.secretName(prometheus, r.PrometheusMonitoringTarget)},
		}
	}

	if nodeSelectorTarget != "" {
		prometheus.Spec.NodeSelector = map[string]string{
			nodeSelectorTarget: "true",
		}
		prometheus.Spec.Tolerations = []corev1.Toleration{
			{
				Key:      nodeSelectorTarget,
				Operator: "Equal",
				Value:    "true",
				Effect:   "NoSchedule",
			},
		}
	}
	return nil
}

func (r *PodMonitorReconciler) createOrUpdateAdditionalScrappingConfig(prometheus *monitoringv1.Prometheus, monitoringTargetName string) error {
	ctx := context.Background()

	additionalScrapingConfigSecret := r.newAdditionalScrapingSecretDef(prometheus, monitoringTargetName)

	_, err := ctrl.CreateOrUpdate(
		ctx,
		r.Client,
		additionalScrapingConfigSecret,
		func() error {
			err := r.updateAdditionalScrappingSecretDef(prometheus, additionalScrapingConfigSecret, monitoringTargetName)
			if err != nil {
				r.Log.Error(err, "Failed to update secret with additional scrapping config")
				prometheusErrors.WithLabelValues(prometheus.Name, prometheus.Namespace, additionalScrapingConfigSecret.Kind).Inc()
				return err
			}

			err = controllerutil.SetOwnerReference(prometheus, additionalScrapingConfigSecret, r.Scheme)
			if err != nil {
				r.Log.Error(err, "Failed to assign owner reference to the secret with additional scrapping config")
				prometheusErrors.WithLabelValues(prometheus.Name, prometheus.Namespace, additionalScrapingConfigSecret.Kind).Inc()
				err = fmt.Errorf("%v %v", additionalScrapingConfigSecret, err)
				return err
			}

			return nil
		},
	)
	return err
}

func (r *PodMonitorReconciler) createOrUpdatePrometheusRules(monitor monitoringv1.PodMonitor, prometheus *monitoringv1.Prometheus) error {
	ruleName := "prometheus-remote-write-behind-seconds"
	rules := []monitoringv1.Rule{
		{
			Record: strings.ReplaceAll(ruleName, "-", "_"),
			Expr: intstr.FromString(
				"max_over_time(prometheus_remote_storage_highest_timestamp_in_seconds{job=\"prometheus-scraper\"}[2m]) " +
					"- ignoring(remote_name, url) group_right " +
					"max_over_time(prometheus_remote_storage_queue_highest_sent_timestamp_seconds{job=\"prometheus-scraper\"}[2m])"),
		},
	}
	err := r.createOrUpdateInternalPrometheusRule(prometheus, ruleName, rules)
	if err != nil {
		r.Log.Error(err, "Can not create or update a prometheus rule", "prometheus", prometheus.Name, "prometheusRule", ruleName)
		return err
	}
	if monitor.Spec.SampleLimit > 0 {
		ruleName = "scrape-config-sample-limit"
		rules = []monitoringv1.Rule{
			{
				Record: strings.ReplaceAll(ruleName, "-", "_"),
				Expr:   intstr.FromInt(int(monitor.Spec.SampleLimit)),
				Labels: map[string]string{
					"job":       fmt.Sprintf("%s/%s", monitor.Namespace, monitor.Name),
					"namespace": monitor.Namespace,
				},
			},
		}
		err = r.createOrUpdatePrometheusRule(&monitor, fmt.Sprintf("%s-%s", monitor.GetName(), ruleName), rules)
		if err != nil {
			r.Log.Error(err, "Can not create or update a prometheus rule", "prometheus", prometheus.Name, "prometheusRule", ruleName)
			return err
		}
	}

	ruleName = "prometheus-remote-write-storage-failures-percentage"
	rules = []monitoringv1.Rule{
		{
			Record: strings.ReplaceAll(ruleName, "-", "_"),
			Expr:   intstr.FromString("(rate(prometheus_remote_storage_failed_samples_total{job=\"prometheus-scraper\"}[2m])/(rate(prometheus_remote_storage_failed_samples_total{job=\"prometheus-scraper\"}[2m])+rate(prometheus_remote_storage_succeeded_samples_total{job=\"prometheus-scraper\"}[2m])))* 100"),
		},
	}
	err = r.createOrUpdateInternalPrometheusRule(prometheus, ruleName, rules)
	if err != nil {
		r.Log.Error(err, "Can not create or update a prometheus rule", "prometheus", prometheus.Name, "prometheusRule", ruleName)
		return err
	}
	return nil
}

func (r *PodMonitorReconciler) createOrUpdateInternalPrometheusRule(owner metav1.Object, name string, rules []monitoringv1.Rule) error {
	return r.createOrUpdatePrometheusRule(owner, fmt.Sprintf("%s-%s", getObjectName(owner), name), rules)
}

func (r *PodMonitorReconciler) createOrUpdatePrometheusRule(owner metav1.Object, name string, rules []monitoringv1.Rule) error {
	ctx := context.Background()

	prometheusRule := r.newPrometheusRuleObjectDef(owner.GetNamespace(), name)

	_, err := ctrl.CreateOrUpdate(
		ctx,
		r.Client,
		prometheusRule,
		func() error {
			err := r.updatePrometheusRuleObjectDef(owner, prometheusRule, rules)
			if err != nil {
				r.Log.Error(err, "Failed to update prometheus rule")
				prometheusErrors.WithLabelValues(owner.GetName(), owner.GetNamespace(), prometheusRule.Kind).Inc()
				return err
			}

			err = controllerutil.SetOwnerReference(owner, prometheusRule, r.Scheme)
			if err != nil {
				r.Log.Error(err, "Failed to assign owner reference to prometheus rule")
				prometheusErrors.WithLabelValues(owner.GetName(), owner.GetNamespace(), prometheusRule.Kind).Inc()
				return err
			}

			return nil
		},
	)
	return err
}

func (r *PodMonitorReconciler) newPrometheusRuleObjectDef(namespace, name string) *monitoringv1.PrometheusRule {
	return &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}
func (r *PodMonitorReconciler) updatePrometheusRuleObjectDef(owner metav1.Object, prometheusRule *monitoringv1.PrometheusRule, rules []monitoringv1.Rule) error {

	tenantNamespace, found := owner.GetAnnotations()[Config.accountAnnotationKey]
	if !found {
		return errors.New("annotation" + Config.accountAnnotationKey + " not found in owner Object.")
	}

	prometheusRule.ObjectMeta.Labels = map[string]string{
		Config.accountLabelKey: tenantNamespace,
	}
	prometheusRule.Spec = monitoringv1.PrometheusRuleSpec{
		Groups: []monitoringv1.RuleGroup{
			{
				Name:  prometheusRule.ObjectMeta.Name,
				Rules: rules,
			},
		},
	}
	return nil
}

func (r *PodMonitorReconciler) secretName(prometheus *monitoringv1.Prometheus, targetMonitor string) string {
	return fmt.Sprintf("%s-%s", getObjectName(prometheus), targetMonitor)
}

func (r *PodMonitorReconciler) newAdditionalScrapingSecretDef(prometheus *monitoringv1.Prometheus, targetMonitor string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.secretName(prometheus, targetMonitor),
			Namespace: prometheus.Namespace,
		},
	}

}

func (r *PodMonitorReconciler) updateAdditionalScrappingSecretDef(prometheus *monitoringv1.Prometheus, cm *corev1.Secret, targetMonitor string) error {
	tenantNamespace, found := prometheus.Spec.PodMetadata.Labels[Config.accountLabelKey]
	if !found {
		return errors.New("Pod metadata label " + Config.accountLabelKey + " not found in Prometheus.")
	}
	cm.ObjectMeta.Labels = map[string]string{
		Config.accountLabelKey: tenantNamespace,
	}

	additionalScrapeConfig := PrometheusAdditionalScrapeConfig{}
	scrapper := newIngressAndClusterScraper()
	scrapper.JobName = fmt.Sprintf("%s/prometheus-%s-%s/0", prometheus.Namespace, tenantNamespace, targetMonitor)
	scrapper.ServiceDiscoveryConfigs = append(scrapper.ServiceDiscoveryConfigs, &kubernetes.SDConfig{
		Role: kubernetes.RoleService,
		NamespaceDiscovery: kubernetes.NamespaceDiscovery{
			Names: []string{prometheus.Namespace},
		},
		HTTPClientConfig: promcommonconfig.DefaultHTTPClientConfig,
	})
	scrapper.Params.Add("match[]", fmt.Sprintf(`{federate="true", namespace="%s"}`, tenantNamespace))

	additionalScrapeConfig = append(additionalScrapeConfig, &prometheusLocalScrapper)
	additionalScrapeConfig = append(additionalScrapeConfig, &scrapper)

	data, err := yaml.Marshal(additionalScrapeConfig)
	if err != nil {
		return fmt.Errorf("There was an error Marshalling the additional scrapping config %v", err)
	}

	cm.StringData = map[string]string{
		"prometheus": fixPrometheusConfig(string(data)),
	}

	return nil
}

func fixPrometheusConfig(cfg string) string {
	redirectRe := regexp.MustCompile("[ ]*follow_redirects:[ ]+(false|true)")

	kubeconfigFileRe := regexp.MustCompile(`[ ]*kubeconfig_file:[ ]+""`)
	// For some reason, the prometheus serializer generates an additional follow_reditect: false
	// at the root of the kubernetes service discovery while it's set to true inside
	cfg = redirectRe.ReplaceAllString(cfg, "")
	cfg = kubeconfigFileRe.ReplaceAllString(cfg, "")
	return cfg
}

func (r *PodMonitorReconciler) createOrUpdateAdditionalScrappingConfiguration(prometheus *monitoringv1.Prometheus) error {
	targetName := r.PrometheusMonitoringTarget
	err := r.createOrUpdateAdditionalScrappingConfig(prometheus, targetName)
	if err != nil {
		r.Log.Error(err, "Can not create or update the secret with additional scrapping config", "prometheus", prometheus.Name, "secretName", targetName)
		return err
	}

	return nil
}

func (r *PodMonitorReconciler) removeFinalizer(podMonitor *monitoringv1.PodMonitor) error {
	controllerutil.RemoveFinalizer(podMonitor, Config.podmonitorFinalizer)
	if err := r.Update(context.Background(), podMonitor); err != nil {
		return err
	}

	if err := r.deletePrometheusIfNotRequired(*podMonitor); err != nil {
		return err
	}
	return nil
}
func (r *PodMonitorReconciler) handleFinalizers(podMonitor *monitoringv1.PodMonitor) (bool, error) {

	podMonitorWithFinalizer := controllerutil.ContainsFinalizer(podMonitor, Config.podmonitorFinalizer)
	// examine DeletionTimestamp to determine if object is under deletion
	if podMonitor.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer and update the object. This is equivalent
		// registering our finalizer.
		if !podMonitorWithFinalizer {
			podMonitor.ObjectMeta.Finalizers = append(podMonitor.ObjectMeta.Finalizers, Config.podmonitorFinalizer)
			if err := r.Update(context.Background(), podMonitor); err != nil {
				return true, err
			}
		}
		return false, nil
	}
	if podMonitorWithFinalizer {
		if err := r.removeFinalizer(podMonitor); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (r *PodMonitorReconciler) createOrUpdatePrometheus(monitor monitoringv1.PodMonitor, prometheus *monitoringv1.Prometheus, grafanaStackTenantMapping map[string]*grafanacloud.Stack, nodeSelectorTarget string) error {
	ctx := context.Background()
	_, err := ctrl.CreateOrUpdate(
		ctx,
		r.Client,
		prometheus,
		func() error {
			return r.updatePrometheusObjectDef(monitor, prometheus, grafanaStackTenantMapping, nodeSelectorTarget)
		},
	)
	if err != nil {
		prometheusErrors.WithLabelValues(prometheus.Name, prometheus.Namespace, prometheus.Kind).Inc()
		return err
	}

	return nil
}

func (r *PodMonitorReconciler) createOrUpdateVerticalPodAutoscaler(prometheus *monitoringv1.Prometheus) error {
	updateModeAuto := vpav1.UpdateMode("Auto")
	vpa := &vpav1.VerticalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: prometheus.Name, Namespace: prometheus.Namespace}}

	ctx := context.Background()
	_, err := ctrl.CreateOrUpdate(
		ctx,
		r.Client,
		vpa,
		func() error {
			vpa.Spec = vpav1.VerticalPodAutoscalerSpec{
				UpdatePolicy: &vpav1.PodUpdatePolicy{
					UpdateMode: &updateModeAuto,
				},
				ResourcePolicy: &vpav1.PodResourcePolicy{
					ContainerPolicies: []vpav1.ContainerResourcePolicy{{
						ContainerName: "prometheus",
						MaxAllowed: corev1.ResourceList{
							corev1.ResourceCPU: *resource.NewQuantity(8, resource.BinarySI),
						},
					}}},
			}

			targetRef := &autoscaling.CrossVersionObjectReference{
				APIVersion: monitoringv1.SchemeGroupVersion.String(),
				Kind:       "Prometheus",
				Name:       prometheus.Name,
			}
			vpa.Spec.TargetRef = targetRef
			err := controllerutil.SetOwnerReference(prometheus, vpa, r.Scheme)
			return err
		},
	)
	return err
}

func (r *PodMonitorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&monitoringv1.PodMonitor{}).
		Complete(r)
}
