package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestHandleFreshCloneError_UsesBootstrapFirstGuidance(t *testing.T) {
	err := errors.New("post-migration validation failed: required config key missing: issue_prefix")

	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = w

	handled := handleFreshCloneError(err)
	_ = w.Close()
	os.Stderr = origStderr

	var buf bytes.Buffer
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatal(copyErr)
	}
	_ = r.Close()

	if !handled {
		t.Fatal("expected fresh clone error to be handled")
	}
	msg := buf.String()
	if !strings.Contains(msg, "bd bootstrap") {
		t.Fatalf("expected bootstrap guidance, got:\n%s", msg)
	}
	if strings.Contains(msg, "To initialize a new database: bd init") {
		t.Fatalf("did not expect init-first guidance for fresh clone recovery, got:\n%s", msg)
	}
	if !strings.Contains(msg, "brand-new database from scratch") {
		t.Fatalf("expected brand-new project fallback note, got:\n%s", msg)
	}
}
