package ado

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// itemStatus describes how the mock server should respond for a given work item ID.
type itemStatus int

const (
	itemExists  itemStatus = iota // 200 OK
	itemDeleted                   // 404 Not Found
	itemDenied                    // 403 Forbidden
)

// setupReconcileServer creates a mock ADO server that returns different responses
// per work item ID. For batch requests containing any missing/denied item, the
// entire batch returns 404 (matching real ADO behaviour). Individual requests
// return the exact status for that item.
func setupReconcileServer(t *testing.T, items map[int]itemStatus) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idsParam := r.URL.Query().Get("ids")
		if idsParam == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"missing ids"}`))
			return
		}

		parts := strings.Split(idsParam, ",")
		ids := make([]int, 0, len(parts))
		for _, p := range parts {
			id, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"invalid id"}`))
				return
			}
			ids = append(ids, id)
		}

		// For batch requests (more than 1 ID), if any item is not found
		// or denied, the whole request fails with 404 (ADO batch behaviour).
		if len(ids) > 1 {
			for _, id := range ids {
				st, ok := items[id]
				if !ok || st != itemExists {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"message":"VS800075: One or more work items do not exist"}`))
					return
				}
			}
		}

		// Single-item request or batch where all exist.
		var result []WorkItem
		for _, id := range ids {
			st := items[id]
			if len(ids) == 1 {
				switch st {
				case itemDeleted:
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"message":"work item not found"}`))
					return
				case itemDenied:
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"message":"access denied"}`))
					return
				}
			}
			result = append(result, WorkItem{
				ID:     id,
				Fields: map[string]interface{}{FieldTitle: "Item " + strconv.Itoa(id)},
			})
		}

		resp := struct {
			Count int        `json:"count"`
			Value []WorkItem `json:"value"`
		}{Count: len(result), Value: result}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)

	client, err := NewClient(NewSecretString("test-pat"), "testorg", "testproject").
		WithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("WithBaseURL error: %v", err)
	}
	client = client.WithHTTPClient(ts.Client())
	return client, ts
}

func TestReconciler_ShouldReconcile(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]string
		want   bool
	}{
		{
			name:   "counter 0 interval 10",
			config: map[string]string{configSyncsSinceReconcile: "0", configReconcileInterval: "10"},
			want:   false,
		},
		{
			name:   "counter 9 interval 10",
			config: map[string]string{configSyncsSinceReconcile: "9", configReconcileInterval: "10"},
			want:   false,
		},
		{
			name:   "counter 10 interval 10",
			config: map[string]string{configSyncsSinceReconcile: "10", configReconcileInterval: "10"},
			want:   true,
		},
		{
			name:   "counter 15 interval 10",
			config: map[string]string{configSyncsSinceReconcile: "15", configReconcileInterval: "10"},
			want:   true,
		},
		{
			name:   "no config uses defaults",
			config: nil,
			want:   false, // counter 0, interval 10
		},
		{
			name:   "custom interval 3 counter 3",
			config: map[string]string{configSyncsSinceReconcile: "3", configReconcileInterval: "3"},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore(tt.config)
			rec := NewReconciler(nil, store)
			got := rec.ShouldReconcile(context.Background())
			if got != tt.want {
				t.Errorf("ShouldReconcile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReconciler_IncrementCounter(t *testing.T) {
	store := newMockStore(nil)
	rec := NewReconciler(nil, store)
	ctx := context.Background()

	if err := rec.IncrementCounter(ctx); err != nil {
		t.Fatalf("IncrementCounter() error: %v", err)
	}

	val, err := store.GetConfig(ctx, configSyncsSinceReconcile)
	if err != nil {
		t.Fatalf("GetConfig() error: %v", err)
	}
	if val != "1" {
		t.Errorf("counter = %q, want %q", val, "1")
	}

	// Increment again.
	if err := rec.IncrementCounter(ctx); err != nil {
		t.Fatalf("IncrementCounter() error: %v", err)
	}
	val, _ = store.GetConfig(ctx, configSyncsSinceReconcile)
	if val != "2" {
		t.Errorf("counter = %q, want %q", val, "2")
	}
}

func TestReconciler_ResetCounter(t *testing.T) {
	store := newMockStore(map[string]string{configSyncsSinceReconcile: "5"})
	rec := NewReconciler(nil, store)
	ctx := context.Background()

	if err := rec.ResetCounter(ctx); err != nil {
		t.Fatalf("ResetCounter() error: %v", err)
	}

	val, err := store.GetConfig(ctx, configSyncsSinceReconcile)
	if err != nil {
		t.Fatalf("GetConfig() error: %v", err)
	}
	if val != "0" {
		t.Errorf("counter = %q, want %q", val, "0")
	}
}

func TestReconciler_Reconcile_AllExist(t *testing.T) {
	items := map[int]itemStatus{1: itemExists, 2: itemExists, 3: itemExists}
	client, _ := setupReconcileServer(t, items)
	store := newMockStore(nil)
	rec := NewReconciler(client, store)

	result, err := rec.Reconcile(context.Background(), []int{1, 2, 3})
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if result.Checked != 3 {
		t.Errorf("Checked = %d, want 3", result.Checked)
	}
	if len(result.Deleted) != 0 {
		t.Errorf("Deleted = %v, want empty", result.Deleted)
	}
	if len(result.Denied) != 0 {
		t.Errorf("Denied = %v, want empty", result.Denied)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %v, want empty", result.Errors)
	}
}

func TestReconciler_Reconcile_SomeDeleted(t *testing.T) {
	items := map[int]itemStatus{
		1: itemExists,
		2: itemDeleted,
		3: itemExists,
		4: itemDeleted,
	}
	client, _ := setupReconcileServer(t, items)
	store := newMockStore(nil)
	rec := NewReconciler(client, store)

	result, err := rec.Reconcile(context.Background(), []int{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if result.Checked != 4 {
		t.Errorf("Checked = %d, want 4", result.Checked)
	}
	if len(result.Deleted) != 2 {
		t.Errorf("Deleted = %v, want 2 items", result.Deleted)
	}
	// Verify the right IDs were detected.
	deleted := make(map[string]bool)
	for _, id := range result.Deleted {
		deleted[id] = true
	}
	if !deleted["2"] || !deleted["4"] {
		t.Errorf("Deleted = %v, want [2, 4]", result.Deleted)
	}
	if len(result.Denied) != 0 {
		t.Errorf("Denied = %v, want empty", result.Denied)
	}
}

func TestReconciler_Reconcile_SomeDenied(t *testing.T) {
	items := map[int]itemStatus{
		10: itemExists,
		20: itemDenied,
		30: itemExists,
	}
	client, _ := setupReconcileServer(t, items)
	store := newMockStore(nil)
	rec := NewReconciler(client, store)

	result, err := rec.Reconcile(context.Background(), []int{10, 20, 30})
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if len(result.Denied) != 1 || result.Denied[0] != "20" {
		t.Errorf("Denied = %v, want [20]", result.Denied)
	}
	if len(result.Deleted) != 0 {
		t.Errorf("Deleted = %v, want empty", result.Deleted)
	}
}

func TestReconciler_Reconcile_EmptyIDs(t *testing.T) {
	// No server needed — empty input should not make any HTTP calls.
	store := newMockStore(nil)
	rec := NewReconciler(nil, store)

	result, err := rec.Reconcile(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Deleted) != 0 || len(result.Denied) != 0 || len(result.Errors) != 0 {
		t.Error("expected empty result for empty input")
	}
}

func TestReconciler_Reconcile_MixedResults(t *testing.T) {
	items := map[int]itemStatus{
		100: itemExists,
		200: itemDeleted,
		300: itemDenied,
		400: itemExists,
		500: itemDeleted,
	}
	client, _ := setupReconcileServer(t, items)
	store := newMockStore(nil)
	rec := NewReconciler(client, store)

	result, err := rec.Reconcile(context.Background(), []int{100, 200, 300, 400, 500})
	if err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}
	if result.Checked != 5 {
		t.Errorf("Checked = %d, want 5", result.Checked)
	}

	deleted := make(map[string]bool)
	for _, id := range result.Deleted {
		deleted[id] = true
	}
	if len(result.Deleted) != 2 || !deleted["200"] || !deleted["500"] {
		t.Errorf("Deleted = %v, want [200, 500]", result.Deleted)
	}

	denied := make(map[string]bool)
	for _, id := range result.Denied {
		denied[id] = true
	}
	if len(result.Denied) != 1 || !denied["300"] {
		t.Errorf("Denied = %v, want [300]", result.Denied)
	}

	if len(result.Errors) != 0 {
		t.Errorf("Errors = %v, want empty", result.Errors)
	}
}

func TestGetInterval_Default(t *testing.T) {
	store := newMockStore(nil)
	rec := NewReconciler(nil, store)
	got := rec.getInterval(context.Background())
	if got != DefaultReconcileInterval {
		t.Errorf("getInterval() = %d, want %d", got, DefaultReconcileInterval)
	}
}

func TestGetInterval_Custom(t *testing.T) {
	store := newMockStore(map[string]string{configReconcileInterval: "5"})
	rec := NewReconciler(nil, store)
	got := rec.getInterval(context.Background())
	if got != 5 {
		t.Errorf("getInterval() = %d, want 5", got)
	}
}

func TestGetInterval_InvalidFallsBackToDefault(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "negative", value: "-1"},
		{name: "zero", value: "0"},
		{name: "non-numeric", value: "abc"},
		{name: "empty", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore(map[string]string{configReconcileInterval: tt.value})
			rec := NewReconciler(nil, store)
			got := rec.getInterval(context.Background())
			if got != DefaultReconcileInterval {
				t.Errorf("getInterval(%q) = %d, want %d", tt.value, got, DefaultReconcileInterval)
			}
		})
	}
}

func TestReconciler_Reconcile_ContextCancelled(t *testing.T) {
	items := map[int]itemStatus{1: itemExists}
	client, _ := setupReconcileServer(t, items)
	store := newMockStore(nil)
	rec := NewReconciler(client, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := rec.Reconcile(ctx, []int{1})
	if err == nil {
		t.Fatal("Reconcile() expected error for cancelled context")
	}
}
