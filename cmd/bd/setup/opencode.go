package setup

import "github.com/steveyegge/beads/internal/templates/agents"

var opencodeIntegration = agentsIntegration{
	name:         "OpenCode",
	setupCommand: "bd setup opencode",
	readHint:     "OpenCode reads AGENTS.md at the start of each session. Restart OpenCode if it is already running.",
	profile:      agents.ProfileFull,
}

var opencodeEnvProvider = defaultAgentsEnv

func InstallOpenCode() error {
	return installOpenCode(opencodeEnvProvider())
}

func installOpenCode(env agentsEnv) error {
	return installAgents(env, opencodeIntegration)
}

func CheckOpenCode() error {
	return checkOpenCode(opencodeEnvProvider())
}

func checkOpenCode(env agentsEnv) error {
	return checkAgents(env, opencodeIntegration)
}

func RemoveOpenCode() error {
	return removeOpenCode(opencodeEnvProvider())
}

func removeOpenCode(env agentsEnv) error {
	return removeAgents(env, opencodeIntegration)
}
