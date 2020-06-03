package grafanacloud_test

import (
	"os"
	"testing"

	"github.com/adevinta/observability-operator/pkg/grafanacloud"
	"github.com/go-logr/logr"
	"github.com/grafana/grafana-com-public-clients/go/gcom"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func defaultGCOMClient(token string) *gcom.APIClient {
	config := gcom.NewConfiguration()
	config.AddDefaultHeader("Authorization", "Bearer "+token)
	config.Host = "grafana.com"
	config.Scheme = "https"

	return gcom.NewAPIClient(config)
}

func TestGetStack(t *testing.T) {
	log := logr.Logger{}

	token, ok := os.LookupEnv("GRAFANA_CLOUD_TOKEN")
	if !ok {
		t.Skip("Missing GRAFANA_CLOUD_TOKEN")
	}

	gcomClient := defaultGCOMClient(token)

	client := grafanacloud.NewClient(log, gcomClient, "adevinta")

	stack, err := client.GetStack("adevintatest")
	require.NoError(t, err)
	assert.Equal(t, 150026, stack.StackID)
	assert.Equal(t, "adevintatest", stack.Slug)
	assert.Equal(t, "https://adevintatest.grafana.net", stack.URL)
	assert.Equal(t, "https://logs-prod-eu-west-0.grafana.net", stack.LogsURL)
	assert.Equal(t, 8302, stack.LogsInstanceID)
	assert.Equal(t, "https://prometheus-prod-01-eu-west-0.grafana.net", stack.PromURL)
	assert.Equal(t, 18630, stack.MetricsInstanceID)
}

func TestListStacks(t *testing.T) {
	log := logr.Logger{}

	token, ok := os.LookupEnv("GRAFANA_CLOUD_TOKEN")
	if !ok {
		t.Skip("Missing GRAFANA_CLOUD_TOKEN")
	}

	gcomClient := defaultGCOMClient(token)

	client := grafanacloud.NewClient(log, gcomClient, "adevinta")

	stacks, err := client.ListStacks()
	require.NoError(t, err)
	assert.Greater(t, len(stacks), 0)
}

func TestGetTracesConnection(t *testing.T) {
	log := logr.Logger{}

	token, ok := os.LookupEnv("GRAFANA_CLOUD_TOKEN")
	if !ok {
		t.Skip("Missing GRAFANA_CLOUD_TOKEN")
	}

	gcomClient := defaultGCOMClient(token)

	client := grafanacloud.NewClient(log, gcomClient, "adevinta")

	num, url, err := client.GetTracesConnection("adevintatest")
	require.NoError(t, err)
	assert.Equal(t, url, "https://otlp-gateway-prod-eu-west-0.grafana.net")
	assert.Equal(t, num, 150026)
}
