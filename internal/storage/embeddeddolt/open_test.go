//go:build cgo

package embeddeddolt

import (
	"strings"
	"testing"
)

func TestBuildDSN_SpacesInPath(t *testing.T) {
	// Regression test for #2920: paths with spaces must not be
	// percent-encoded (%20) because the Dolt driver's ParseDataSource
	// uses the path as a literal filesystem path.
	dir := "/Users/bbrenner/Documents/Scripting Projects/beads/.beads/embeddeddolt"
	dsn := buildDSN(dir, "beads")

	if !strings.HasPrefix(dsn, "file://") {
		t.Fatalf("DSN should start with file://, got %q", dsn)
	}

	if strings.Contains(dsn, "%20") {
		t.Errorf("DSN must not percent-encode spaces; got %q", dsn)
	}

	if !strings.Contains(dsn, "Scripting Projects") {
		t.Errorf("DSN must preserve literal spaces in path; got %q", dsn)
	}

	// Verify the path portion is between "file://" and "?"
	afterScheme := strings.TrimPrefix(dsn, "file://")
	qIdx := strings.Index(afterScheme, "?")
	if qIdx == -1 {
		t.Fatalf("DSN missing query parameters: %q", dsn)
	}
	pathPortion := afterScheme[:qIdx]
	if pathPortion != dir {
		t.Errorf("path portion = %q, want %q", pathPortion, dir)
	}
}

func TestBuildDSN_NoDatabase(t *testing.T) {
	dsn := buildDSN("/tmp/test", "")
	if strings.Contains(dsn, "database=") {
		t.Errorf("DSN should not contain database param when empty; got %q", dsn)
	}
}

func TestBuildDSN_WithDatabase(t *testing.T) {
	dsn := buildDSN("/tmp/test", "mydb")
	if !strings.Contains(dsn, "database=mydb") {
		t.Errorf("DSN should contain database=mydb; got %q", dsn)
	}
}
