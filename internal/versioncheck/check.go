package versioncheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"golang.org/x/mod/modfile"
)

const (
	// ModulePath identifies the Beads source module.
	ModulePath = "github.com/steveyegge/beads"

	// CommandDescription is the stable preflight description for this checker.
	CommandDescription = "built-in Beads release version check"
)

type source struct {
	path        string
	description string
	read        func(string) (string, error)
}

// SourceResult describes one metadata surface inspected by Check.
type SourceResult struct {
	Description string
	Version     string
	Expected    string
	Problem     string
}

// UVLockChecker runs the optional dependency-freshness check. The bool reports
// whether uv was available; an unavailable uv is a soft skip.
type UVLockChecker func(root string) (available bool, err error)

var releaseSources = []source{
	{
		path:        "integrations/beads-mcp/pyproject.toml",
		description: "MCP pyproject.toml",
		read:        readProjectTOMLVersion,
	},
	{
		path:        "integrations/beads-mcp/src/beads_mcp/__init__.py",
		description: "MCP __init__.py",
		read:        readPythonModuleVersion,
	},
	{
		path:        "plugins/beads/.claude-plugin/plugin.json",
		description: "Claude plugin.json",
		read:        readTopLevelJSONVersion,
	},
	{
		path:        "plugins/beads/.codex-plugin/plugin.json",
		description: "Codex plugin.json",
		read:        readTopLevelJSONVersion,
	},
	{
		path:        ".claude-plugin/marketplace.json",
		description: "Claude marketplace.json",
		read:        readMarketplaceJSONVersion,
	},
	{
		path:        "npm-package/package.json",
		description: "npm package.json",
		read:        readTopLevelJSONVersion,
	},
}

var trackedHookMarkerPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{
		name:    "BEGIN",
		pattern: regexp.MustCompile(`--- BEGIN BEADS INTEGRATION v([^ ]+) ---`),
	},
	{
		name:    "END",
		pattern: regexp.MustCompile(`--- END BEADS INTEGRATION v([^ ]+) ---`),
	},
}

// Report describes a successful or failed release-version check.
type Report struct {
	CanonicalVersion   string
	CheckedSources     int
	CheckedHookMarkers int
	Sources            []SourceResult
}

// SuccessMessage formats the stable human-readable success result.
func (r Report) SuccessMessage() string {
	return fmt.Sprintf(
		"Versions match across %d release files, MCP uv.lock, and %d tracked hook markers: %s",
		r.CheckedSources,
		r.CheckedHookMarkers,
		r.CanonicalVersion,
	)
}

// Check compares every released metadata surface with cmd/bd/version.go.
func Check(root string) (Report, error) {
	report := Report{CheckedSources: len(releaseSources)}
	canonicalPath := filepath.Join(root, "cmd", "bd", "version.go")
	canonical, err := readGoPackageVersion(canonicalPath)
	if err != nil {
		return report, fmt.Errorf(
			"cannot read canonical version from cmd/bd/version.go: %w",
			err,
		)
	}
	report.CanonicalVersion = canonical

	var problems []string
	for _, item := range releaseSources {
		version, readErr := item.read(filepath.Join(root, filepath.FromSlash(item.path)))
		result := SourceResult{
			Description: item.description,
			Version:     version,
			Expected:    canonical,
		}
		switch {
		case readErr != nil:
			result.Problem = readErr.Error()
			problems = append(
				problems,
				fmt.Sprintf("%s: %v (expected %s)", item.description, readErr, canonical),
			)
		case version != canonical:
			result.Problem = fmt.Sprintf("version is %s", version)
			problems = append(
				problems,
				fmt.Sprintf("%s: %s (expected %s)", item.description, version, canonical),
			)
		}
		report.Sources = append(report.Sources, result)
	}

	// uv.lock is not one of the six published metadata surfaces, but the
	// existing release gate also requires its local beads-mcp package pin to
	// match. Preserve that assertion without expanding the release inventory.
	lockExpected := normalizePythonVersion(canonical)
	lockVersion, lockErr := readUVLockPackageVersion(
		filepath.Join(root, "integrations", "beads-mcp", "uv.lock"),
	)
	lockResult := SourceResult{
		Description: "MCP uv.lock (beads-mcp pin)",
		Version:     lockVersion,
		Expected:    lockExpected,
	}
	switch {
	case lockErr != nil:
		lockResult.Problem = lockErr.Error()
		problems = append(
			problems,
			fmt.Sprintf("%s: %v (expected %s)", lockResult.Description, lockErr, lockExpected),
		)
	case lockVersion != lockExpected:
		lockResult.Problem = fmt.Sprintf("version is %s", lockVersion)
		problems = append(
			problems,
			fmt.Sprintf("%s: %s (expected %s)", lockResult.Description, lockVersion, lockExpected),
		)
	}
	report.Sources = append(report.Sources, lockResult)

	hookResults, hookProblems := checkTrackedHookMarkers(root, canonical)
	report.CheckedHookMarkers = len(hookResults)
	report.Sources = append(report.Sources, hookResults...)
	problems = append(problems, hookProblems...)

	if len(problems) != 0 {
		return report, fmt.Errorf(
			"version mismatch detected:\n- %s",
			strings.Join(problems, "\n- "),
		)
	}
	return report, nil
}

func checkTrackedHookMarkers(root, canonical string) ([]SourceResult, []string) {
	hooksDir := filepath.Join(root, ".githooks")
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("enumerate tracked git hooks: %v", err)}
	}

	var results []SourceResult
	var problems []string
	for _, entry := range entries {
		// Match the shell's .githooks/* expansion: leading-dot artifacts are
		// not tracked hooks and are not rewritten by update-versions.sh.
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(hooksDir, entry.Name())
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relative = path
		}
		relative = filepath.ToSlash(relative)

		content, readErr := os.ReadFile(path) //nolint:gosec // path comes from the bounded .githooks directory

		for _, marker := range trackedHookMarkerPatterns {
			description := relative + " " + marker.name + " marker"
			result := SourceResult{
				Description: description,
				Expected:    canonical,
			}
			switch {
			case readErr != nil:
				result.Problem = readErr.Error()
				problems = append(
					problems,
					fmt.Sprintf("%s: %v (expected %s)", description, readErr, canonical),
				)
			default:
				version, found := firstHookMarkerVersion(content, marker.pattern)
				result.Version = version
				switch {
				case !found:
					result.Problem = fmt.Sprintf(
						"no '%s BEADS INTEGRATION' marker found",
						marker.name,
					)
					problems = append(
						problems,
						fmt.Sprintf("%s: %s", description, result.Problem),
					)
				case version != canonical:
					result.Problem = fmt.Sprintf("version is %s", version)
					problems = append(
						problems,
						fmt.Sprintf("%s: %s (expected %s)", description, version, canonical),
					)
				}
			}
			results = append(results, result)
		}
	}
	return results, problems
}

func firstHookMarkerVersion(content []byte, pattern *regexp.Regexp) (string, bool) {
	for _, line := range bytes.Split(content, []byte{'\n'}) {
		match := pattern.FindSubmatch(line)
		if len(match) == 2 {
			return string(match[1]), true
		}
	}
	return "", false
}

func normalizePythonVersion(version string) string {
	version = strings.Replace(version, "-rc.", "rc", 1)
	return strings.Replace(version, "-rc", "rc", 1)
}

// CheckUVLockFreshness runs uv directly, without selecting a shell. A missing
// uv executable is an intentional soft skip; an available uv returning an
// error means the checked-in lockfile is stale or otherwise invalid.
func CheckUVLockFreshness(root string) (bool, error) {
	uv, err := exec.LookPath("uv")
	if err != nil {
		return false, nil
	}
	cmd := exec.Command(
		uv,
		"lock",
		"--check",
		"--directory",
		filepath.Join(root, "integrations", "beads-mcp"),
	)
	return true, cmd.Run()
}

// FindRoot walks from start toward the filesystem root and identifies the
// nearest Beads source module. A Beads-shaped root with missing, malformed, or
// conflicting module identity is an error rather than an unrelated project.
func FindRoot(start string) (string, bool, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", false, fmt.Errorf("resolve start directory: %w", err)
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return "", false, fmt.Errorf("resolve start directory symlinks: %w", err)
	}
	for {
		signature, signatureErr := hasBeadsSourceSignature(current)
		if signatureErr != nil {
			return "", false, signatureErr
		}

		goModPath := filepath.Join(current, "go.mod")
		content, readErr := os.ReadFile(goModPath) //nolint:gosec // path is derived from the bounded ancestor walk
		switch {
		case readErr == nil:
			modulePath, parseErr := readModulePath(content)
			if parseErr != nil {
				if signature {
					return "", false, fmt.Errorf(
						"Beads-shaped source root %s has malformed go.mod: %w",
						current,
						parseErr,
					)
				}
				return "", false, nil
			}
			if modulePath == ModulePath {
				return current, true, nil
			}
			if signature {
				return "", false, fmt.Errorf(
					"Beads-shaped source root %s declares module %q, want %q",
					current,
					modulePath,
					ModulePath,
				)
			}
			return "", false, nil
		case !os.IsNotExist(readErr):
			return "", false, fmt.Errorf("inspect %s: %w", goModPath, readErr)
		case signature:
			return "", false, fmt.Errorf(
				"Beads-shaped source root %s is missing go.mod",
				current,
			)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", false, nil
		}
		current = parent
	}
}

func hasBeadsSourceSignature(root string) (bool, error) {
	paths := []string{
		filepath.Join(root, "cmd", "bd", "version.go"),
		filepath.Join(root, "scripts", "update-versions.sh"),
		filepath.Join(root, "integrations", "beads-mcp", "pyproject.toml"),
		filepath.Join(root, "plugins", "beads", ".codex-plugin", "plugin.json"),
	}
	found := 0
	for _, path := range paths {
		info, err := os.Stat(path)
		switch {
		case err == nil && info.Mode().IsRegular():
			found++
		case err == nil:
			continue
		case os.IsNotExist(err):
			continue
		default:
			return false, fmt.Errorf("inspect Beads source marker %s: %w", path, err)
		}
	}
	return found >= 2, nil
}

func readModulePath(content []byte) (string, error) {
	file, err := modfile.Parse("go.mod", content, nil)
	if err != nil {
		return "", fmt.Errorf("parse go.mod: %w", err)
	}
	if file.Module == nil {
		return "", fmt.Errorf("module directive not found")
	}
	return file.Module.Mod.Path, nil
}

func readGoPackageVersion(path string) (string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return "", fmt.Errorf("parse Go: %w", err)
	}
	var versions []string
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, specification := range general.Specs {
			values, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range values.Names {
				if name.Name != "Version" {
					continue
				}
				if index >= len(values.Values) {
					return "", fmt.Errorf("package-level Version has no direct value")
				}
				literal, ok := values.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return "", fmt.Errorf("package-level Version must be a string literal")
				}
				value, unquoteErr := strconv.Unquote(literal.Value)
				if unquoteErr != nil {
					return "", fmt.Errorf("parse package-level Version: %w", unquoteErr)
				}
				versions = append(versions, value)
			}
		}
	}
	if len(versions) != 1 {
		return "", fmt.Errorf(
			"found %d package-level Version string assignments, want exactly one",
			len(versions),
		)
	}
	return versions[0], nil
}

func readProjectTOMLVersion(path string) (string, error) {
	var document map[string]any
	if _, err := toml.DecodeFile(path, &document); err != nil {
		return "", fmt.Errorf("parse TOML: %w", err)
	}

	projectValue, found, err := readExactTOMLField(document, "project", "[project]")
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("non-empty string field [project].version not found")
	}
	project, ok := projectValue.(map[string]any)
	if !ok {
		return "", fmt.Errorf("field [project] must be a table")
	}

	versionValue, found, err := readExactTOMLField(
		project,
		"version",
		"[project].version",
	)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("non-empty string field [project].version not found")
	}
	version, ok := versionValue.(string)
	if !ok {
		return "", fmt.Errorf("field [project].version must be a string")
	}
	if version == "" {
		return "", fmt.Errorf("non-empty string field [project].version not found")
	}
	return version, nil
}

func readUVLockPackageVersion(path string) (string, error) {
	var document map[string]any
	if _, err := toml.DecodeFile(path, &document); err != nil {
		return "", fmt.Errorf("parse TOML: %w", err)
	}

	packageValue, found, err := readExactTOMLField(document, "package", "[[package]]")
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("package entries not found")
	}
	packages, ok := packageValue.([]map[string]any)
	if !ok {
		return "", fmt.Errorf("field [[package]] must be an array of tables")
	}

	var versions []string
	for index, item := range packages {
		qualified := fmt.Sprintf("[[package]][%d]", index)
		nameValue, nameFound, nameErr := readExactTOMLField(item, "name", qualified+".name")
		if nameErr != nil {
			return "", nameErr
		}
		if !nameFound {
			return "", fmt.Errorf("non-empty string field %s.name not found", qualified)
		}
		name, ok := nameValue.(string)
		if !ok || name == "" {
			return "", fmt.Errorf("field %s.name must be a non-empty string", qualified)
		}
		if name != "beads-mcp" {
			continue
		}

		versionValue, versionFound, versionErr := readExactTOMLField(
			item,
			"version",
			qualified+".version",
		)
		if versionErr != nil {
			return "", versionErr
		}
		if !versionFound {
			return "", fmt.Errorf("non-empty string field %s.version not found", qualified)
		}
		version, ok := versionValue.(string)
		if !ok || version == "" {
			return "", fmt.Errorf("field %s.version must be a non-empty string", qualified)
		}
		versions = append(versions, version)
	}
	if len(versions) != 1 {
		return "", fmt.Errorf(
			"found %d beads-mcp package entries, want exactly one",
			len(versions),
		)
	}
	return versions[0], nil
}

func readExactTOMLField(
	table map[string]any,
	name string,
	qualifiedName string,
) (any, bool, error) {
	var aliases []string
	for candidate := range table {
		if strings.EqualFold(candidate, name) {
			aliases = append(aliases, candidate)
		}
	}
	if len(aliases) == 0 {
		return nil, false, nil
	}
	sort.Strings(aliases)
	if len(aliases) != 1 || aliases[0] != name {
		quoted := make([]string, len(aliases))
		for index, alias := range aliases {
			quoted[index] = strconv.Quote(alias)
		}
		return nil, false, fmt.Errorf(
			"field %s is ambiguous or uses non-exact casing: %s",
			qualifiedName,
			strings.Join(quoted, ", "),
		)
	}
	return table[name], true, nil
}

func readPythonModuleVersion(path string) (string, error) {
	content, err := os.ReadFile(path) //nolint:gosec // callers supply fixed repository-relative paths
	if err != nil {
		return "", err
	}

	var (
		tripleQuote string
		versions    []string
	)
	for lineNumber, line := range strings.Split(string(content), "\n") {
		if tripleQuote == "" && strings.HasPrefix(line, "__version__") {
			version, matched, parseErr := parsePythonVersionAssignment(line)
			if parseErr != nil {
				return "", fmt.Errorf("line %d: %w", lineNumber+1, parseErr)
			}
			if matched {
				versions = append(versions, version)
				continue
			}
		}
		tripleQuote = scanPythonTripleQuotedState(line, tripleQuote)
	}
	if len(versions) != 1 {
		return "", fmt.Errorf(
			"found %d module-level __version__ string assignments, want exactly one",
			len(versions),
		)
	}
	return versions[0], nil
}

func parsePythonVersionAssignment(line string) (string, bool, error) {
	const name = "__version__"
	if !strings.HasPrefix(line, name) {
		return "", false, nil
	}
	rest := line[len(name):]
	if rest != "" && !strings.ContainsRune(" \t=", rune(rest[0])) {
		return "", false, nil
	}
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, "==") {
		return "", true, fmt.Errorf("%s must use a direct assignment", name)
	}
	rest = strings.TrimLeft(rest[1:], " \t")
	if len(rest) < 2 || (rest[0] != '"' && rest[0] != '\'') {
		return "", true, fmt.Errorf("%s must be assigned a string literal", name)
	}
	quote := rest[0]
	end := -1
	for index := 1; index < len(rest); index++ {
		if rest[index] == '\\' {
			index++
			continue
		}
		if rest[index] == quote {
			end = index
			break
		}
	}
	if end < 0 {
		return "", true, fmt.Errorf("%s has an unterminated string literal", name)
	}
	literal := rest[:end+1]
	if strings.Contains(literal, `\`) {
		return "", true, fmt.Errorf("%s must not use escape sequences", name)
	}
	value := literal[1 : len(literal)-1]
	trailing := strings.TrimSpace(rest[end+1:])
	if trailing != "" && !strings.HasPrefix(trailing, "#") {
		return "", true, fmt.Errorf("%s has unexpected trailing syntax", name)
	}
	if value == "" {
		return "", true, fmt.Errorf("%s must be non-empty", name)
	}
	return value, true, nil
}

func scanPythonTripleQuotedState(line, active string) string {
	for index := 0; index < len(line); {
		if active != "" {
			end := findUnescaped(line, active, index)
			if end < 0 {
				return active
			}
			index = end + len(active)
			active = ""
			continue
		}
		if line[index] == '#' {
			return ""
		}
		if line[index] != '\'' && line[index] != '"' {
			index++
			continue
		}
		quote := line[index]
		if index+2 < len(line) && line[index+1] == quote && line[index+2] == quote {
			active = line[index : index+3]
			index += 3
			continue
		}
		index++
		for index < len(line) {
			if line[index] == '\\' {
				index += 2
				continue
			}
			if index < len(line) && line[index] == quote {
				index++
				break
			}
			index++
		}
	}
	return active
}

func findUnescaped(value, needle string, start int) int {
	for {
		index := strings.Index(value[start:], needle)
		if index < 0 {
			return -1
		}
		index += start
		backslashes := 0
		for position := index - 1; position >= 0 && value[position] == '\\'; position-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return index
		}
		start = index + 1
	}
}

func readTopLevelJSONVersion(path string) (string, error) {
	content, err := os.ReadFile(path) //nolint:gosec // callers supply fixed repository-relative paths
	if err != nil {
		return "", err
	}
	version, err := readUniqueJSONStringField(content, "version")
	if err != nil {
		return "", err
	}
	if version == "" {
		return "", fmt.Errorf("non-empty string field .version not found")
	}
	return version, nil
}

func readMarketplaceJSONVersion(path string) (string, error) {
	content, err := os.ReadFile(path) //nolint:gosec // callers supply fixed repository-relative paths
	if err != nil {
		return "", err
	}
	pluginsJSON, err := readUniqueJSONField(content, "plugins")
	if err != nil {
		return "", err
	}
	var plugins []json.RawMessage
	if err := json.Unmarshal(pluginsJSON, &plugins); err != nil {
		return "", fmt.Errorf("parse JSON field .plugins: %w", err)
	}
	if len(plugins) == 0 {
		return "", fmt.Errorf("non-empty string field .plugins[0].version not found")
	}
	version, err := readUniqueJSONStringField(plugins[0], "version")
	if err != nil {
		return "", fmt.Errorf(".plugins[0]: %w", err)
	}
	if version == "" {
		return "", fmt.Errorf("non-empty string field .plugins[0].version not found")
	}
	return version, nil
}

func readUniqueJSONStringField(document []byte, field string) (string, error) {
	raw, err := readUniqueJSONField(document, field)
	if err != nil {
		return "", err
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("field .%s must be a string: %w", field, err)
	}
	return value, nil
}

func readUniqueJSONField(document []byte, field string) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	start, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	if start != json.Delim('{') {
		return nil, fmt.Errorf("parse JSON: top-level value must be an object")
	}

	var (
		found bool
		value json.RawMessage
	)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("parse JSON: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("parse JSON: object key must be a string")
		}
		var candidate json.RawMessage
		if err := decoder.Decode(&candidate); err != nil {
			return nil, fmt.Errorf("parse JSON: %w", err)
		}
		if key != field {
			continue
		}
		if found {
			return nil, fmt.Errorf("field .%s appears more than once", field)
		}
		found = true
		value = candidate
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse JSON: unexpected trailing value")
		}
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("field .%s not found", field)
	}
	return value, nil
}
