package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDoRequest_RetryOn429(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(Issue{ID: "1", Key: "PROJ-1"})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "3")
	body, err := c.doRequest(context.Background(), "GET", srv.URL+"/rest/api/3/issue/PROJ-1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&attempts) < 3 {
		t.Errorf("expected at least 3 attempts for 429 retry, got %d", atomic.LoadInt32(&attempts))
	}
	if !strings.Contains(string(body), "PROJ-1") {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestDoRequest_RetryOn503(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"service unavailable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(Issue{ID: "1", Key: "PROJ-1"})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "3")
	body, err := c.doRequest(context.Background(), "GET", srv.URL+"/rest/api/3/issue/PROJ-1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&attempts) < 2 {
		t.Errorf("expected at least 2 attempts for 503 retry, got %d", atomic.LoadInt32(&attempts))
	}
	if !strings.Contains(string(body), "PROJ-1") {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestDoRequest_NoRetryOn400(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"bad request"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "3")
	_, err := c.doRequest(context.Background(), "GET", srv.URL+"/rest/api/3/issue/PROJ-1", nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400: %v", err)
	}
	if !strings.Contains(err.Error(), "bad request") {
		t.Errorf("error should include response body: %v", err)
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("expected exactly 1 attempt for 400, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestDoRequest_MaxRetriesExceeded(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limited"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "3")
	_, err := c.doRequest(context.Background(), "GET", srv.URL+"/rest/api/3/issue/PROJ-1", nil)
	if err == nil {
		t.Fatal("expected error after max retries")
	}
	if !strings.Contains(err.Error(), "max retries") {
		t.Errorf("error should mention max retries: %v", err)
	}
	expectedAttempts := int32(MaxRetries + 1)
	if atomic.LoadInt32(&attempts) != expectedAttempts {
		t.Errorf("expected %d attempts, got %d", expectedAttempts, atomic.LoadInt32(&attempts))
	}
}
