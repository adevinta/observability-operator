package grafanacloud

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"slices"
	"strings"
	"text/template"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed fluentd_template.conf
var lokiFluentDTemplate string

type GrafanaCloudStackLister interface {
	ListStacks() (Stacks, error)
}

type GrafanaCloudConfigUpdater struct {
	client.Client
	Log                logr.Logger
	GrafanaCloudClient GrafanaCloudStackLister
	ClusterName        string
	ClusterRegion      string
	RateLimiter        RateLimitedFunc

	ConfigMapNamespace string
	ConfigMapName      string
	ConfigMapLokiKey   string
}

type RateLimitedFunc struct {
	Do    func(context.Context)
	Queue workqueue.TypedRateLimitingInterface[string]
}

func (r *RateLimitedFunc) Start() {
	for {
		item, stop := r.Queue.Get()
		if stop {
			return
		}
		r.Do(context.Background())
		r.Queue.Done(item)
	}
}

func (r *RateLimitedFunc) EnsureDone() {
	switch r.Queue.Len() {
	case 0:
		r.Queue.Add("call")
	case 1:
		r.Queue.AddRateLimited("call")
	}
}

func (r *GrafanaCloudConfigUpdater) Start(queue workqueue.TypedRateLimitingInterface[string]) {
	r.RateLimiter = RateLimitedFunc{
		Do:    r.createFluentDConfigmap,
		Queue: queue,
	}
	r.RateLimiter.Start()
}

func (r *GrafanaCloudConfigUpdater) InjectFluentdLokiConfiguration(ctx context.Context) error {
	if r.ConfigMapName == "" && r.ConfigMapNamespace == "" && r.ConfigMapLokiKey == "" {
		return nil
	}
	if r.ConfigMapName == "" {
		return errors.New("missing loki configmap name")
	}
	if r.ConfigMapNamespace == "" {
		return errors.New("missing loki configmap namespace")
	}
	if r.ConfigMapLokiKey == "" {
		return errors.New("missing loki configmap loki key")
	}
	r.RateLimiter.EnsureDone()
	return nil
}

func (r *GrafanaCloudConfigUpdater) createFluentDConfigmap(ctx context.Context) {
	namespaces := corev1.NamespaceList{}
	err := r.Client.List(ctx, &namespaces)
	if err != nil {
		r.Log.Error(err, "failed to list namespaces")
		return
	}

	stacks, err := r.GrafanaCloudClient.ListStacks()
	if err != nil {
		r.Log.Error(err, "failed to list grafanacloud stacks")
		return
	}
	if len(stacks) == 0 {
		r.Log.Info("Grafana API does not return any stack, skipping loki configuration injection")
		return
	}
	r.Log.Info("namespaces and stacks", "namespaceCount", len(namespaces.Items), "stackCount", len(stacks))

	lokiConfigs := map[string][]lokiCredentials{}
	for _, namespace := range namespaces.Items {
		if value, ok := namespace.Labels[Config.logsLabelKey]; ok && value == "disabled" {
			// This namespace does not want logs, move to next one
			r.Log.WithValues("namespace", namespace.GetName()).Info("namespace disabled log routing, skipping")
			continue
		}

		configuredStacks, ok := namespace.Annotations[Config.stackNameAnnotationKey]
		if !ok {
			r.Log.WithValues("namespace", namespace.GetName()).Info("namespace has no configured destination stack, skipping")
			continue
		}

		var stackNames []string
		for _, part := range strings.Split(configuredStacks, ",") {
			stackNames = append(stackNames, strings.TrimSpace(part))
		}

		for _, stack := range stacks {
			if slices.IndexFunc(stackNames, func(name string) bool { return name == stack.Slug }) == -1 {
				// stack is not present in the list of configured stacks for this namespace
				continue
			}
			if lokiConfigs[namespace.Name] != nil {
				// There's more than one destination stack for this namespace
				lokiConfigs[namespace.Name] = append(
					lokiConfigs[namespace.Name],
					lokiCredentials{
						URL:    stack.LogsURL,
						UserID: stack.LogsInstanceID,
					},
				)
			} else {
				// This is the first destination stack we have found for this namespace
				lokiConfigs[namespace.Name] = []lokiCredentials{
					{
						URL:    stack.LogsURL,
						UserID: stack.LogsInstanceID,
					},
				}
			}
		}
	}
	r.Log.Info("found loki configurations", "namespaceCount", len(lokiConfigs))

	tpl := template.New("loki-config")
	tpl, err = tpl.Parse(lokiFluentDTemplate)
	if err != nil {
		r.Log.Error(err, "failed to generate loki configuration")
		return
	}
	b := bytes.Buffer{}
	if err := tpl.Execute(&b, lokiOptions{
		Cluster: clusterDetails{
			Name:   r.ClusterName,
			Region: r.ClusterRegion,
		},
		Stacks: lokiConfigs,
	}); err != nil {
		r.Log.Error(err, "failed to generate loki configuration")
		return
	}
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.ConfigMapNamespace,
			Name:      r.ConfigMapName,
		},
	}
	if _, err := ctrl.CreateOrUpdate(ctx, r.Client, &cm, func() error {
		cm.Data = map[string]string{
			r.ConfigMapLokiKey: b.String(),
		}
		return nil
	}); err != nil {
		r.Log.Error(err, "failed to create loki configuration configmap")
		return
	}
	r.Log.Info("injected loki configuration", "namespaceCount", len(lokiConfigs))
}

type lokiOptions struct {
	Cluster clusterDetails
	Stacks  map[string][]lokiCredentials
}

type clusterDetails struct {
	Name, Region string
}

type lokiCredentials struct {
	URL    string
	UserID int
}
