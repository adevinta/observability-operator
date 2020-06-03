package test_helpers

import (
	"bytes"
	"io"
	"net/http"
)

type RoundTripFunc func(req *http.Request) *http.Response

func (f RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}
func newTestClient(fn RoundTripFunc) *http.Client {
	return &http.Client{
		Transport: RoundTripFunc(fn),
	}
}

func NewHttpMockWithResponse(response string, statusCode int) *http.Client {
	return newTestClient(func(req *http.Request) *http.Response {
		return &http.Response{
			StatusCode: statusCode,
			Body:       io.NopCloser(bytes.NewBufferString(response)),
			Header:     make(http.Header),
		}
	})
}

func NewHttpMockWithFunc(fn func(req *http.Request) *http.Response) *http.Client {
	return newTestClient(fn)
}
