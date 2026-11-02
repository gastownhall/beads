package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestPermissionProbeAvoidsSymlink plants an entry at the old fixed probe
// name: neither permission check may write through it, replace it or delete
// it, and each must clean up its own probe.
func TestPermissionProbeAvoidsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	for _, shared := range []bool{false, true} {
		for _, planted := range []string{"symlink", "dangling", "file"} {
			name := map[bool]string{false: "standalone", true: "shared"}[shared] + "/" + planted
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				beadsDir := filepath.Join(dir, ".beads")
				if err := os.Mkdir(beadsDir, 0o755); err != nil {
					t.Fatal(err)
				}
				sentinel := filepath.Join(dir, "sentinel")
				entry := filepath.Join(beadsDir, ".doctor-test-write")
				switch planted {
				case "symlink", "dangling":
					if planted == "symlink" {
						if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Symlink(sentinel, entry); err != nil {
						t.Fatal(err)
					}
				case "file":
					if err := os.WriteFile(entry, []byte("user data"), 0o600); err != nil {
						t.Fatal(err)
					}
				}

				var check DoctorCheck
				if shared {
					check = CheckPermissionsWithStore(dir, &SharedStore{beadsDir: beadsDir})
				} else {
					check = CheckPermissions(dir)
				}
				if check.Status != StatusOK {
					t.Errorf("permissions: %+v", check)
				}

				switch planted {
				case "symlink":
					data, err := os.ReadFile(sentinel)
					if err != nil || string(data) != "sentinel" {
						t.Errorf("sentinel changed: %q, %v", data, err)
					}
				case "dangling":
					if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
						t.Errorf("dangling target created: %v", err)
					}
				case "file":
					data, err := os.ReadFile(entry)
					if err != nil || string(data) != "user data" {
						t.Errorf("planted file changed: %q, %v", data, err)
					}
				}
				if planted != "file" {
					if info, err := os.Lstat(entry); err != nil || info.Mode()&os.ModeSymlink == 0 {
						t.Errorf("planted link changed: %v, %v", info, err)
					}
				}
				entries, err := os.ReadDir(beadsDir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 {
					t.Errorf("probe left entries behind: %v", entries)
				}
			})
		}
	}
}

func TestPermissionProbeCleansUp(t *testing.T) {
	dir := t.TempDir()
	if err := probeDirectoryWrite(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left temporary files: %v", entries)
	}
	if err := probeDirectoryWrite(filepath.Join(dir, "missing")); err == nil {
		t.Error("accepted missing directory")
	}
}
