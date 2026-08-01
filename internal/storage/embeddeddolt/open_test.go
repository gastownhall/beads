//go:build cgo

package embeddeddolt

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConfiguredEmbeddedOpenTimeoutFromEnv(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want time.Duration
	}{
		{name: "empty", env: "", want: defaultEmbeddedOpenTimeout},
		{name: "whitespace", env: " \t", want: defaultEmbeddedOpenTimeout},
		{name: "valid", env: "75ms", want: 75 * time.Millisecond},
		{name: "invalid", env: "not-a-duration", want: defaultEmbeddedOpenTimeout},
		{name: "zero", env: "0s", want: defaultEmbeddedOpenTimeout},
		{name: "negative", env: "-1s", want: defaultEmbeddedOpenTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", tt.env)
			t.Setenv("BEADS_EMBEDDED_LOCK_TIMEOUT", "")
			if got := configuredEmbeddedOpenTimeout(); got != tt.want {
				t.Fatalf("configuredEmbeddedOpenTimeout() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestConfiguredEmbeddedOpenTimeoutLegacyEnv(t *testing.T) {
	t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", "")
	t.Setenv("BEADS_EMBEDDED_LOCK_TIMEOUT", "90ms")

	if got := configuredEmbeddedOpenTimeout(); got != 90*time.Millisecond {
		t.Fatalf("configuredEmbeddedOpenTimeout() = %s, want 90ms", got)
	}
}

func TestEmbeddedOpenTimeoutUsesSoonerParentDeadline(t *testing.T) {
	t.Setenv("BEADS_EMBEDDED_OPEN_TIMEOUT", "5s")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got := embeddedOpenTimeout(ctx)
	if got <= 0 || got > time.Second {
		t.Fatalf("embeddedOpenTimeout() = %s, want positive duration below 1s", got)
	}
}

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
