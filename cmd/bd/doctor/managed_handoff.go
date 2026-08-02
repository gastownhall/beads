package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// CheckManagedHandoffPort detects the #3926 split-brain risk where a managed
// city/orchestrator port override differs from an existing standalone port file.
func CheckManagedHandoffPort(repoPath string) DoctorCheck {
	beadsDir := ResolveBeadsDirForRepo(repoPath)

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil || cfg.GetBackend() != configfile.BackendDolt {
		return DoctorCheck{
			Name:     "Managed Handoff Port",
			Status:   StatusOK,
			Message:  "N/A (not using Dolt backend)",
			Category: CategoryRuntime,
		}
	}

	localPort := doltserver.ReadPortFile(beadsDir)
	if localPort == 0 {
		return DoctorCheck{
			Name:     "Managed Handoff Port",
			Status:   StatusOK,
			Message:  "No local standalone port file",
			Category: CategoryRuntime,
		}
	}

	envName, envPort := runtimePortOverride()
	if envPort == 0 {
		return DoctorCheck{
			Name:     "Managed Handoff Port",
			Status:   StatusOK,
			Message:  fmt.Sprintf("local port file points at %d", localPort),
			Category: CategoryRuntime,
		}
	}
	if envPort == localPort {
		return DoctorCheck{
			Name:     "Managed Handoff Port",
			Status:   StatusOK,
			Message:  fmt.Sprintf("%s matches local port file (%d)", envName, localPort),
			Category: CategoryRuntime,
		}
	}

	detail := fmt.Sprintf(
		"%s=%d but %s contains %d. This can mean commands are reading a managed-city database while the standalone store still exists locally.",
		envName,
		envPort,
		filepath.Join(beadsDir, doltserver.PortFileName),
		localPort,
	)
	if gtRoot := strings.TrimSpace(os.Getenv("GT_ROOT")); gtRoot != "" {
		detail += fmt.Sprintf("\nGT_ROOT=%s", gtRoot)
	}

	return DoctorCheck{
		Name:     "Managed Handoff Port",
		Status:   StatusWarning,
		Message:  "managed Dolt port override differs from local standalone port file",
		Detail:   detail,
		Fix:      "Before removing the local port file, export from the standalone store, stop its server, then import into the managed server. See docs/architecture/dolt.md#standalone-to-managed-city-handoff",
		Category: CategoryRuntime,
	}
}

func runtimePortOverride() (string, int) {
	for _, name := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		port, err := strconv.Atoi(raw)
		if err == nil && port > 0 {
			return name, port
		}
	}
	return "", 0
}
