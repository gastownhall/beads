package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/templates/agents"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// The built-in copilot recipe's files. The plugin manifest is bd's own; the
// instructions file is the repository's, where Copilot reads its custom
// instructions, so bd keeps only a managed section in it (GH#7431).
const (
	CopilotManifestPath     = ".copilot-plugin/plugin.json"
	CopilotInstructionsPath = ".github/copilot-instructions.md"
)

var (
	errCopilotMalformedSection = errors.New("malformed beads section markers")
	errCopilotOpenBlock        = errors.New("instructions end inside an open block")
)

var copilotAgentsIntegration = agentsIntegration{
	name:         "GitHub Copilot CLI",
	setupCommand: "bd setup copilot",
	profile:      agents.ProfileMinimal,
}

type copilotEnv struct {
	stdout           io.Writer
	stderr           io.Writer
	manifestPath     string
	instructionsPath string
	// manifest is the plugin manifest install writes, the built-in recipe's.
	manifest string
	// renderOpts, when set, fixes the section's render options instead of
	// detecting them from the current directory's beads config.
	renderOpts func() agents.RenderOpts
}

func defaultCopilotEnv(manifest string) copilotEnv {
	return copilotEnv{
		stdout:           os.Stdout,
		stderr:           os.Stderr,
		manifestPath:     CopilotManifestPath,
		instructionsPath: CopilotInstructionsPath,
		manifest:         manifest,
	}
}

// instructionsEnv is the agentsEnv that manages the beads section in the
// instructions file. A missing or empty file gets the section alone, without
// the AGENTS.md scaffolding.
func (env copilotEnv) instructionsEnv() agentsEnv {
	return agentsEnv{
		agentsPath:  env.instructionsPath,
		stdout:      env.stdout,
		stderr:      env.stderr,
		sectionOnly: true,
		renderOpts:  env.renderOpts,
	}
}

// InstallCopilot writes the Copilot CLI plugin manifest and adds or updates
// the beads section in .github/copilot-instructions.md, keeping the rest of
// that file as it is.
func InstallCopilot(manifest string) error {
	return installCopilot(defaultCopilotEnv(manifest))
}

func installCopilot(env copilotEnv) error {
	// Refuse markers the section cannot be found by before writing anything:
	// an END marker alone would otherwise get a section appended after it.
	// Refuse, too, a file that would hold an appended section in a block.
	content, err := validateCopilotInstructions(env)
	if err != nil {
		return err
	}
	if err := refuseOpenBlockAtEnd(env, content); err != nil {
		return err
	}

	for _, path := range []string{env.manifestPath, env.instructionsPath} {
		if err := EnsureDir(filepath.Dir(path), 0o755); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
			return err
		}
	}
	// A repository can commit the manifest path as a symlink to anywhere, so
	// atomicWriteFile refuses to write through one.
	if err := atomicWriteFile(env.manifestPath, []byte(env.manifest)); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write %s: %v\n", env.manifestPath, err)
		return err
	}

	skipped := false
	instructions := env.instructionsEnv()
	instructions.skipped = &skipped
	integration := copilotAgentsIntegration
	integration.readHint = "Plugin manifest: " + env.manifestPath
	if err := installAgents(instructions, integration); err != nil {
		return err
	}
	if skipped {
		_, _ = fmt.Fprintf(env.stdout, "\n✓ GitHub Copilot CLI plugin manifest installed: %s\n", env.manifestPath)
		_, _ = fmt.Fprintf(env.stdout, "  Instructions skipped: %s is a symlink\n", env.instructionsPath)
	}
	return nil
}

// validateCopilotInstructions fails, saying so, when the instructions file's
// beads markers are malformed, and otherwise returns the file's text. A
// missing file passes, and so does a symlink, which install and --remove
// leave alone; both return "".
func validateCopilotInstructions(env copilotEnv) (string, error) {
	info, err := os.Lstat(env.instructionsPath)
	if os.IsNotExist(err) || (err == nil && info.Mode()&os.ModeSymlink != 0) {
		return "", nil
	}
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to inspect %s: %v\n", env.instructionsPath, err)
		return "", err
	}
	data, err := os.ReadFile(env.instructionsPath) // #nosec G304 -- bd's own setup destination
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", env.instructionsPath, err)
		return "", err
	}
	if err := validateCopilotSection(string(data)); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: %s: %v\n", env.instructionsPath, err)
		_, _ = fmt.Fprintf(env.stderr, "  Fix or delete the beads markers by hand, including any the text only mentions, then run: %s\n", copilotAgentsIntegration.setupCommand)
		return "", err
	}
	return string(data), nil
}

// refuseOpenBlockAtEnd fails, saying so, when content holds no beads section
// and ends inside a block that a blank line does not close, such as a fenced
// code block or an HTML comment left open. Install appends the section after
// a blank line, so it would land in that block, where --check and --remove
// would take its markers for an example and refuse them.
func refuseOpenBlockAtEnd(env copilotEnv, content string) error {
	if content == "" || containsBeadsMarker(content) {
		return nil
	}
	// The block a line falls in depends only on the lines up to it, so the
	// BEGIN marker stands in for the whole section.
	appended := addBeadsSection(content, agentsBeginMarker+"\n", "")
	block := markdownBlockAround(appended, findBeginMarker(appended))
	if block == "" {
		return nil
	}
	_, _ = fmt.Fprintf(env.stderr, "Error: %s ends inside %s, where bd would add its section\n", env.instructionsPath, block)
	_, _ = fmt.Fprintf(env.stderr, "  Close that block by hand, then run: %s\n", copilotAgentsIntegration.setupCommand)
	return fmt.Errorf("%w: %s", errCopilotOpenBlock, block)
}

// agentsBeginPrefix starts every BEGIN marker, legacy or with metadata.
const agentsBeginPrefix = "<!-- BEGIN BEADS INTEGRATION"

// validateCopilotSection reports beads markers that are not exactly the pair
// bd writes around its section: one BEGIN and one END marker, in that order,
// each a comment alone on a line that starts an HTML block of its own, as
// CommonMark parses the file, and so in no code block or HTML block opened
// above it. installAgents and removeBeadsSection take the text from the
// first BEGIN marker to the first END marker as the section, so a marker
// that text only mentions, inline or in an example, would have install
// replace and --remove cut the user's own text up to the other marker.
func validateCopilotSection(content string) error {
	begins := strings.Count(content, agentsBeginPrefix)
	ends := strings.Count(content, agentsEndMarker)
	var problem string
	switch {
	case begins == 0 && ends == 0:
		return nil
	case begins == 0:
		problem = "END marker without a BEGIN marker"
	case ends == 0:
		problem = "BEGIN marker without an END marker"
	case begins > 1:
		problem = fmt.Sprintf("%d BEGIN markers, not one", begins)
	case ends > 1:
		problem = fmt.Sprintf("%d END markers, not one", ends)
	default:
		problem = copilotMarkerPlacement(content)
	}
	if problem == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", errCopilotMalformedSection, problem)
}

// copilotMarkerPlacement says what is wrong with where the one BEGIN and the
// one END marker of content stand, or returns "" when the BEGIN marker comes
// first and each is a comment alone on a line that starts an HTML block of
// its own.
func copilotMarkerPlacement(content string) string {
	start := findBeginMarker(content)
	end := strings.Index(content, agentsEndMarker)
	switch {
	case end < start:
		return "END marker before the BEGIN marker"
	case !isBeginMarkerLine(markerLine(content, start)):
		return "BEGIN marker not on a line of its own"
	case markerLine(content, end) != agentsEndMarker:
		return "END marker not on a line of its own"
	}
	if block := markdownBlockAround(content, start); block != "" {
		return "BEGIN marker inside " + block
	}
	if block := markdownBlockAround(content, end); block != "" {
		return "END marker inside " + block
	}
	return ""
}

// markerLine returns the line of content holding offset at, without its line
// ending.
func markerLine(content string, at int) string {
	line := content[strings.LastIndexByte(content[:at], '\n')+1:]
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	return strings.TrimSuffix(line, "\r")
}

// isBeginMarkerLine reports whether line, without its line ending, is a
// BEGIN marker alone: the marker opens the line, and nothing but spaces or
// tabs follows the "-->" that ends its comment.
func isBeginMarkerLine(line string) bool {
	rest, ok := strings.CutPrefix(line, agentsBeginPrefix)
	if !ok {
		return false
	}
	_, after, closed := strings.Cut(rest, "-->")
	return closed && strings.TrimRight(after, " \t") == ""
}

// markdownBlockAround names the block that holds the line of content
// starting at offset at, as CommonMark parses content, such as "the fenced
// code block that starts at line 3", or returns "" when that line starts an
// HTML block of its own, as each beads marker bd writes does. CommonMark
// decides what no line-by-line fence count can: the length and info string
// of a fence, how far it may be indented, and the list item or block quote
// whose end closes it.
func markdownBlockAround(content string, at int) string {
	doc := goldmark.DefaultParser().Parse(text.NewReader([]byte(content)))
	block := markdownBlockHolding(doc, at)
	if html, ok := block.(*ast.HTMLBlock); ok && html.Lines().Len() > 0 && html.Lines().At(0).Start == at {
		return ""
	}
	return describeMarkdownBlock(content, block)
}

// markdownBlockHolding returns the block of doc one of whose lines holds
// offset at, or nil.
func markdownBlockHolding(doc ast.Node, at int) ast.Node {
	var holder ast.Node
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && n.Type() == ast.TypeBlock && blockHasLineAt(n, at) {
			holder = n
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return holder
}

// blockHasLineAt reports whether one of block's lines, or the line that
// closes an HTML block, holds offset at.
func blockHasLineAt(block ast.Node, at int) bool {
	lines := block.Lines()
	for i := range lines.Len() {
		if line := lines.At(i); line.Start <= at && at < line.Stop {
			return true
		}
	}
	html, ok := block.(*ast.HTMLBlock)
	return ok && html.HasClosure() && html.ClosureLine.Start <= at && at < html.ClosureLine.Stop
}

// describeMarkdownBlock names block, a block of content, by its kind and the
// line it starts on.
func describeMarkdownBlock(content string, block ast.Node) string {
	if block == nil || block.Lines().Len() == 0 {
		return "a Markdown block"
	}
	line := strings.Count(content[:block.Lines().At(0).Start], "\n") + 1
	kind := "Markdown block"
	switch block := block.(type) {
	case *ast.FencedCodeBlock:
		// Its lines are its content: the opening fence is the line above.
		kind, line = "fenced code block", line-1
	case *ast.CodeBlock:
		kind = "indented code block"
	case *ast.HTMLBlock:
		kind = "HTML block"
		if block.HTMLBlockType == ast.HTMLBlockType2 {
			kind = "HTML comment"
		}
	}
	return fmt.Sprintf("the %s that starts at line %d", kind, line)
}

var (
	errCopilotNotInstalled       = errors.New("copilot integration not installed")
	errCopilotInstructionsLinked = errors.New("copilot instructions are a symlink")
)

// CheckCopilot reports whether the Copilot CLI integration is installed: the
// plugin manifest exists and .github/copilot-instructions.md holds a current
// beads section. A file without the section, whatever else it holds, is not
// installed. A symlink there is judged by its target, which Copilot reads.
func CheckCopilot() error {
	return checkCopilot(defaultCopilotEnv(""))
}

func checkCopilot(env copilotEnv) error {
	setupCommand := copilotAgentsIntegration.setupCommand
	var missing []string
	if _, err := os.Stat(env.manifestPath); os.IsNotExist(err) {
		missing = append(missing, env.manifestPath)
	} else if err != nil {
		// A manifest bd cannot stat is not one it can report installed.
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to inspect %s: %v\n", env.manifestPath, err)
		return err
	}
	info, lstatErr := os.Lstat(env.instructionsPath)
	if os.IsNotExist(lstatErr) {
		missing = append(missing, env.instructionsPath)
	}
	if len(missing) > 0 {
		_, _ = fmt.Fprintf(env.stdout, "✗ %s integration not installed\n", copilotAgentsIntegration.name)
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s\n", setupCommand)
		for _, path := range missing {
			_, _ = fmt.Fprintf(env.stdout, "  Missing: %s\n", path)
		}
		return errCopilotNotInstalled
	}

	if lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return checkLinkedCopilotInstructions(env)
	}
	if _, err := validateCopilotInstructions(env); err != nil {
		return err
	}
	if err := checkAgents(env.instructionsEnv(), copilotAgentsIntegration); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.stdout, "  File: %s\n", env.manifestPath)
	return nil
}

// checkLinkedCopilotInstructions judges a symlinked instructions file by the
// beads section in its target, as checkAgents judges a CLAUDE.md linked to
// AGENTS.md. Install and --remove leave the link and its target alone, so
// for a target without a current section it points at the target rather
// than at bd setup copilot.
func checkLinkedCopilotInstructions(env copilotEnv) error {
	target := copilotLinkTarget(env.instructionsPath)
	quiet := env.instructionsEnv()
	quiet.stdout = io.Discard
	var problem string
	switch err := checkAgents(quiet, copilotAgentsIntegration); {
	case err == nil:
		_, _ = fmt.Fprintf(env.stdout, "✓ %s integration installed: %s, a symlink to %s (current)\n", copilotAgentsIntegration.name, env.instructionsPath, target)
		_, _ = fmt.Fprintf(env.stdout, "  File: %s\n", env.manifestPath)
		return nil
	case errors.Is(err, errAgentsFileMissing):
		problem = "which does not exist"
	case errors.Is(err, errBeadsSectionMissing):
		problem = "which has no beads section"
	case errors.Is(err, errBeadsSectionStale):
		problem = "whose beads section is stale"
	default:
		return err
	}
	_, _ = fmt.Fprintf(env.stdout, "✗ %s is a symlink to %s, %s\n", env.instructionsPath, target, problem)
	_, _ = fmt.Fprintf(env.stdout, "  bd does not write through the link: give %s a current beads section, or replace the link with a regular file and run: %s\n", target, copilotAgentsIntegration.setupCommand)
	return errCopilotInstructionsLinked
}

// copilotLinkTarget names the file the symlink at link points to, as a path
// from where bd runs. A relative link resolves from the link's own
// directory, so printed as written, "../AGENTS.md" would name a file beside
// the repository. The link is the repository's, so its text is spelled by
// remedy.PrintedValue.
func copilotLinkTarget(link string) string {
	target, _ := os.Readlink(link)
	if target != "" && !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(link), target)
	}
	return remedy.PrintedValue(target)
}

// RemoveCopilot removes the Copilot CLI plugin manifest and the beads section
// of .github/copilot-instructions.md, keeping the rest of that file. A file
// that held only the section, as install creates, is deleted. A symlink, a
// file without the section and one whose markers are malformed are left as
// they are.
func RemoveCopilot() error {
	return removeCopilot(defaultCopilotEnv(""))
}

func removeCopilot(env copilotEnv) error {
	// Refuse malformed markers before removing anything.
	if _, err := validateCopilotInstructions(env); err != nil {
		return err
	}

	removed := false
	if err := os.Remove(env.manifestPath); err == nil {
		removed = true
		_ = RemoveEmptyDir(filepath.Dir(env.manifestPath))
	} else if !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(env.stderr, "Error: remove %s: %v\n", env.manifestPath, err)
		return err
	}

	section, err := copilotInstructionsHaveSection(env)
	if err != nil {
		return err
	}
	if section {
		if err := removeAgents(env.instructionsEnv(), copilotAgentsIntegration); err != nil {
			return err
		}
		removed = true
		// Install created a missing file with the section alone, so a file
		// the section was all of goes with it.
		if data, err := os.ReadFile(env.instructionsPath); err == nil && len(data) == 0 { // #nosec G304 -- bd's own setup destination
			if err := os.Remove(env.instructionsPath); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: remove %s: %v\n", env.instructionsPath, err)
				return err
			}
			_ = RemoveEmptyDir(filepath.Dir(env.instructionsPath))
		}
	}

	if !removed {
		_, _ = fmt.Fprintln(env.stdout, "No integration files found")
		return nil
	}
	_, _ = fmt.Fprintf(env.stdout, "✓ Removed %s integration\n", copilotAgentsIntegration.name)
	return nil
}

// copilotInstructionsHaveSection reports whether the instructions file is a
// regular file holding a beads section. It says so when it leaves a symlink
// alone.
func copilotInstructionsHaveSection(env copilotEnv) (bool, error) {
	info, err := os.Lstat(env.instructionsPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to inspect %s: %v\n", env.instructionsPath, err)
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		_, _ = fmt.Fprintf(env.stdout, "Leaving %s alone: it is a symlink\n", env.instructionsPath)
		return false, nil
	}
	data, err := os.ReadFile(env.instructionsPath) // #nosec G304 -- bd's own setup destination
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", env.instructionsPath, err)
		return false, err
	}
	return containsBeadsMarker(string(data)), nil
}
