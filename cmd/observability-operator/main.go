package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	advlog "github.com/adevinta/go-log-toolkit"
	"github.com/adevinta/observability-operator/pkg/controllers"
	"github.com/adevinta/observability-operator/pkg/grafanacloud"
	"github.com/grafana/grafana-com-public-clients/go/gcom"
	"github.com/sirupsen/logrus"
	networkingv1 "k8s.io/api/networking/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	validatingfield "k8s.io/apimachinery/pkg/util/validation/field"

	apilabels "k8s.io/apimachinery/pkg/labels"
	// +kubebuilder:scaffold:imports
)

const (
	missing = "missing"
)

type grafanaCloudClient interface {
	controllers.GrafanaCloudClient
	grafanacloud.GrafanaCloudStackLister
}

func main() {
	// ############## Manager options ##############
	var metricsAddr string
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	var enableLeaderElection bool
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false, "Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	// #############################################

	// ############## metadata to add to metrics, logs, traces... ##############
	var clusterName string
	flag.StringVar(&clusterName, "cluster-name", missing, "The name of the cluster being run. This will add the cluster tag to all collected metrics.")
	var clusterRegion string
	flag.StringVar(&clusterRegion, "cluster-region", missing, "The region the cluster runs in. This will add the region tag to all collected metrics.")
	var clusterDomain string
	flag.StringVar(&clusterDomain, "cluster-domain", "adevinta.com", "The domain used for labels, annotations and leader election.")
	// #############################################

	// ############## Feature toggles ##############
	// Enable sending to GrafanaCloud if storage is defined as such
	// TODO: Always enabled? Can we drop it for now?
	var enableMetricsRemoteWrite bool
	flag.BoolVar(&enableMetricsRemoteWrite, "metrics-remote-write-to-grafana-cloud", true, "Enable/Disable remote-write of metrics to Grafana Cloud")
	// Enable VPA provisioning for Prometheus
	var enableVpa bool
	flag.BoolVar(&enableVpa, "enable-vpa", false, "Enable/Disable support for VPA")
	// #############################################

	// ############## Filtering/ignoring workloads ##############
	// Avoids recognising apps with this label as valid to create
	// Trace Collectors
	var ignoreApps string
	flag.StringVar(&ignoreApps, "exclude-apps-label", "", "comma-separated list of app label values to exclude, if any")

	// Filter workloads by label selector
	var workloadLabelSelector string
	flag.StringVar(&workloadLabelSelector, "exclude-workload-selector", "", "Exclude applications using the provided K8s Selector. It is a string specification of a equality-based or set-based selector as described in https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors . This selector will be matched against deployments, statefulsets, daemonsets, etc. Matching ones will be skipped")
	// Filter namespaces by label selector
	var namespaceLabelSelector string
	flag.StringVar(&namespaceLabelSelector, "exclude-namespace-selector", "", "Exclude namespaces using the provided K8s Selector. It is a string specification of a equality-based or set-based selector as described in https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors . This selector will be matched against namespaces. Matching ones will be skipped")

	// Do not process workloads in these namespaces, nor react to
	// its events
	var ignoreNamespaces string
	flag.StringVar(&ignoreNamespaces, "exclude-namespaces-name", "", "comma-separated list of namespaces to exclude, if any")
	// #############################################

	// ############## GrafanaCloud configuration ##############
	// Name identifier of the GrafanaCloud organization controlling all stacks
	var grafanaCloudOrgSlug string
	flag.StringVar(&grafanaCloudOrgSlug, "grafana-cloud-organization-name", "adevinta", "The name of the grafanacloud organization hosting all stacks used as destination for telemetry")
	// Name of the secret containing credentials to access GrafanaCloud
	var grafanaCloudCredentials string
	flag.StringVar(&grafanaCloudCredentials, "grafana-cloud-metrics-credentials", "", "Secret used to store Grafana Cloud credentials")
	var grafanaCloudClientCache bool
	flag.BoolVar(&grafanaCloudClientCache, "grafana-cloud-client-use-cache", false, "enables the usage of a client-level cache in the GrafanaCloud client. This can significantly reduce the amount of calls to the GCOM API for the stacks that exist.")
	// Periodicity to reconcile the status of Grafana stacks for changes
	var grafanaCloudStackReconcilePeriod string
	defaultGrafanaCloudStackReconcilePeriod := 5 * time.Minute
	flag.StringVar(&grafanaCloudStackReconcilePeriod, "grafana-cloud-stack-reconcile-period", defaultGrafanaCloudStackReconcilePeriod.String(), "Frequency to reconcile grafana stack change that triggers namespace reconcile (expressed in go duration: https://pkg.go.dev/time#ParseDuration)")
	// #############################################

	// ############## Prometheus configuration settings ##############
	// Prometheus image details
	var prometheusDockerImage string
	flag.StringVar(&prometheusDockerImage, "prometheus-docker-image", "", "The name of the prometheus docker image to use.")
	var prometheusDockerTag string
	flag.StringVar(&prometheusDockerTag, "prometheus-docker-tag", "", "The prometheus version to use.")
	// The name of the nodepool to target for Prometheus pods
	// If empty, Prometheus pods do not target any nodepool
	var prometheusNodeSelectorTarget string
	flag.StringVar(&prometheusNodeSelectorTarget, "prometheus-node-selector-target", "", "Node-selector label target pool for Prometheus pods. If empty, no node-selector gets added.")
	// Name of the namespace to hold Prometheus pods
	var promNamespace string
	flag.StringVar(&promNamespace, "prometheus-namespace", "platform-services", "Namespace in which to create prometheus instances")
	// The service account to be used by the Prometheus instances, which provides the necessary permissions to scrape workloads
	var prometheusServiceAccountName string
	flag.StringVar(&prometheusServiceAccountName, "prometheus-service-account-name", "prometheus-tenant", "Service account to use for Prometheus")
	// The pod priority class to use for Prometheus instances
	var prometheusPodPriorityClassName string
	flag.StringVar(&prometheusPodPriorityClassName, "prometheus-pod-priority-classname", "", "Prometheus pod priority class name to use for Prometheus pods")
	var prometheusMonitoringTargetName string
	flag.StringVar(&prometheusMonitoringTargetName, "prometheus-monitoring-target-name", "", "Name of the secret to store the monitoring target and extra scraping configuration")
	var prometheusExtraExternalLabels string
	flag.StringVar(&prometheusExtraExternalLabels, "prometheus-extra-external-labels", "", "Extra external labels to be added to the prometheus configuration. Format: key1:value1,key2:value2")

	// #############################################

	// ############## Logs configuration settings ##############
	// Namespace to locate the ConfigMap with FluentD's configuration
	var fluentdLokiConfigMapNamespace string
	flag.StringVar(&fluentdLokiConfigMapNamespace, "logs-fluentd-loki-configmap-namespace", "", "The namespace of the fluentd-loki configmap.")
	// Name of the ConfigMap containing FluentD's configuration
	var fluentdLokiConfigMapName string
	flag.StringVar(&fluentdLokiConfigMapName, "logs-fluentd-loki-configmap-name", "", "The name of the fluentd-loki configmap.")
	// Key used in the ConfigMap with FluentD's configuration that contains the configuration
	var fluentdLokiConfigMapKey string
	flag.StringVar(&fluentdLokiConfigMapKey, "logs-fluentd-loki-configmap-key", "", "The data key of the fluentd-loki configmap where the fluentd rules will be injected.")
	// #############################################

	// ############## Traces configuration settings ##############
	// Name of the namespace to hold Alloy collectors
	var tracesNamespace string
	flag.StringVar(&tracesNamespace, "traces-namespace", "observability", "Namespace in which to create traces collector (alloy) instances")
	// #############################################

	// END of CLI options
	flag.Parse()

	// ############## Parse extra external labels for the Prometheus instance ##############
	prometheusExtraExternalLabelsMap := make(map[string]string)
	if prometheusExtraExternalLabels != "" {
		labels := strings.Split(prometheusExtraExternalLabels, ",")
		for _, label := range labels {
			kv := strings.Split(label, ":")
			if len(kv) != 2 {
				fmt.Println("invalid extra external label format")
				flag.Usage()
				os.Exit(1)
			}
			prometheusExtraExternalLabelsMap[kv[0]] = kv[1]
		}
	}
	// #############################################

	// ############## Initialize label names for configuration ##############
	grafanacloud.Config = grafanacloud.NewConfig(clusterDomain)
	controllers.Config = controllers.NewConfig(clusterDomain)
	// #############################################

	// ############## Verify configuration is valid ##############
	grafanaCloudToken, tokenPresent := os.LookupEnv("GRAFANA_CLOUD_TOKEN")
	if !tokenPresent {
		fmt.Println("missing GRAFANA_CLOUD_TOKEN env variable")
		os.Exit(1)
	}

	grafanaCloudTracesToken, tokenPresent := os.LookupEnv("GRAFANA_CLOUD_TRACES_TOKEN")
	if !tokenPresent {
		fmt.Println("missing GRAFANA_CLOUD_TRACES_TOKEN env variable")
		flag.Usage()
		os.Exit(1)
	}

	if clusterName == missing {
		fmt.Println("missing cluster name")
		flag.Usage()
		os.Exit(1)
	}
	if clusterRegion == missing {
		fmt.Println("missing cluster region")
		flag.Usage()
		os.Exit(1)
	}
	// #############################################

	// ############## Set up logging facilities ##############
	log := logrus.New()
	log.SetFormatter(&logrus.JSONFormatter{})
	log.SetLevel(logrus.InfoLevel)
	ctrl.SetLogger(advlog.NewLogr(log))
	setupLog := ctrl.Log.WithName("setup")
	// #############################################

	// ############## Set up the Manager ##############
	scheme := controllers.NewScheme()
	cfg := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "8cba130d.prometheus." + clusterDomain,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&networkingv1.NetworkPolicy{}: {
					Namespaces: map[string]cache.Config{
						tracesNamespace: {},
					},
				},
			},
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}
	// #############################################

	// ############## Verify we can connect to GrafanaCloud ##############
	config := gcom.NewConfiguration()
	config.AddDefaultHeader("Authorization", "Bearer "+grafanaCloudToken)
	config.Host = "grafana.com"
	config.Scheme = "https"

	gcomClient := gcom.NewAPIClient(config)
	var gcClient grafanaCloudClient
	gcClient = grafanacloud.NewClient(ctrl.Log.WithName("controllers").WithName("Grafana Cloud Client (non-cached)"), gcomClient, grafanaCloudOrgSlug)

	if grafanaCloudClientCache {
		gcClient = grafanacloud.NewCachedClient(ctrl.Log.WithName("controllers").WithName("Grafana Cloud Client (cached)"), gcClient.(*grafanacloud.Client))
	}

	// #############################################

	path := validatingfield.NewPath("metadata", "labels")
	appSelector, err := apilabels.Parse(workloadLabelSelector, validatingfield.WithPath(path))
	if err != nil {
		setupLog.Error(err, "unable to parse application selector", workloadLabelSelector)
		os.Exit(1)
	}
	namespaceSelector, err := apilabels.Parse(namespaceLabelSelector, validatingfield.WithPath(path))
	if err != nil {
		setupLog.Error(err, "unable to parse namespace selector", namespaceSelector)
		os.Exit(1)
	}

	// ############## Set up Pod Reconciler ##############
	if err = (&controllers.PodReconciler{
		Client:                        mgr.GetClient(),
		Log:                           ctrl.Log.WithName("controllers").WithName("Deployment"),
		Scheme:                        mgr.GetScheme(),
		ExcludeWorkloadLabelSelector:  appSelector,
		ExcludeNamespaceLabelSelector: namespaceSelector,
		IgnoreApps:                    strings.Split(ignoreApps, ","),
		IgnoreNamespaces:              strings.Split(ignoreNamespaces, ","),
		TracesNamespace:               tracesNamespace,
		GrafanaCloudClient:            gcClient,
		GrafanaCloudTracesToken:       grafanaCloudTracesToken,
		ClusterName:                   clusterName,
		EnableVPA:                     enableVpa,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Deployment")
		os.Exit(1)
	}
	// #############################################

	// ############## Set up GrafanaStackReconciler ##############
	// we need a client that can manage resources only in a given namespace
	// by default, clients generated by cluster-controller manager are generated
	// for cluster scope controllers are using cluster-wide caches.
	// In the case of the loki configuration, we want are restricting the role to manage a single resource.
	// Hence we create a dedicated client for it.
	// TODO: Consider using the general client now that we restrict the cache for specific objects
	namespacedClient, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create uncached client", "controller", "Grafana Cloud Config Updater")
		os.Exit(1)
	}

	gcConfigUpdater := &grafanacloud.GrafanaCloudConfigUpdater{
		Log:                ctrl.Log.WithName("controllers").WithName("Grafana Cloud Config Updater"),
		Client:             namespacedClient,
		GrafanaCloudClient: gcClient,
		ClusterName:        clusterName,
		ClusterRegion:      clusterRegion,
		ConfigMapNamespace: fluentdLokiConfigMapNamespace,
		ConfigMapName:      fluentdLokiConfigMapName,
		ConfigMapLokiKey:   fluentdLokiConfigMapKey,
	}

	go gcConfigUpdater.Start(workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](time.Millisecond, 30*time.Second)))

	grafanaStackChangeEvents := make(chan event.GenericEvent, 10)
	stackReconcilePeriod, err := time.ParseDuration(grafanaCloudStackReconcilePeriod)
	if err != nil {
		setupLog.Error(err, "failed to parse grafanacloud reconciliation period. Using default %v ", defaultGrafanaCloudStackReconcilePeriod)
		stackReconcilePeriod = defaultGrafanaCloudStackReconcilePeriod
	}

	tick := time.NewTicker(stackReconcilePeriod)

	if err = (&grafanacloud.GrafanaStackReconciler{
		Log:                      ctrl.Log.WithName("controllers").WithName("GrafanaStack"),
		GrafanaCloudClient:       gcClient,
		GrafanaStackChangeEvents: grafanaStackChangeEvents,
	}).WatchGrafanaStacksChange(tick.C); err != nil {
		setupLog.Error(err, "unable to create grafana stacks reconciler", "controller", "GrafanaStacks")
		os.Exit(1)
	}
	// #############################################

	// ############## Set up Namespace Reconciler ##############
	if err = (&controllers.NamespaceReconciler{
		Client:                        mgr.GetClient(),
		Log:                           ctrl.Log.WithName("controllers").WithName("Namespace"),
		TracesNamespace:               tracesNamespace,
		ExcludeWorkloadLabelSelector:  appSelector,
		ExcludeNamespaceLabelSelector: namespaceSelector,
		IgnoreApps:                    strings.Split(ignoreApps, ","),
		IgnoreNamespaces:              strings.Split(ignoreNamespaces, ","),
		GrafanaCloudUpdater:           gcConfigUpdater,
		GrafanaCloudClient:            gcClient,
		GrafanaCloudTracesToken:       grafanaCloudTracesToken,
		ClusterName:                   clusterName,
		EnableVPA:                     enableVpa,
	}).SetupWithManager(mgr, grafanaStackChangeEvents); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Namespace")
		os.Exit(1)
	}
	// #############################################

	// ############## Set up PodMonitor Reconciler ##############
	if err = (&controllers.PodMonitorReconciler{
		Client:                   mgr.GetClient(),
		Log:                      ctrl.Log.WithName("controllers").WithName("PodMonitor"),
		Scheme:                   mgr.GetScheme(),
		ClusterName:              clusterName,
		Region:                   clusterRegion,
		GrafanaCloudCredentials:  grafanaCloudCredentials,
		GrafanaCloudClient:       gcClient,
		PrometheusNamespace:      promNamespace,
		PrometheusExposedDomain:  fmt.Sprintf("%s.%s", clusterName, clusterDomain),
		NodeSelectorTarget:       prometheusNodeSelectorTarget,
		EnableMetricsRemoteWrite: enableMetricsRemoteWrite,
		EnableVpa:                enableVpa,
		PrometheusDockerImage: controllers.DockerImage{
			Name: prometheusDockerImage,
			Tag:  prometheusDockerTag,
		},
		PrometheusServiceAccountName:   prometheusServiceAccountName,
		PrometheusPodPriorityClassName: prometheusPodPriorityClassName,
		PrometheusMonitoringTarget:     prometheusMonitoringTargetName,
		PrometheusExtraExternalLabels:  prometheusExtraExternalLabelsMap,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PodMonitor")
		os.Exit(1)
	}
	// #############################################

	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
