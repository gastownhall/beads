package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
)

// RunPerformanceDiagnostics runs performance diagnostics.
// Delegates to Dolt backend diagnostics.
func RunPerformanceDiagnostics(path string) error {
	beadsDir := ResolveBeadsDirForRepo(path)
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return fmt.Errorf("no .beads/ directory found at %s; run 'bd init' to initialize beads", path)
	}

	metrics, err := RunDoltPerformanceDiagnostics(path, true)
	if err != nil {
		return fmt.Errorf("running performance diagnostics: %w", err)
	}
	PrintDoltPerfReport(metrics)
	return nil
}

// CollectPlatformInfo gathers platform information for diagnostics.
func CollectPlatformInfo(path string) map[string]string {
	info := make(map[string]string)
	info["os_arch"] = fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	info["go_version"] = runtime.Version()

	beadsDir := ResolveBeadsDirForRepo(path)
	if IsDoltBackend(beadsDir) {
		info["backend"] = "dolt"
	} else {
		info["backend"] = "unknown"
	}

	return info
}

// startCPUProfile starts CPU profiling into a new file beside path. The file
// is created exclusively with a random suffix (path "x.prof" becomes
// "x-<random>.prof"), so an entry already at a predictable name, such as a
// planted symlink, is never followed or replaced. Callers report f.Name().
func startCPUProfile(path string) (*os.File, error) {
	pattern := strings.TrimSuffix(filepath.Base(path), ".prof") + "-*.prof"
	f, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return nil, err
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// stopCPUProfile stops CPU profiling and closes the profile file.
// Must be called after pprof.StartCPUProfile() to flush profile data to disk.
func stopCPUProfile(f *os.File) {
	pprof.StopCPUProfile()
	if f != nil {
		_ = f.Close() // best effort cleanup
	}
}
