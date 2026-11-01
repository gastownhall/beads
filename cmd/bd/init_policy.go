package main

import "github.com/steveyegge/beads/internal/configfile"

// initRemoteCloneDisposition describes how init continues after a remote clone
// attempt. It deliberately owns only the clone result policy; callers retain
// all output, state, and remote-safety decisions.
type initRemoteCloneDisposition uint8

const (
	initRemoteCloneFailed initRemoteCloneDisposition = iota
	initRemoteCloneBootstrapped
	initRemoteCloneFresh
)

// runInitRemoteClone runs one remote clone attempt and classifies the result
// for Init. Empty remotes initialize locally; every other clone error is
// returned unchanged.
func runInitRemoteClone(remoteURL string, clone func(string) error) (initRemoteCloneDisposition, error) {
	if err := clone(remoteURL); err != nil {
		if isEmptyRemoteCloneError(err) {
			return initRemoteCloneFresh, nil
		}
		return initRemoteCloneFailed, err
	}
	return initRemoteCloneBootstrapped, nil
}

// initRemoteHostConflict contains the effective remote host configuration
// that embedded Init rejects when server mode is not enabled.
type initRemoteHostConflict struct {
	host         string
	source       string
	includesPort bool
}

// detectInitRemoteHostConflict applies Init's host-precedence rule. An
// environment host overrides config, and ports are only explanatory once a
// remote effective host has made the configuration incompatible with embedded
// mode.
func detectInitRemoteHostConflict(configHost, envHost, configPort, envPort string) *initRemoteHostConflict {
	effectiveHost := configHost
	hostSource := "config.yaml"
	if envHost != "" {
		effectiveHost = envHost
		hostSource = "environment"
	}
	// Machine locality, as mode inference asks it (HostImpliesServerMode),
	// not ownership: a host of this machine is no server intent.
	if effectiveHost == "" || configfile.IsLocalHostString(effectiveHost) {
		return nil
	}
	return &initRemoteHostConflict{
		host:         effectiveHost,
		source:       hostSource,
		includesPort: configPort != "" || envPort != "",
	}
}
