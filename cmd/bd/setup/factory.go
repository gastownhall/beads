package setup

var factoryIntegration = agentsIntegration{
	name:         "Factory.ai (Droid)",
	setupCommand: "bd setup factory",
	readHint:     "Factory Droid will automatically read AGENTS.md on session start.",
}

type factoryEnv = agentsEnv

var factoryEnvProvider = defaultAgentsEnv

// InstallFactory installs Factory.ai/Droid integration.
func InstallFactory() {
	env := factoryEnvProvider()
	if err := installAgents(env, factoryIntegration); err != nil {
		setupExit(1)
	}
}

func installFactory(env factoryEnv) error {
	return installAgents(env, factoryIntegration)
}

// CheckFactory checks if Factory.ai integration is installed.
func CheckFactory() {
	env := factoryEnvProvider()
	if err := checkAgents(env, factoryIntegration); err != nil {
		setupExit(1)
	}
}

func checkFactory(env factoryEnv) error {
	return checkAgents(env, factoryIntegration)
}

// RemoveFactory removes Factory.ai integration.
func RemoveFactory() {
	env := factoryEnvProvider()
	if err := removeAgents(env, factoryIntegration); err != nil {
		setupExit(1)
	}
}

func removeFactory(env factoryEnv) error {
	return removeAgents(env, factoryIntegration)
}
