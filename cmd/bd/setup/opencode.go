package setup

import "github.com/steveyegge/beads/internal/templates/agents"

var opencodeIntegration = agentsIntegration{
	name:         "OpenCode",
	setupCommand: "bd setup opencode",
	readHint:     "OpenCode reads AGENTS.md at the start of each session. Restart OpenCode if it is already running.",
	profile:      agents.ProfileFull,
}

var opencodeEnvProvider = defaultAgentsEnv

// InstallOpenCode installs OpenCode integration.
func InstallOpenCode() {
	env := opencodeEnvProvider()
	if err := installOpenCode(env); err != nil {
		setupExit(1)
	}
}

func installOpenCode(env agentsEnv) error {
	return installAgents(env, opencodeIntegration)
}

// CheckOpenCode checks if OpenCode integration is installed.
func CheckOpenCode() {
	env := opencodeEnvProvider()
	if err := checkOpenCode(env); err != nil {
		setupExit(1)
	}
}

func checkOpenCode(env agentsEnv) error {
	return checkAgents(env, opencodeIntegration)
}

// RemoveOpenCode removes OpenCode integration.
func RemoveOpenCode() {
	env := opencodeEnvProvider()
	if err := removeOpenCode(env); err != nil {
		setupExit(1)
	}
}

func removeOpenCode(env agentsEnv) error {
	return removeAgents(env, opencodeIntegration)
}
