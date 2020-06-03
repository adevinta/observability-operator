package controllers

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	podMonitorsErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "podmonitor_errors",
			Help: "Number of errors reconciling podmonitor objects",
		}, []string{"name", "namespace"},
	)

	prometheusErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "prometheus_errors",
			Help: "Number of errors reconciling prometheus objects/ingresses/svcs",
		}, []string{"name", "namespace", "kind"},
	)
)

func init() {
	prometheusErrors.WithLabelValues("default", "default", "default").Add(0)
	podMonitorsErrors.WithLabelValues("default", "default").Add(0)
	metrics.Registry.MustRegister(prometheusErrors, podMonitorsErrors)
}
