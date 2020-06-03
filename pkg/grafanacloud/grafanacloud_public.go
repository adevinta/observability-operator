package grafanacloud

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-logr/logr"
	gcom "github.com/grafana/grafana-com-public-clients/go/gcom"
)

var Config *config = NewConfig("adevinta.com")

type config struct {
	// Destination GrafanaCloud stack selection
	stackNameAnnotationKey string
	// Namespace feature toggles
	logsLabelKey string
}

func NewConfig(domain string) *config {
	return &config{
		stackNameAnnotationKey: "grafanacloud." + domain + "/stack-name",
		logsLabelKey:           "grafanacloud." + domain + "/logs",
	}
}

// Stack contains all the relevant details of a GrafanaCloud stack
type Stack struct {
	LogsInstanceID    int    `json:"hlInstanceId"`
	MetricsInstanceID int    `json:"hmInstancePromId"`
	PromURL           string `json:"hmInstancePromUrl"`
	LogsURL           string `json:"hlInstanceUrl"`
	StackID           int    `json:"id"`
	Slug              string `json:"slug" yaml:"slug"`
	URL               string `json:"url" yaml:"url"`
}

type Stacks []Stack

type Client struct {
	GComClient *gcom.APIClient
	Log        logr.Logger
	OrgSlug    string
}

func NewClient(logr logr.Logger, gcomClient *gcom.APIClient, org string) *Client {
	return &Client{
		GComClient: gcomClient,
		Log:        logr,
		OrgSlug:    org,
	}
}

// GetStack returns a stack definition for the corresponding GrafanaCloud stack
func (c *Client) GetStack(slug string) (*Stack, error) {
	resp, httpResp, err := c.GComClient.InstancesAPI.GetInstances(context.Background()).OrgSlug(c.OrgSlug).Slug(slug).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to get stack %s: %s", slug, err.Error())
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected return code")
	}

	if len(resp.GetItems()) == 0 {
		err := fmt.Errorf("stack not found: %s", slug)
		return nil, err
	}

	stack := resp.GetItems()[0]
	s := &Stack{
		LogsInstanceID:    int(stack.HlInstanceId),
		MetricsInstanceID: int(stack.HmInstancePromId),
		PromURL:           stack.HmInstancePromUrl,
		LogsURL:           stack.HlInstanceUrl,
		StackID:           int(stack.Id),
		Slug:              stack.Slug,
		URL:               stack.Url,
	}

	return s, nil
}

func (c *Client) GetTracesConnection(stackSlug string) (int, string, error) {
	stack, err := c.GetStack(stackSlug)
	if err != nil {
		return -1, "", err
	}
	resp, httpResp, err := c.GComClient.InstancesAPI.GetConnections(context.Background(), strconv.Itoa(stack.StackID)).Execute()
	if err != nil {
		return -1, "", err
	}
	if httpResp.StatusCode != http.StatusOK {
		return -1, "Cannot retrieve the OTLP connection: ", fmt.Errorf("unexpected return code")
	}
	otlpURL := resp.OtlpHttpUrl
	if !otlpURL.IsSet() || otlpURL.Get() == nil {
		return -1, "", fmt.Errorf("OTLP URL is not set")
	}
	return stack.StackID, *otlpURL.Get(), nil
}

func (c *Client) ListStacks() (Stacks, error) {
	resp, httpResp, err := c.GComClient.InstancesAPI.GetInstances(context.Background()).OrgSlug(c.OrgSlug).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to get stacks: %s", err.Error())
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected return code")
	}

	stacks := []Stack{}
	for _, stack := range resp.Items {
		stacks = append(
			stacks,
			Stack{
				LogsInstanceID:    int(stack.HlInstanceId),
				MetricsInstanceID: int(stack.HmInstancePromId),
				PromURL:           stack.HmInstancePromUrl,
				LogsURL:           stack.HlInstanceUrl,
				StackID:           int(stack.Id),
				Slug:              stack.Slug,
				URL:               stack.Url,
			},
		)
	}

	return stacks, nil
}
