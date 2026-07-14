package creds

import (
	"context"
	"testing"
)

func TestFileSourceHit(t *testing.T) {
	src := FileSource{Host: "h", Port: 5432, Lookup: func(host string, port int) string {
		if host == "h" && port == 5432 {
			return "pw"
		}
		return ""
	}}
	cred, ok, err := src.Resolve(context.Background())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if cred.Value != "pw" || cred.Kind != KindSecret || cred.Source != "credentials-file" {
		t.Fatalf("unexpected credential: %+v", cred)
	}
}

func TestFileSourceMiss(t *testing.T) {
	src := FileSource{Host: "h", Port: 5432, Lookup: func(string, int) string { return "" }}
	_, ok, err := src.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected not-configured on an empty lookup")
	}
}

func TestFileSourceNilLookup(t *testing.T) {
	if _, ok, _ := (FileSource{Host: "h", Port: 5432}).Resolve(context.Background()); ok {
		t.Fatal("expected not-configured with a nil Lookup")
	}
}

// No valid endpoint (missing host or port) → not configured, and Lookup is never called.
func TestFileSourceNoEndpoint(t *testing.T) {
	called := false
	lk := func(string, int) string { called = true; return "pw" }
	for _, s := range []FileSource{
		{Host: "", Port: 5432, Lookup: lk},
		{Host: "h", Port: 0, Lookup: lk},
	} {
		if _, ok, _ := s.Resolve(context.Background()); ok {
			t.Fatalf("expected not-configured for %+v", s)
		}
	}
	if called {
		t.Fatal("Lookup must not be called without a valid endpoint")
	}
}

func TestFileSourceLabel(t *testing.T) {
	if got := (FileSource{}).Name(); got != "credentials-file" {
		t.Fatalf("default Name() = %q, want credentials-file", got)
	}
	if got := (FileSource{Label: "custom"}).Name(); got != "custom" {
		t.Fatalf("Name() = %q, want custom", got)
	}
}
