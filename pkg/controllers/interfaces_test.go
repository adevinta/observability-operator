package controllers

import "github.com/adevinta/observability-operator/pkg/grafanacloud"

type mockGrafanaCloudClient struct {
	GetStackFunc            func(string) (*grafanacloud.Stack, error)
	GetTracesConnectionFunc func(string) (int, string, error)
}

func (m *mockGrafanaCloudClient) GetStack(tenant string) (*grafanacloud.Stack, error) {
	return m.GetStackFunc(tenant)
}

func (m *mockGrafanaCloudClient) GetTracesConnection(stack string) (int, string, error) {
	return m.GetTracesConnectionFunc(stack)
}
