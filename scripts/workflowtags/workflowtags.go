// Package workflowtags checks the repository's source convention for direct Go
// commands in GitHub Actions run steps. It recognizes only the first simple
// command on each logical line and deliberately does not interpret arbitrary
// shell control flow, substitutions, or sourced scripts.
package workflowtags

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// Violation identifies a direct workflow Go command missing the required tag.
type Violation struct {
	Path    string
	Job     string
	Step    string
	Line    int
	Message string
}

// CheckDir checks every workflow YAML file immediately under dir.
func CheckDir(dir string) ([]Violation, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.y*ml"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no GitHub Actions workflows found under %s", dir)
	}
	sort.Strings(paths)

	var violations []Violation
	for _, path := range paths {
		// #nosec G304 -- path is emitted by the bounded workflow glob above.
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var workflow workflow
		if err := yaml.Unmarshal(data, &workflow); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		jobNames := make([]string, 0, len(workflow.Jobs))
		for jobName := range workflow.Jobs {
			jobNames = append(jobNames, jobName)
		}
		sort.Strings(jobNames)
		for _, jobName := range jobNames {
			job := workflow.Jobs[jobName]
			for stepIndex, step := range job.Steps {
				stepName := step.Name
				if stepName == "" {
					stepName = fmt.Sprintf("step %d", stepIndex+1)
				}
				for _, runViolation := range CheckRun(step.Run) {
					runViolation.Path = path
					runViolation.Job = jobName
					runViolation.Step = stepName
					violations = append(violations, runViolation)
				}
			}
		}
	}
	return violations, nil
}

type workflow struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Steps []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

// CheckRun checks recognizable direct commands in one decoded run scalar.
func CheckRun(run string) []Violation {
	lines := strings.Split(strings.ReplaceAll(run, "\r\n", "\n"), "\n")
	var violations []Violation
	for i := 0; i < len(lines); i++ {
		lineNumber := i + 1
		logical := lines[i]
		for trailingUnquotedBackslash(logical) && i+1 < len(lines) {
			logical = strings.TrimSpace(strings.TrimSuffix(strings.TrimRightFunc(logical, unicode.IsSpace), "\\")) + " " + strings.TrimSpace(lines[i+1])
			i++
		}

		words, err := tokenize(logical)
		if err != nil {
			// Malformed or complex shell syntax is outside this bounded source check.
			continue
		}
		goArgs, ok := directGoArgs(words)
		if !ok || exemptVersionedTool(goArgs) || hasRequiredTag(goArgs) {
			continue
		}
		violations = append(violations, Violation{
			Line:    lineNumber,
			Message: fmt.Sprintf("direct `go %s` must declare literal gms_pure_go in -tags", goArgs[0]),
		})
	}
	return violations
}

func directGoArgs(words []string) ([]string, bool) {
	words = skipEnvPrefix(words)
	if len(words) > 0 && words[0] == "ci_time" {
		separator := -1
		for i, word := range words[1:] {
			if word == "--" {
				separator = i + 1
				break
			}
		}
		if separator < 0 {
			return nil, false
		}
		words = skipEnvPrefix(words[separator+1:])
	}
	if len(words) < 2 || words[0] != "go" {
		return nil, false
	}
	switch words[1] {
	case "build", "test", "run", "generate", "install":
		return words[1:], true
	default:
		return nil, false
	}
}

func skipEnvPrefix(words []string) []string {
	if len(words) > 0 && words[0] == "env" {
		words = words[1:]
		for len(words) > 0 {
			switch {
			case words[0] == "--":
				words = words[1:]
				goto assignments
			case words[0] == "-i" || words[0] == "--ignore-environment":
				words = words[1:]
			case words[0] == "-u" || words[0] == "--unset" || words[0] == "-C" || words[0] == "--chdir":
				if len(words) < 2 {
					return nil
				}
				words = words[2:]
			case strings.HasPrefix(words[0], "--unset=") || strings.HasPrefix(words[0], "--chdir="):
				words = words[1:]
			default:
				goto assignments
			}
		}
	}
assignments:
	for len(words) > 0 && assignment.MatchString(words[0]) {
		words = words[1:]
	}
	return words
}

func hasRequiredTag(goArgs []string) bool {
	lastTags := ""
	found := false
	for i := 1; i < len(goArgs); i++ {
		arg := goArgs[i]
		if arg == "--" || (goArgs[0] == "test" && arg == "-args") {
			break
		}
		switch {
		case arg == "-tags":
			if i+1 >= len(goArgs) {
				lastTags = ""
				found = true
				break
			}
			i++
			lastTags = goArgs[i]
			found = true
		case strings.HasPrefix(arg, "-tags="):
			lastTags = strings.TrimPrefix(arg, "-tags=")
			found = true
		}
	}
	if !found {
		return false
	}
	for _, tag := range strings.FieldsFunc(lastTags, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if tag == "gms_pure_go" {
			return true
		}
	}
	return false
}

// The repository's pinned-tool convention is intentionally narrow: unflagged
// external package operands with versions. For go run, later words are program
// arguments; for go install, every package operand must qualify.
func exemptVersionedTool(goArgs []string) bool {
	if len(goArgs) < 2 || (goArgs[0] != "run" && goArgs[0] != "install") {
		return false
	}
	packages := goArgs[1:]
	if goArgs[0] == "run" {
		packages = packages[:1]
	}
	for _, pkg := range packages {
		at := strings.LastIndexByte(pkg, '@')
		if at <= 0 || at == len(pkg)-1 || strings.HasPrefix(pkg, ".") || strings.HasPrefix(pkg, "/") || !strings.Contains(pkg[:at], "/") {
			return false
		}
	}
	return true
}

func trailingUnquotedBackslash(line string) bool {
	trimmed := strings.TrimRightFunc(line, unicode.IsSpace)
	if !strings.HasSuffix(trimmed, "\\") {
		return false
	}
	backslashes := 0
	for i := len(trimmed) - 1; i >= 0 && trimmed[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

// tokenize covers only the simple command source forms owned by this check.
// At the first unquoted shell operator it returns the first simple command.
func tokenize(line string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}

	for _, r := range strings.TrimSpace(line) {
		if escaped {
			word.WriteRune(r)
			escaped = false
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' {
				escaped = true
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\\':
			escaped = true
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		case r == '#' && word.Len() == 0:
			return words, nil
		case r == '#':
			word.WriteRune(r)
		case r == ';' || r == '|' || r == '&':
			flush()
			return words, nil
		default:
			word.WriteRune(r)
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	flush()
	return words, nil
}
