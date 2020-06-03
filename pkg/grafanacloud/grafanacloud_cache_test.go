package grafanacloud

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	gcom "github.com/grafana/grafana-com-public-clients/go/gcom"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	bodyFound = `{"items":[{"hlInstanceId":123,"hmInstancePromId":456,"hmInstancePromUrl":"http://prom-prod.test","hlInstanceUrl":"http://logs-prod.test","id":789,"slug":"dummystack","url":"http://stack-prod.test"}]}`
)

type httpTransportSpy struct {
	Calls        int
	ResponseFunc func(*http.Request) (*http.Response, error)
}

func (c *httpTransportSpy) RoundTrip(req *http.Request) (*http.Response, error) {
	c.Calls++
	return c.ResponseFunc(req)
}

func defaultResponseFunc(statusCode int, body string) func(req *http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		resp := http.Response{
			StatusCode: statusCode,
			Header: http.Header{
				"Content-Type":   []string{"application/json"},
				"Content-Length": []string{fmt.Sprintf("%d", len(body))},
			},
			Body: io.NopCloser(bytes.NewReader([]byte(body))),
		}
		return &resp, nil
	}
}

func mockedGCOMClient(client *http.Client) *gcom.APIClient {
	config := gcom.NewConfiguration()
	config.Host = "grafana.com"
	config.Scheme = "https"
	config.HTTPClient = client

	return gcom.NewAPIClient(config)
}

func TestGetStackUsesCache(t *testing.T) {
	t.Run("when asking for the same stack twice, only one http call is made", func(t *testing.T) {
		transportSpy := &httpTransportSpy{
			ResponseFunc: defaultResponseFunc(http.StatusOK, bodyFound),
		}

		httpClient := &http.Client{
			Transport: transportSpy,
		}

		grafanaClient := NewCachedClient(logr.Logger{}, NewClient(
			logr.Logger{},
			mockedGCOMClient(httpClient),
			"test-org",
		))

		_, err := grafanaClient.GetStack("dummystack")
		require.NoError(t, err)

		_, err = grafanaClient.GetStack("dummystack")
		require.NoError(t, err)

		assert.Equal(t, 1, transportSpy.Calls)
	})

	t.Run("when asking for non-existing stacks, all trigger an http call", func(t *testing.T) {
		transportSpy := &httpTransportSpy{
			ResponseFunc: defaultResponseFunc(http.StatusNotFound, `{"message":"Stack not found"}`),
		}

		httpClient := &http.Client{
			Transport: transportSpy,
		}

		grafanaClient := NewCachedClient(logr.Logger{}, NewClient(
			logr.Logger{},
			mockedGCOMClient(httpClient),
			"test-org",
		))

		_, err := grafanaClient.GetStack("nonexistingstack")
		require.Error(t, err)

		_, err = grafanaClient.GetStack("neitherdoesthisone")
		require.Error(t, err)

		assert.Equal(t, 2, transportSpy.Calls)
	})
}

func TestGetStackCacheStaleness(t *testing.T) {
	t.Run("when the cache entry is not stale, we do not trigger any http call", func(t *testing.T) {
		transportSpy := &httpTransportSpy{
			ResponseFunc: defaultResponseFunc(http.StatusOK, bodyFound),
		}

		httpClient := &http.Client{
			Transport: transportSpy,
		}

		grafanaClient := NewCachedClient(logr.Logger{}, NewClient(
			logr.Logger{},
			mockedGCOMClient(httpClient),
			"test-org",
		))

		grafanaClient.cache.timestamp = time.Now().Unix()

		cacheContents := make(map[string]CacheEntry)
		cacheContents["dummystack"] = CacheEntry{
			stack:     &Stack{},
			timestamp: time.Now().Unix(),
		}

		grafanaClient.cache.data = cacheContents

		_, err := grafanaClient.GetStack("dummystack")

		require.NoError(t, err)

		assert.Equal(t, 0, transportSpy.Calls)
	})
	t.Run("when the cache entry is stale, we trigger and http call", func(t *testing.T) {
		transportSpy := &httpTransportSpy{
			ResponseFunc: defaultResponseFunc(http.StatusOK, bodyFound),
		}

		httpClient := &http.Client{
			Transport: transportSpy,
		}

		grafanaClient := NewCachedClient(logr.Logger{}, NewClient(
			logr.Logger{},
			mockedGCOMClient(httpClient),
			"test-org",
		))

		cacheContents := make(map[string]CacheEntry)
		cacheContents["dummystack"] = CacheEntry{
			stack:     &Stack{},
			timestamp: time.Now().Add(-time.Hour).Unix(),
		}

		grafanaClient.cache.data = cacheContents

		_, err := grafanaClient.GetStack("dummystack")
		require.NoError(t, err)

		assert.Equal(t, 1, transportSpy.Calls)
	})
}

func TestConcurrentGetStackCache(t *testing.T) {
	t.Run("concurrent access to the cache is safe", func(t *testing.T) {
		transportSpy := &httpTransportSpy{
			ResponseFunc: defaultResponseFunc(http.StatusOK, bodyFound),
		}

		grafanaClient := NewCachedClient(logr.Logger{}, NewClient(
			logr.Logger{},
			mockedGCOMClient(&http.Client{
				Transport: transportSpy,
			}),
			"test-org",
		))

		var wg sync.WaitGroup

		// We simulate concurrent access to the cache
		for range 100 {
			wg.Add(1)
			go func() {
				_, err := grafanaClient.GetStack("dummystack")
				require.NoError(t, err)
				wg.Done()
			}()
		}
		wg.Wait()

		assert.Equal(t, 1, transportSpy.Calls)
	})
}

func TestListStackCache(t *testing.T) {
	t.Run("Call ListStacks when the cache is empty. Makes one call to the Grafana API, to populate the cache, then uses it to respond the rest of the calls", func(t *testing.T) {
		transportSpy := &httpTransportSpy{
			ResponseFunc: defaultResponseFunc(http.StatusOK, bodyFound),
		}

		httpClient := &http.Client{
			Transport: transportSpy,
		}

		grafanaClient := NewCachedClient(logr.Logger{}, NewClient(
			logr.Logger{},
			mockedGCOMClient(httpClient),
			"test-org",
		))

		_, err := grafanaClient.ListStacks()
		require.NoError(t, err)

		_, err = grafanaClient.ListStacks()
		require.NoError(t, err)

		assert.Equal(t, 1, transportSpy.Calls)
	})

	t.Run("Call ListStacks when the cache is stale. Makes one call to the Grafana API, to populate the cache, then uses it to respond the rest of the calls", func(t *testing.T) {
		transportSpy := &httpTransportSpy{
			ResponseFunc: defaultResponseFunc(http.StatusOK, bodyFound),
		}

		httpClient := &http.Client{
			Transport: transportSpy,
		}

		grafanaClient := NewCachedClient(logr.Logger{}, NewClient(
			logr.Logger{},
			mockedGCOMClient(httpClient),
			"test-org",
		))

		_, err := grafanaClient.ListStacks()
		require.NoError(t, err)

		grafanaClient.cache.timestamp = 0

		_, err = grafanaClient.ListStacks()
		require.NoError(t, err)

		assert.Equal(t, 2, transportSpy.Calls)
	})

}
