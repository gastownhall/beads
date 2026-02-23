package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
)

var cpuProfileFile *os.File

// RunPerformanceDiagnostics runs performance diagnostics.
// Delegates to Dolt backend diagnostics.
func RunPerformanceDiagnostics(path string) {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: No .beads/ directory found at %s\n", path)
		fmt.Fprintf(os.Stderr, "Run 'bd init' to initialize beads\n")
		os.Exit(1)
	}

	metrics, err := RunDoltPerformanceDiagnostics(path, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running performance diagnostics: %v\n", err)
		os.Exit(1)
	}
	PrintDoltPerfReport(metrics)
}

// CollectPlatformInfo gathers platform information for diagnostics.
func CollectPlatformInfo(path string) map[string]string {
	info := make(map[string]string)
	info["os_arch"] = fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	info["go_version"] = runtime.Version()

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	if IsDoltBackend(beadsDir) {
		info["backend"] = "dolt"
	} else {
		info["backend"] = "unknown"
	}

	return info
}

func startCPUProfile(path string) error {
	// #nosec G304 -- profile path supplied by CLI flag in trusted environment
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	cpuProfileFile = f
	return pprof.StartCPUProfile(f)
}

// stopCPUProfile stops CPU profiling and closes the profile file.
// Must be called after pprof.StartCPUProfile() to flush profile data to disk.
func stopCPUProfile() {
	pprof.StopCPUProfile()
	if cpuProfileFile != nil {
		_ = cpuProfileFile.Close() // best effort cleanup
	}
}
