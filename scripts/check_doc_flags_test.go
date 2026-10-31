package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckDocFlagsVersionBannerQuotesConfiguredBinary(t *testing.T) {
	bash := requireHostTool(t, "bash")
	script := filepath.Join(sourceRepoRoot(t), "scripts", "check-doc-flags.sh")

	for _, test := range []struct {
		name        string
		binaryDir   string
		versionExit int
		wantBanner  string
	}{
		{name: "ordinary path", binaryDir: "bin", wantBanner: "Using: bd fixture 9.9"},
		{name: "path with spaces", binaryDir: "bin with spaces", wantBanner: "Using: bd fixture 9.9"},
		{name: "version failure falls back to path", binaryDir: "failed version", versionExit: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), test.binaryDir, "bd")
			if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
				t.Fatalf("mkdir fake binary directory: %v", err)
			}
			fixture := "#!/bin/sh\n"
			if test.versionExit == 0 {
				fixture += "[ \"${1:-}\" != version ] || { echo 'bd fixture 9.9'; exit 0; }\n"
			} else {
				fixture += "[ \"${1:-}\" != version ] || exit 1\n"
			}
			fixture += "exit 0\n"
			if err := os.WriteFile(binary, []byte(fixture), 0o755); err != nil {
				t.Fatalf("write fake binary: %v", err)
			}

			want := test.wantBanner
			if want == "" {
				want = "Using: " + binary
			}
			command := exec.Command(bash, script, binary)
			command.Env = append(os.Environ(), "BD_DOCS_IGNORE_PIN=1")
			output, _ := command.CombinedOutput()
			if !strings.Contains(string(output), want+"\n") {
				t.Fatalf("version banner missing %q:\n%s", want, output)
			}
		})
	}
}
