package doctor

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
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

func startCPUProfile(dir, base string) (*os.File, error) {
	// CreateTemp: exclusive creation under a name neither predictable nor
	// pre-placeable, in the requested directory. A second-resolution name
	// with os.Create followed a committed symlink and collided with any
	// preexisting entry (bd-1tmym4 / #7888).
	f, err := os.CreateTemp(dir, base+"-*.prof")
	if err != nil {
		return nil, err
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name()) // clean up the failed start's temp file
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
