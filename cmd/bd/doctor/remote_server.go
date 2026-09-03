package doctor

import (
	"github.com/steveyegge/beads/internal/configfile"
)

// IsRemoteServerMode returns true if the beads directory at the given path is
// configured to connect to a remote (non-localhost) Dolt server. When true,
// doctor checks that access the local .dolt/ directory should be skipped since
// there is no local Dolt data directory — the data lives on the remote server.
func IsRemoteServerMode(repoPath string) bool {
	beadsDir := ResolveBeadsDirForRepo(repoPath)
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return false
	}
	return isRemoteHost(cfg.DoltServerHost)
}

// isRemoteHost returns true if the given host string refers to a remote machine
// (i.e., not localhost).
func isRemoteHost(host string) bool {
	switch host {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return false
	}
	return true
}

// remoteServerSkipMessage is the standard message for checks skipped in
// remote server mode.
const remoteServerSkipMessage = "skipped: remote server mode"

// SkipForRemoteServer returns a DoctorCheck with StatusSkip for checks that
// are not applicable in remote server mode.
func SkipForRemoteServer(name, category string) DoctorCheck {
	return DoctorCheck{
		Name:     name,
		Status:   StatusSkip,
		Message:  remoteServerSkipMessage,
		Category: category,
	}
}
