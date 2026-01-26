package setup

var codexIntegration = agentsIntegration{
	name:         "Codex CLI",
	setupCommand: "bd setup codex",
	readHint:     "Codex reads AGENTS.md at the start of each run or session. Restart Codex if it is already running.",
}

var codexEnvProvider = defaultAgentsEnv

// InstallCodex installs Codex integration.
func InstallCodex() {
	env := codexEnvProvider()
	if err := installCodex(env); err != nil {
		setupExit(1)
	}
}

func installCodex(env agentsEnv) error {
	return installAgents(env, codexIntegration)
}

// CheckCodex checks if Codex integration is installed.
func CheckCodex() {
	env := codexEnvProvider()
	if err := checkCodex(env); err != nil {
		setupExit(1)
	}
}

func checkCodex(env agentsEnv) error {
	return checkAgents(env, codexIntegration)
}

// RemoveCodex removes Codex integration.
func RemoveCodex() {
	env := codexEnvProvider()
	if err := removeCodex(env); err != nil {
		setupExit(1)
	}
}

func removeCodex(env agentsEnv) error {
	return removeAgents(env, codexIntegration)
}
