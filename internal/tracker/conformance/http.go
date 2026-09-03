// Package conformance contains reusable tracker adapter test fixtures.
package conformance

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Request records one outbound tracker request.
type Request struct {
	Method string
	URL    *url.URL
	Body   []byte
}

// Response is one deterministic HTTP response. Err simulates transport
// failure; Status and Body are returned otherwise.
type Response struct {
	Status int
	Body   string
	Err    error
}

// HTTPDouble is a deterministic, FIFO HTTP RoundTripper. It records every
// request and returns queued responses, making API mapping tests independent of
// network services.
type HTTPDouble struct {
	mu        sync.Mutex
	requests  []Request
	responses []Response
}

// NewHTTPDouble returns an empty deterministic transport.
func NewHTTPDouble() *HTTPDouble { return &HTTPDouble{} }

// Enqueue adds a response to the FIFO queue.
func (d *HTTPDouble) Enqueue(response Response) {
	d.mu.Lock()
	d.responses = append(d.responses, response)
	d.mu.Unlock()
}

// Requests returns a copy of all observed requests.
func (d *HTTPDouble) Requests() []Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Request, len(d.requests))
	copy(out, d.requests)
	return out
}

// Client returns an HTTP client using this transport.
func (d *HTTPDouble) Client() *http.Client { return &http.Client{Transport: d} }

// RoundTrip implements http.RoundTripper.
func (d *HTTPDouble) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
	}
	d.mu.Lock()
	urlCopy := *req.URL
	d.requests = append(d.requests, Request{Method: req.Method, URL: &urlCopy, Body: body})
	var response Response
	queued := false
	if len(d.responses) > 0 {
		response, d.responses = d.responses[0], d.responses[1:]
		queued = true
	}
	d.mu.Unlock()
	if response.Err != nil {
		return nil, response.Err
	}
	if !queued {
		return nil, errors.New("conformance HTTPDouble: no queued response")
	}
	if response.Status == 0 {
		response.Status = http.StatusOK
	}
	return &http.Response{
		StatusCode: response.Status,
		Status:     fmt.Sprintf("%d", response.Status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(response.Body)),
		Request:    req,
	}, nil
}
