package gittraceenv

import (
	"os"

	"runtime"
	"slices"
	"sync"
	"testing"
)

func TestStderrDirected(t *testing.T) {
	absPath := "/tmp/git.trace"
	if runtime.GOOS == "windows" {
		absPath = `C:\temp\git.trace`
	}
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		// Disabled forms: harmless, keep.
		{"GIT_TRACE", "", false},
		{"GIT_TRACE", "0", false},
		{"GIT_TRACE", "false", false},
		{"GIT_TRACE", "FALSE", false},
		// Stderr forms: scrub.
		{"GIT_TRACE", "1", true},
		{"GIT_TRACE", "true", true},
		{"GIT_TRACE", "TRUE", true},
		{"GIT_TRACE", "yes", true},
		{"GIT_TRACE", "on", true},
		// Inherited-fd forms: scrub (fd 2 is stderr; the rest are not ours).
		{"GIT_TRACE", "2", true},
		{"GIT_TRACE", "9", true},
		// File targets: never touch stderr, keep. This is the supported way
		// to trace bd's git remote plumbing.
		{"GIT_TRACE", absPath, false},
		// Socket target: only the GIT_TRACE2 family supports af_unix; on
		// classic GIT_TRACE it is an unknown value that draws a warning ON
		// STDERR per plumbing call, so it is scrubbed there.
		{"GIT_TRACE2", "af_unix:/tmp/trace.sock", false},
		{"GIT_TRACE2_EVENT", "af_unix:stream:/tmp/trace.sock", false},
		{"GIT_TRACE", "af_unix:/tmp/trace.sock", true},
		{"GIT_TRACE_PACKET", "af_unix:/tmp/trace.sock", true},
		// Relative path: git rejects it with a warning ON STDERR, scrub.
		{"GIT_TRACE", "trace.log", true},
		{"GIT_TRACE", "./trace.log", true},
	}
	for _, tt := range tests {
		if got := stderrDirected(tt.name, tt.value); got != tt.want {
			t.Errorf("stderrDirected(%q, %q) = %v, want %v", tt.name, tt.value, got, tt.want)
		}
	}
}

func TestWithScrubbedRemovesAndRestores(t *testing.T) {
	t.Setenv("GIT_TRACE", "1")
	t.Setenv("GIT_TRACE_PACKET", "true")
	t.Setenv("GIT_CURL_VERBOSE", "1")

	absPath := "/tmp/git.trace"
	if runtime.GOOS == "windows" {
		absPath = `C:\temp\git.trace`
	}
	t.Setenv("GIT_TRACE2", absPath) // file target: must survive untouched

	err := WithScrubbed(func() error {
		for _, name := range []string{"GIT_TRACE", "GIT_TRACE_PACKET", "GIT_CURL_VERBOSE"} {
			if v, ok := os.LookupEnv(name); ok {
				t.Errorf("during fn, %s = %q, want unset", name, v)
			}
		}
		if v := os.Getenv("GIT_TRACE2"); v != absPath {
			t.Errorf("during fn, GIT_TRACE2 = %q, want file target %q kept", v, absPath)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithScrubbed: %v", err)
	}

	// Everything restored afterwards.
	for name, want := range map[string]string{
		"GIT_TRACE":        "1",
		"GIT_TRACE_PACKET": "true",
		"GIT_CURL_VERBOSE": "1",
		"GIT_TRACE2":       absPath,
	} {
		if got := os.Getenv(name); got != want {
			t.Errorf("after fn, %s = %q, want %q restored", name, got, want)
		}
	}
}

func TestWithScrubbedNested(t *testing.T) {
	t.Setenv("GIT_TRACE", "1")

	err := WithScrubbed(func() error {
		return WithScrubbed(func() error {
			if v, ok := os.LookupEnv("GIT_TRACE"); ok {
				t.Errorf("nested: GIT_TRACE = %q, want unset", v)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("WithScrubbed: %v", err)
	}
	if got := os.Getenv("GIT_TRACE"); got != "1" {
		t.Errorf("after nested calls, GIT_TRACE = %q, want %q", got, "1")
	}
}

// TestWithScrubbedConcurrent exercises the refcount under overlap: the
// variable must stay unset while ANY caller is inside fn, and be restored
// once the last one leaves.
func TestWithScrubbedConcurrent(t *testing.T) {
	t.Setenv("GIT_TRACE", "1")

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = WithScrubbed(func() error {
				if v, ok := os.LookupEnv("GIT_TRACE"); ok {
					t.Errorf("during overlap, GIT_TRACE = %q, want unset", v)
				}
				return nil
			})
		}()
	}
	wg.Wait()
	if got := os.Getenv("GIT_TRACE"); got != "1" {
		t.Errorf("after overlap, GIT_TRACE = %q, want %q restored", got, "1")
	}
}

func TestScrubEnv(t *testing.T) {
	absPath := "/tmp/git.trace"
	if runtime.GOOS == "windows" {
		absPath = `C:\temp\git.trace`
	}
	in := []string{
		"PATH=/usr/bin",
		"GIT_TRACE=1",
		"GIT_TRACE=" + absPath, // later duplicate with a file target: kept
		"GIT_CURL_VERBOSE=0",   // presence alone enables it: dropped
		"GIT_TRACE2=" + absPath,
		"GIT_TRACE2=af_unix:/tmp/trace.sock", // trace2 socket target: kept
		"GIT_TRACE=af_unix:/tmp/trace.sock",  // unknown value for classic GIT_TRACE: dropped
		"GIT_TRACE_PACKET=true",
		"NOT_GIT_TRACE=1",
	}
	got := ScrubEnv(in)
	want := []string{
		"PATH=/usr/bin",
		"GIT_TRACE=" + absPath,
		"GIT_TRACE2=" + absPath,
		"GIT_TRACE2=af_unix:/tmp/trace.sock",
		"NOT_GIT_TRACE=1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("ScrubEnv() = %q, want %q", got, want)
	}
}

// Windows resolves environment names case-insensitively (both git's getenv
// and Go's os.LookupEnv), so ScrubEnv must match names the same way there —
// `set Git_Trace=1` enables tracing in the child just like GIT_TRACE=1.
func TestScrubEnvWindowsCaseInsensitiveNames(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows env-name semantics")
	}
	got := ScrubEnv([]string{"Git_Trace=1", "git_curl_verbose=1", "PATH=C:\\bin"})
	want := []string{"PATH=C:\\bin"}
	if !slices.Equal(got, want) {
		t.Errorf("ScrubEnv() = %q, want %q", got, want)
	}
}

// Git on Windows treats a leading dir separator as absolute (is_dir_sep), so
// a Git-Bash-style file target like /c/temp/git.trace never touches stderr
// and must be kept, even though filepath.IsAbs rejects it.
func TestStderrDirectedWindowsLeadingSlashIsFileTarget(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	for _, v := range []string{"/c/temp/git.trace", `\temp\git.trace`} {
		if stderrDirected("GIT_TRACE", v) {
			t.Errorf("stderrDirected(GIT_TRACE, %q) = true, want false (git treats it as a file target)", v)
		}
	}
}

// TestVarsCoversKnownTraceVars pins the scrub list: a git version bump that
// adds a new stderr-capable trace variable should extend this deliberately.
func TestVarsCoversKnownTraceVars(t *testing.T) {
	vars := Vars()
	for _, name := range []string{"GIT_TRACE", "GIT_TRACE2", "GIT_CURL_VERBOSE", "GIT_TRACE_PACKET"} {
		if !slices.Contains(vars, name) {
			t.Errorf("Vars() is missing %s", name)
		}
	}
}
