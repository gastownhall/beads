package docsync

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// rootAgentsMaxBytes caps the root AGENTS.md. Agent tools load it into every
// session, Codex stops reading project instructions at 32 KiB combined, and
// area-specific rules belong in nested AGENTS.md files instead.
const rootAgentsMaxBytes = 20000

// agentFileSkipDirs are repo-relative directories whose AGENTS.md/CLAUDE.md
// files are shipped to users (product assets), not contributor instructions.
var agentFileSkipDirs = map[string]bool{
	"plugins":            true,
	"internal/templates": true,
}

// backtickPathRE matches a backticked token that names a repository file.
var backtickPathRE = regexp.MustCompile("`([A-Za-z0-9_./-]+\\.(?:md|go|sh|yml|yaml|json|txt))`")

func TestRootAgentsFileWithinBudget(t *testing.T) {
	info, err := os.Stat(filepath.Join(repoRoot(), "AGENTS.md"))
	if err != nil {
		t.Fatalf("stat AGENTS.md: %v", err)
	}
	if info.Size() > rootAgentsMaxBytes {
		t.Errorf("AGENTS.md is %d bytes; keep it at or under %d and move area-specific rules into a nested AGENTS.md listed in its routing table",
			info.Size(), rootAgentsMaxBytes)
	}
}

// contributorAgentFiles walks the checkout for AGENTS.md files outside the
// shipped product-asset directories, returning repo-relative paths.
func contributorAgentFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "testdata" || agentFileSkipDirs[rel]) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "AGENTS.md" {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking checkout: %v", err)
	}
	sort.Strings(files)
	return files
}

// TestAgentInstructionFilesAreLinkedAndRouted: every contributor AGENTS.md has
// a sibling CLAUDE.md symlink (Claude Code skips AGENTS.md files when a
// CLAUDE.md exists above them), every nested one is listed in the root routing
// table, and every local link or backticked file path in them resolves.
func TestAgentInstructionFilesAreLinkedAndRouted(t *testing.T) {
	if bazeltest.IsBazel() {
		t.Skip("walks the source checkout; Bazel runfiles hold only declared data, so this runs under go test")
	}
	root := repoRoot()
	agentFiles := contributorAgentFiles(t, root)
	if len(agentFiles) < 2 {
		t.Fatalf("found %d contributor AGENTS.md files; the walk or the skip list is wrong", len(agentFiles))
	}
	rootAgents, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}

	for _, rel := range agentFiles {
		agentsPath := filepath.Join(root, filepath.FromSlash(rel))
		claudePath := filepath.Join(filepath.Dir(agentsPath), "CLAUDE.md")
		claudeRel := filepath.ToSlash(filepath.Join(filepath.Dir(rel), "CLAUDE.md"))
		info, err := os.Lstat(claudePath)
		switch {
		case err != nil:
			t.Errorf("%s has no sibling CLAUDE.md; add one with `ln -s AGENTS.md CLAUDE.md`", rel)
		case info.Mode()&os.ModeSymlink == 0:
			t.Errorf("%s is a regular file; replace it with a symlink to AGENTS.md", claudeRel)
		default:
			agents, aerr := os.ReadFile(agentsPath)
			claude, cerr := os.ReadFile(claudePath)
			if aerr != nil || cerr != nil || !bytes.Equal(agents, claude) {
				t.Errorf("%s does not resolve to its sibling AGENTS.md", claudeRel)
			}
		}
		if rel != "AGENTS.md" && !bytes.Contains(rootAgents, []byte("("+rel+")")) {
			t.Errorf("%s is not linked from the routing table in the root AGENTS.md", rel)
		}
	}

	checked := append(agentFiles, filepath.ToSlash(filepath.Join(".github", "copilot-instructions.md")))
	var broken []string
	for _, rel := range checked {
		path := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		content := stripCodeFences(string(data))
		for _, target := range extractMarkdownLinks(content) {
			if isExternalLink(target) {
				continue
			}
			if idx := strings.IndexAny(target, "#?"); idx >= 0 {
				target = target[:idx]
			}
			if target == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), filepath.FromSlash(target))); err != nil {
				broken = append(broken, rel+" -> "+target)
			}
		}
		for _, m := range backtickPathRE.FindAllStringSubmatch(content, -1) {
			ref := filepath.FromSlash(m[1])
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), ref)); err == nil {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, ref)); err == nil {
				continue
			}
			broken = append(broken, rel+" -> `"+m[1]+"`")
		}
	}
	if len(broken) > 0 {
		sort.Strings(broken)
		t.Errorf("agent instruction files reference paths that do not exist (%d):", len(broken))
		for _, b := range broken {
			t.Errorf("  %s", b)
		}
	}
}
