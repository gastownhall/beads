package setup

import "github.com/steveyegge/beads/internal/templates/agents"

var factoryIntegration = agentsIntegration{
	name:         "Factory.ai (Droid)",
	setupCommand: "bd setup factory",
	readHint:     "Factory Droid will automatically read AGENTS.md on session start.",
	profile:      agents.ProfileFull,
}

type factoryEnv = agentsEnv

var factoryEnvProvider = defaultAgentsEnv

func InstallFactory() error {
	return installAgents(factoryEnvProvider(), factoryIntegration)
}

func installFactory(env factoryEnv) error {
	return installAgents(env, factoryIntegration)
}

func CheckFactory() error {
	return checkAgents(factoryEnvProvider(), factoryIntegration)
}

func checkFactory(env factoryEnv) error {
	return checkAgents(env, factoryIntegration)
}

func RemoveFactory() error {
	return removeAgents(factoryEnvProvider(), factoryIntegration)
}

func removeFactory(env factoryEnv) error {
	return removeAgents(env, factoryIntegration)
}
