package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPermissionProbeAvoidsSymlink(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "shared"}[shared], func(t *testing.T) {
			dir := t.TempDir()
			beadsDir := filepath.Join(dir, ".beads")
			if err := os.Mkdir(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(dir, "sentinel")
			if err := os.WriteFile(sentinel, []byte("sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(beadsDir, ".doctor-test-write")
			if err := os.Symlink(sentinel, link); err != nil {
				t.Fatal(err)
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
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != "sentinel" {
				t.Errorf("sentinel changed: %q, %v", data, err)
			}
			if _, err := os.Lstat(link); err != nil {
				t.Errorf("planted link removed: %v", err)
			}
		})
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

func TestCPUProfileAvoidsSymlink(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "beads-dolt-perf-test.prof")
	if err := os.Symlink(sentinel, path); err != nil {
		t.Fatal(err)
	}
	f, err := startCPUProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	stopCPUProfile(f)
	if f.Name() == path {
		t.Error("profile reused predictable path")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "sentinel" {
		t.Errorf("sentinel changed: %q, %v", data, err)
	}
	if info, err := os.Stat(f.Name()); err != nil || info.Size() == 0 {
		t.Errorf("profile not written: %v", err)
	}
}

func TestCPUProfileCreateErrors(t *testing.T) {
	dir := t.TempDir()
	if f, err := startCPUProfile(filepath.Join(dir, "missing", "profile.prof")); err == nil {
		stopCPUProfile(f)
		t.Fatal("accepted missing parent")
	}
	f, err := startCPUProfile(filepath.Join(dir, "profile.prof"))
	if err != nil {
		t.Fatal(err)
	}
	defer stopCPUProfile(f)
	if other, err := startCPUProfile(filepath.Join(dir, "profile.prof")); err == nil {
		_ = other.Close()
		t.Error("started second CPU profile")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("failed profile left temporary files: %v", entries)
	}
}
