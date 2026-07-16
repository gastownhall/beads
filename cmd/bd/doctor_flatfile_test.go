package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// makeFlatfileWorkspace creates a minimal flatfile beads workspace under
// root and returns its path. Issue files are written verbatim.
func makeFlatfileWorkspace(t *testing.T, root string, issueFiles map[string]string) string {
	t.Helper()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"backend":"flatfile","project_id":"test"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range issueFiles {
		if err := os.WriteFile(filepath.Join(beadsDir, "issues", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TASKS-n2hu: 'bd doctor <path>' must diagnose the workspace at <path>, not
// the current working directory. Pre-fix, the flatfile short-circuit always
// ran runDiagnostics(".") — here the cwd workspace is healthy while the
// target has a corrupt issue file, so a misrouted doctor would never report
// the corruption.
func TestDoctorFlatfilePathArgTargetsThatWorkspace(t *testing.T) {
	os.Unsetenv("BEADS_DIR")
	// Executing doctor with a path arg rebinds BEADS_DIR process-wide
	// (prepareSelectedCommandContext never restores it — fine for the real
	// CLI, which exits). Without this cleanup the temp workspace path leaks
	// into every later test in the package: t.Setenv snapshots in
	// unrelated tests re-propagate it, and exec'd bd children inherit it
	// and silently initialize the leaked (deleted) workspace instead of
	// their own.
	t.Cleanup(func() { os.Unsetenv("BEADS_DIR") })

	healthy := makeFlatfileWorkspace(t, t.TempDir(), map[string]string{
		"ok-1.json": `{"id":"ok-1","title":"fine","status":"open"}`,
	})
	broken := makeFlatfileWorkspace(t, t.TempDir(), map[string]string{
		"bad-1.json": `{not json`,
	})

	t.Chdir(healthy)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	rootCmd.SetArgs([]string{"doctor", broken})
	err := rootCmd.Execute()
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	output := buf.String()

	if err == nil {
		t.Errorf("bd doctor <broken workspace> returned nil error; want doctor checks failed")
	}
	if !bytes.Contains([]byte(output), []byte("issues have problems")) {
		t.Errorf("doctor output does not report the target workspace's corrupt issue; diagnosed the wrong workspace?\noutput:\n%s", output)
	}
}
