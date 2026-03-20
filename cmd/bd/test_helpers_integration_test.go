//go:build cgo && integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// initExecTestDB initializes a test database using the bd binary and returns the temp dir.
func initExecTestDB(t *testing.T) string {
	t.Helper()
	tmpDir := createTempDirWithCleanup(t)
	initCmd := exec.Command(testBD, "init", "--prefix", "test", "--quiet")
	initCmd.Dir = tmpDir
	initCmd.Env = os.Environ()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}
	return tmpDir
}

// createExecTestIssue creates a test issue using the bd binary and returns the ID.
func createExecTestIssue(t *testing.T, tmpDir, title string) string {
	t.Helper()
	createCmd := exec.Command(testBD, "create", title, "-p", "1", "--json")
	createCmd.Dir = tmpDir
	createCmd.Env = os.Environ()
	out, err := createCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create failed: %v\n%s", err, out)
	}
	jsonStart := strings.Index(string(out), "{")
	if jsonStart < 0 {
		t.Fatalf("No JSON in create output: %s", out)
	}
	var issue map[string]interface{}
	if err := json.Unmarshal(out[jsonStart:], &issue); err != nil {
		t.Fatalf("parse create JSON: %v\n%s", err, out)
	}
	return issue["id"].(string)
}
