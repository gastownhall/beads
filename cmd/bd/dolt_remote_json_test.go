package main

import (
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestFormatDoltRemoteListJSONPreservesCompatibilityShape(t *testing.T) {
	remotes := []storage.RemoteInfo{
		{Name: "origin", URL: "dolthub://example/beads"},
	}

	payload, err := json.Marshal(formatDoltRemoteListJSON(remotes))
	if err != nil {
		t.Fatalf("marshal remote list JSON: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal remote list JSON: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	entry := got[0]
	if entry["name"] != "origin" {
		t.Fatalf("name = %v, want origin", entry["name"])
	}
	if entry["url"] != "dolthub://example/beads" {
		t.Fatalf("url = %v, want remote URL", entry["url"])
	}
	if entry["sql_url"] != "dolthub://example/beads" {
		t.Fatalf("sql_url = %v, want remote URL", entry["sql_url"])
	}
	if entry["status"] != "ok" {
		t.Fatalf("status = %v, want ok", entry["status"])
	}
	if _, ok := entry["Name"]; ok {
		t.Fatal("remote list JSON should not expose capitalized Go field Name")
	}
	if _, ok := entry["URL"]; ok {
		t.Fatal("remote list JSON should not expose capitalized Go field URL")
	}
}
