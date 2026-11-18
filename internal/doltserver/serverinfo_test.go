package doltserver_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// TestReadServerInfoParsesDoltServerInfoFile pins the read-only parser bd
// uses to find a server dolt's own info file names (#8489): the pid:port:secret
// form, the "-" socket-only port (reported as -1), and every refusal (missing file, wrong
// shape, non-numeric or non-positive pid) as an error, never a guess.
func TestReadServerInfoParsesDoltServerInfoFile(t *testing.T) {
	dir := t.TempDir()
	infoPath := filepath.Join(dir, ".dolt", doltserver.ServerInfoFileName)
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(infoPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("4242:3306:7f3a\n")
	pid, port, err := doltserver.ReadServerInfo(dir)
	if err != nil || pid != 4242 || port != 3306 {
		t.Fatalf("ReadServerInfo(%q) = %d, %d, %v; want 4242, 3306, nil", dir, pid, port, err)
	}

	write("4242:-:7f3a")
	pid, port, err = doltserver.ReadServerInfo(dir)
	if err != nil || pid != 4242 || port != -1 {
		t.Fatalf("ReadServerInfo(%q) on socket-only server = %d, %d, %v; want 4242, -1, nil", dir, pid, port, err)
	}

	for name, content := range map[string]string{
		"empty":            "",
		"two parts":        "4242:3306",
		"four parts":       "4242:3306:7f3a:extra",
		"non-numeric pid":  "dolt:3306:7f3a",
		"non-numeric port": "4242:http:7f3a",
		"zero pid":         "0:3306:7f3a",
		"negative pid":     "-1:3306:7f3a",
	} {
		write(content)
		if _, _, err := doltserver.ReadServerInfo(dir); err == nil {
			t.Fatalf("ReadServerInfo(%q) with %s content %q succeeded; want error", dir, name, content)
		}
	}

	if err := os.Remove(infoPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := doltserver.ReadServerInfo(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadServerInfo(%q) on missing file = %v; want os.ErrNotExist", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".dolt")); err != nil {
		t.Fatalf("ReadServerInfo must not create or remove anything under .dolt: %v", err)
	}
}
