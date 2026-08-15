package gitattributespolicy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func resolveEOLPolicyGit(t *testing.T) string {
	t.Helper()

	discovered, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("resolve Git once: %v", err)
	}
	absolute, err := filepath.Abs(discovered)
	if err != nil {
		t.Fatalf("make resolved Git path absolute: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		t.Fatalf("canonicalize resolved Git path %q: %v", absolute, err)
	}
	canonical = filepath.Clean(canonical)
	if !filepath.IsAbs(canonical) {
		t.Fatalf("canonical Git path is not absolute: %q", canonical)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		t.Fatalf("stat canonical Git path %q: %v", canonical, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("canonical Git path is not a regular file: %q (%s)", canonical, info.Mode())
	}
	return canonical
}

func requireEOLPolicyGitVersion(t *testing.T, gitPath, repository string) {
	t.Helper()

	version := strings.TrimSpace(runEOLPolicyGit(t, gitPath, repository, "--version"))
	if strings.ContainsAny(version, "\r\n") {
		t.Fatalf("Git version identity is not one line: %q", version)
	}
	fields := strings.Fields(version)
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" ||
		fields[2] == "" || fields[2][0] < '0' || fields[2][0] > '9' ||
		!strings.Contains(fields[2], ".") {
		t.Fatalf("resolved executable did not positively identify as Git: %q", version)
	}
}

func runEOLPolicyGit(
	t *testing.T,
	gitPath string,
	repository string,
	arguments ...string,
) string {
	t.Helper()
	command := exec.Command(gitPath, arguments...)
	command.Dir = repository
	command.Env = eolPolicyGitEnvironment(repository, gitPath)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func eolPolicyGitEnvironment(repository, gitPath string) []string {
	environment := make([]string, 0, 18)
	for _, name := range []string{
		"COMSPEC",
		"LANG",
		"LC_ALL",
		"LC_CTYPE",
		"PATHEXT",
		"SYSTEMDRIVE",
		"SYSTEMROOT",
		"TEMP",
		"TMP",
		"TMPDIR",
		"WINDIR",
	} {
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		}
	}
	return append(
		environment,
		"PATH="+filepath.Dir(gitPath),
		"HOME="+repository,
		"XDG_CONFIG_HOME="+filepath.Join(repository, ".xdg"),
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0")
}
