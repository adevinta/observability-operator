package controllers

import "github.com/adevinta/observability-operator/pkg/grafanacloud"

type GrafanaCloudClient interface {
	GetStack(tenant string) (*grafanacloud.Stack, error)
	GetTracesConnection(stack string) (int, string, error)
}
