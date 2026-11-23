package main

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/config"
)

// macOSLegacyUserConfigGOOS is the platform the macOS legacy user config
// check believes it runs on; tests drive the darwin branch on any host.
var macOSLegacyUserConfigGOOS = runtime.GOOS

// macOSLegacyUserConfigCheck runs the "macOS Legacy User Config" doctor check
// (GH#8113). Off darwin and ios it returns false before reading anything, so
// Linux and Windows doctor output is unchanged. It is host-level and
// store-free: it never opens a workspace.
func macOSLegacyUserConfigCheck() (doctorCheck, bool) {
	goos := macOSLegacyUserConfigGOOS
	if !config.IsMacOSFamily(goos) {
		return doctorCheck{}, false
	}
	home, homeErr := os.UserHomeDir()
	in := doctor.MacOSLegacyUserConfigInputs{
		GOOS:              goos,
		Home:              home,
		HomeErr:           homeErr,
		ActiveConfigPaths: config.UserConfigYamlSearchPaths(),
		MetricsEnabled:    metricsEnabledAfterBootstrap(),
		NodeID:            config.NodeID(),
		Stat:              os.Stat,
	}
	if target, err := config.UserConfigYamlPath(); err == nil {
		in.ConfigTarget = target
	}
	if dir, err := config.UserConfigDir(); err == nil {
		in.PrimeTarget = filepath.Join(dir, "beads", "PRIME.md")
	}
	dc, ok := doctor.CheckMacOSLegacyUserConfig(in)
	if !ok {
		return doctorCheck{}, false
	}
	return convertWithCategory(dc, doctor.CategoryData), true
}

// hostLevelDoctorIssues returns the store-free host-level checks that found
// something, for the embedded-mode doctor stub (GH#8113, GH#3794 sign-off):
// today only a non-OK macOS legacy user config result. Tests replace it.
var hostLevelDoctorIssues = func() []doctorCheck {
	check, ok := macOSLegacyUserConfigCheck()
	if !ok || check.Status == statusOK {
		return nil
	}
	return []doctorCheck{check}
}

// embeddedDoctorHostIssues is what the embedded-mode doctor stub shows above
// its notice: hostLevelDoctorIssues for bare bd doctor only. --deep,
// --server, --migration and --fix ask for something else that embedded mode
// cannot do, so their stub stays as it was (design §2).
func embeddedDoctorHostIssues() []doctorCheck {
	if doctorDeep || doctorServer || doctorMigration != "" || doctorFix {
		return nil
	}
	return hostLevelDoctorIssues()
}
