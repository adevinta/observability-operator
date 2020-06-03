package controllers

import (
	promcommonconfig "github.com/prometheus/common/config"
	"github.com/prometheus/common/model"
	promconfig "github.com/prometheus/prometheus/config"

	"github.com/prometheus/prometheus/discovery"
	"github.com/prometheus/prometheus/discovery/targetgroup"
	"github.com/prometheus/prometheus/pkg/relabel"
)

var prometheusLocalScrapper = promconfig.ScrapeConfig{
	JobName:          "prometheus-scraper",
	HonorTimestamps:  true,
	HTTPClientConfig: promcommonconfig.DefaultHTTPClientConfig,
	ServiceDiscoveryConfigs: discovery.Configs{
		discovery.StaticConfig{
			&targetgroup.Group{
				Targets: []model.LabelSet{
					{
						"__address__": "localhost:9090",
					},
				},
			},
		},
	},
}

func newIngressAndClusterScraper() promconfig.ScrapeConfig {
	duration, _ := model.ParseDuration("30s")
	return promconfig.ScrapeConfig{
		HonorLabels:     true,
		HonorTimestamps: true,
		Params: map[string][]string{
			"match[]": {},
		},
		ScrapeInterval:          duration,
		MetricsPath:             "/federate",
		ServiceDiscoveryConfigs: discovery.Configs{
			//&kubernetes.SDConfig{},
		},
		// ServiceDiscoveryConfig: sd_config.ServiceDiscoveryConfig{
		// 	KubernetesSDConfigs: []*kubernetes.SDConfig{},
		// },
		RelabelConfigs: []*relabel.Config{
			{
				Regex: relabel.MustNewRegexp("^.*-(ingress|cluster)-metrics$"),
				SourceLabels: model.LabelNames{
					"__meta_kubernetes_service_label_release",
				},
				Separator: ";",
				Action:    relabel.Keep,
			},
			{
				Regex:       relabel.MustNewRegexp("Node;(.*)"),
				TargetLabel: "node",
				Replacement: "${1}",
				Separator:   ";",
				SourceLabels: model.LabelNames{
					"__meta_kubernetes_endpoint_address_target_kind",
					"__meta_kubernetes_endpoint_address_target_name",
				},
			},
			{
				Regex:       relabel.MustNewRegexp("Pod;(.*)"),
				TargetLabel: "pod",
				Replacement: "${1}",
				Separator:   ";",
				SourceLabels: model.LabelNames{
					"__meta_kubernetes_endpoint_address_target_kind",
					"__meta_kubernetes_endpoint_address_target_name",
				},
			},
			{
				TargetLabel: "namespace",
				SourceLabels: model.LabelNames{
					"__meta_kubernetes_namespace",
				},
			},
			{
				TargetLabel: "service",
				SourceLabels: model.LabelNames{
					"__meta_kubernetes_service_name",
				},
			},
			{
				TargetLabel: "pod",
				SourceLabels: model.LabelNames{
					"__meta_kubernetes_pod_name",
				},
			},
			{
				TargetLabel: "job",
				Replacement: "${1}",
				SourceLabels: model.LabelNames{
					"__meta_kubernetes_service_name",
				},
			},
			{
				Regex:  relabel.MustNewRegexp("pod"),
				Action: relabel.LabelDrop,
			},
			{
				Regex:  relabel.MustNewRegexp("node"),
				Action: relabel.LabelDrop,
			},
			{
				Regex:  relabel.MustNewRegexp("namespace"),
				Action: relabel.LabelDrop,
			},
		},
		MetricRelabelConfigs: []*relabel.Config{
			{
				Regex:  relabel.MustNewRegexp("federate"),
				Action: relabel.LabelDrop,
			},
			{
				Regex:  relabel.MustNewRegexp("__replica__"),
				Action: relabel.LabelDrop,
			},
			{
				Regex:  relabel.MustNewRegexp("^prometheus$"),
				Action: relabel.LabelDrop,
			},
		},
	}
}
