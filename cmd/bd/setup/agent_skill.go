package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/templates"
)

var (
	agentSkillEnvProvider = defaultAgentSkillEnv
	errAgentSkillMissing  = errors.New("beads agent skill not installed")
	errAgentSkillStale    = errors.New("beads agent skill is stale")
)

type agentSkillEnv struct {
	stdout     io.Writer
	stderr     io.Writer
	projectDir string
	ensureDir  func(string, os.FileMode) error
	readFile   func(string) ([]byte, error)
	writeFile  func(string, []byte) error
	removeFile func(string) error
}

func defaultAgentSkillEnv() (agentSkillEnv, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return agentSkillEnv{}, fmt.Errorf("working directory: %w", err)
	}
	return agentSkillEnv{
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		projectDir: workDir,
		ensureDir:  EnsureDir,
		readFile:   os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
		removeFile: os.Remove,
	}, nil
}

func agentSkillEnvFromCodex(env codexEnv) agentSkillEnv {
	return agentSkillEnv{
		stdout:     env.stdout,
		stderr:     env.stderr,
		projectDir: env.projectDir,
		ensureDir:  env.ensureDir,
		readFile:   env.readFile,
		writeFile:  env.writeFile,
		removeFile: env.removeFile,
	}
}

func agentSkillRootPath(base string) string {
	return filepath.Join(base, ".agents", "skills", "beads")
}

func agentSkillPath(base string) string {
	return filepath.Join(agentSkillRootPath(base), "SKILL.md")
}

func agentSkillOpenAIYAMLPath(base string) string {
	return filepath.Join(agentSkillRootPath(base), "agents", "openai.yaml")
}

// InstallProjectAgentSkill installs the repo-local Beads agent skill,
// returning an error instead of exiting. Used by bd init.
func InstallProjectAgentSkill() error {
	return InstallProjectAgentSkillWithOutput(os.Stdout, os.Stderr)
}

// InstallProjectAgentSkillWithOutput installs the repo-local Beads agent skill
// using explicit output streams. Used by bd init to honor --quiet.
func InstallProjectAgentSkillWithOutput(stdout, stderr io.Writer) error {
	env, err := agentSkillEnvProvider()
	if err != nil {
		return err
	}
	env.stdout = stdout
	env.stderr = stderr
	return installAgentSkill(env)
}

func installAgentSkill(env agentSkillEnv) error {
	_, _ = fmt.Fprintln(env.stdout, "Installing Beads agent skill...")

	skillPath := agentSkillPath(env.projectDir)
	if err := env.ensureDir(filepath.Dir(skillPath), 0o755); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return err
	}
	if err := env.writeFile(skillPath, []byte(templates.BeadsAgentSkill())); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write skill: %v\n", err)
		return err
	}

	openAIYAMLPath := agentSkillOpenAIYAMLPath(env.projectDir)
	if err := env.ensureDir(filepath.Dir(openAIYAMLPath), 0o755); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return err
	}
	if err := env.writeFile(openAIYAMLPath, []byte(templates.BeadsAgentSkillOpenAIYAML())); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write skill metadata: %v\n", err)
		return err
	}

	_, _ = fmt.Fprintln(env.stdout, "✓ Beads agent skill installed")
	_, _ = fmt.Fprintf(env.stdout, "  Skill: %s\n", skillPath)
	return nil
}

func checkAgentSkill(env agentSkillEnv, setupCommand string) error {
	skillPath := agentSkillPath(env.projectDir)
	data, err := env.readFile(skillPath)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintf(env.stdout, "✗ Beads agent skill not found: %s\n", skillPath)
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s\n", setupCommand)
		return errAgentSkillMissing
	}
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: read skill: %v\n", err)
		return err
	}
	if string(data) != templates.BeadsAgentSkill() {
		_, _ = fmt.Fprintf(env.stdout, "⚠ Beads agent skill installed but stale: %s\n", skillPath)
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s\n", setupCommand)
		return errAgentSkillStale
	}

	openAIYAMLPath := agentSkillOpenAIYAMLPath(env.projectDir)
	data, err = env.readFile(openAIYAMLPath)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintf(env.stdout, "✗ Beads agent skill metadata not found: %s\n", openAIYAMLPath)
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s\n", setupCommand)
		return errAgentSkillMissing
	}
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: read skill metadata: %v\n", err)
		return err
	}
	if string(data) != templates.BeadsAgentSkillOpenAIYAML() {
		_, _ = fmt.Fprintf(env.stdout, "⚠ Beads agent skill metadata is stale: %s\n", openAIYAMLPath)
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s\n", setupCommand)
		return errAgentSkillStale
	}

	_, _ = fmt.Fprintf(env.stdout, "✓ Beads agent skill installed: %s\n", skillPath)
	return nil
}

func removeAgentSkill(env agentSkillEnv) error {
	_, _ = fmt.Fprintln(env.stdout, "Removing Beads agent skill...")
	for _, path := range []string{
		agentSkillOpenAIYAMLPath(env.projectDir),
		agentSkillPath(env.projectDir),
	} {
		if err := env.removeFile(path); err != nil && !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(env.stderr, "Error: remove %s: %v\n", path, err)
			return err
		}
	}
	for _, dir := range []string{
		filepath.Join(env.projectDir, ".agents", "skills", "beads", "agents"),
		filepath.Join(env.projectDir, ".agents", "skills", "beads"),
		filepath.Join(env.projectDir, ".agents", "skills"),
		filepath.Join(env.projectDir, ".agents"),
	} {
		entries, err := os.ReadDir(dir) // #nosec G304 -- project-local skill path
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			continue
		}
		if err := env.removeFile(dir); err != nil && !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(env.stderr, "Error: remove %s: %v\n", dir, err)
			return err
		}
	}
	_, _ = fmt.Fprintln(env.stdout, "✓ Beads agent skill removed")
	return nil
}
