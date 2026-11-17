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
	errAgentSkillMissing = errors.New("beads agent skill not installed")
	errAgentSkillStale   = errors.New("beads agent skill is stale")
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

func agentSkillRootPath(base string) string {
	return filepath.Join(base, ".agents", "skills", "beads")
}

func agentSkillPath(base string) string {
	return filepath.Join(agentSkillRootPath(base), "SKILL.md")
}

func agentSkillOpenAIYAMLPath(base string) string {
	return filepath.Join(agentSkillRootPath(base), "agents", "openai.yaml")
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
	removed := map[string]bool{}
	for _, path := range []string{
		agentSkillOpenAIYAMLPath(env.projectDir),
		agentSkillPath(env.projectDir),
	} {
		err := env.removeFile(path)
		if err == nil {
			removed[path] = true
		} else if !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(env.stderr, "Error: remove %s: %v\n", path, err)
			return err
		}
	}
	// The directories the skill's files were in, innermost first, go only
	// when this run took one of bd's files, or such a directory, out of
	// them, and each only if that left it empty; never a symlinked one
	// (removeEmptyDir), so a linked .agents, .agents/skills or
	// .agents/skills/beads stays. The metadata's agents directory goes only
	// when its openai.yaml did. Directories are removed through
	// env.removeFile, as the skill's files are.
	var dirs []string
	if removed[agentSkillOpenAIYAMLPath(env.projectDir)] {
		dirs = append(dirs, filepath.Join(env.projectDir, ".agents", "skills", "beads", "agents"))
	}
	if len(removed) > 0 {
		dirs = append(dirs,
			filepath.Join(env.projectDir, ".agents", "skills", "beads"),
			filepath.Join(env.projectDir, ".agents", "skills"),
			filepath.Join(env.projectDir, ".agents"),
		)
	}
	for _, dir := range dirs {
		if err := removeEmptyDir(dir, env.removeFile); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: remove %s: %v\n", dir, err)
			return err
		}
	}
	_, _ = fmt.Fprintln(env.stdout, "✓ Beads agent skill removed")
	return nil
}
