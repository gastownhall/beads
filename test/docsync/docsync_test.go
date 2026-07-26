// Package docsync keeps the Mintlify site under docs/ and its navigation in
// docs/docs.json in exact correspondence, and keeps local markdown links
// resolving across the documentation trees.
//
// Modeled on the Gas City docsync guard: nav entries must point at real
// files, every published page must be in the nav (no orphans), and links are
// checked with the convention of the tree they live in — docs/ links are
// root-relative and extensionless (Mintlify routes), while engdocs/ and root
// markdown are GitHub-viewed and need exact file paths.
package docsync

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func repoRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

var markdownLinkRE = regexp.MustCompile(`\[[^][]*\]\(([^)]+)\)`)

// docsPublishExemptions lists markdown files under docs/ that are allowed to
// exist without a docs.json navigation entry. Keep this list tiny: moved
// pages get an entry in the docs.json redirects array, not a pointer stub
// (engdocs/decisions/2026-07-10-mintlify-docs-overhaul.md, decision 6).
var docsPublishExemptions = map[string]bool{
	"CLI_REFERENCE.md": true, // generated single-file reference (bd help --all)
	// Deliberate exception to decision 6: released bd binaries print this
	// path (v1.1.0 printAncestorPKMismatchGuidance emits
	// docs/RECOVERY.md#pk-fork-refused), and github.com blob URLs cannot be
	// redirected. Keep until no supported release prints it.
	"RECOVERY.md": true,
}

// rootDocFiles are the curated root-level markdown files whose local links
// are checked GitHub-style. CHANGELOG.md is deliberately absent: its
// historical entries reference files that no longer exist.
var rootDocFiles = []string{
	"README.md",
	"AGENTS.md",
	"AGENT_INSTRUCTIONS.md",
	"CONTRIBUTING.md",
	"CLAUDE.md",
	"RELEASING.md",
	"PR_MAINTAINER_GUIDELINES.md",
}

func collectMintPages(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if k == "pages" {
				if arr, ok := child.([]any); ok {
					for _, item := range arr {
						if s, ok := item.(string); ok {
							*out = append(*out, s)
						}
					}
				}
			}
			collectMintPages(child, out)
		}
	case []any:
		for _, child := range x {
			collectMintPages(child, out)
		}
	}
}

func mintNavPages(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(), "docs", "docs.json"))
	if err != nil {
		t.Fatalf("reading docs.json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("parsing docs.json: %v", err)
	}
	var pages []string
	collectMintPages(decoded, &pages)
	sort.Strings(pages)
	return pages
}

func collectMarkdownFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext == ".md" || ext == ".mdx" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func extractMarkdownLinks(content string) []string {
	matches := markdownLinkRE.FindAllStringSubmatchIndex(content, -1)
	var links []string
	for _, m := range matches {
		start := m[0]
		if start > 0 && content[start-1] == '!' {
			continue // image
		}
		target := strings.TrimSpace(content[m[2]:m[3]])
		target = strings.Trim(target, "<>")
		if target == "" {
			continue
		}
		if idx := strings.Index(target, ` "`); idx >= 0 {
			target = target[:idx]
		}
		links = append(links, target)
	}
	return links
}

// stripCodeFences removes fenced code blocks so links inside examples are
// not checked.
func stripCodeFences(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inFence := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func isExternalLink(target string) bool {
	switch {
	case strings.HasPrefix(target, "http://"),
		strings.HasPrefix(target, "https://"),
		strings.HasPrefix(target, "mailto:"),
		strings.HasPrefix(target, "tel:"),
		strings.HasPrefix(target, "#"):
		return true
	default:
		return false
	}
}

func hasMdSuffix(target string) bool {
	path := target
	if idx := strings.IndexAny(path, "#?"); idx >= 0 {
		path = path[:idx]
	}
	ext := filepath.Ext(path)
	return ext == ".md" || ext == ".mdx"
}

// routeExists reports whether a root-relative extensionless Mintlify route
// (e.g. /getting-started/quickstart) resolves to a page under docs/.
func routeExists(docsRoot, route string) bool {
	rel := filepath.FromSlash(strings.TrimPrefix(route, "/"))
	if rel == "" {
		rel = "index"
	}
	for _, try := range []string{
		filepath.Join(docsRoot, rel+".md"),
		filepath.Join(docsRoot, rel+".mdx"),
		filepath.Join(docsRoot, rel, "index.md"),
		filepath.Join(docsRoot, rel, "index.mdx"),
		filepath.Join(docsRoot, rel), // static assets (images)
	} {
		if info, err := os.Stat(try); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// TestMintNavigationPagesExist: every docs.json navigation entry points at a
// real page under docs/.
func TestMintNavigationPagesExist(t *testing.T) {
	root := repoRoot()
	var missing []string
	for _, page := range mintNavPages(t) {
		path := filepath.Join(root, "docs", filepath.FromSlash(page))
		if filepath.Ext(path) == "" {
			path += ".md"
		}
		if _, err := os.Stat(path); err != nil {
			mdx := strings.TrimSuffix(path, ".md") + ".mdx"
			if _, err2 := os.Stat(mdx); err2 != nil {
				missing = append(missing, page)
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("docs.json references missing pages:")
		for _, page := range missing {
			t.Errorf("  %s", page)
		}
	}
}

// TestEveryDocsPageIsPublished: docs/ holds only published pages — every
// markdown file must appear in docs.json navigation (or the tiny stub
// allowlist). Engineering docs belong in engdocs/.
func TestEveryDocsPageIsPublished(t *testing.T) {
	root := repoRoot()
	docsRoot := filepath.Join(root, "docs")

	navPages := make(map[string]bool)
	for _, p := range mintNavPages(t) {
		navPages[strings.TrimSuffix(filepath.ToSlash(p), filepath.Ext(p))] = true
	}

	files, err := collectMarkdownFiles(docsRoot)
	if err != nil {
		t.Fatalf("collecting docs markdown: %v", err)
	}

	var orphans []string
	for _, path := range files {
		rel, err := filepath.Rel(docsRoot, path)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		if docsPublishExemptions[rel] {
			continue
		}
		pageForm := strings.TrimSuffix(rel, filepath.Ext(rel))
		if !navPages[pageForm] {
			orphans = append(orphans, rel)
		}
	}

	if len(orphans) > 0 {
		t.Errorf("docs/ markdown files not referenced in docs.json navigation.\n" +
			"docs/ publishes via Mintlify, so every page must be in the nav.\n" +
			"Add the file to docs/docs.json navigation (published page) or move it\n" +
			"under engdocs/ (engineering doc):")
		for _, o := range orphans {
			t.Errorf("  docs/%s", o)
		}
	}
}

// TestDocsSiteLinks: published docs/ pages use Mintlify link conventions —
// internal links are root-relative and extensionless, and every route
// resolves. Pointer stubs (GitHub-viewed) use exact relative paths instead.
func TestDocsSiteLinks(t *testing.T) {
	root := repoRoot()
	docsRoot := filepath.Join(root, "docs")

	files, err := collectMarkdownFiles(docsRoot)
	if err != nil {
		t.Fatalf("collecting docs markdown: %v", err)
	}

	var broken []string
	for _, path := range files {
		rel, _ := filepath.Rel(docsRoot, path)
		rel = filepath.ToSlash(rel)
		isStub := docsPublishExemptions[rel]

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, target := range extractMarkdownLinks(stripCodeFences(string(data))) {
			if isExternalLink(target) {
				continue
			}
			if isStub {
				// GitHub-viewed pointer stub: relative path must exist.
				resolved := target
				if idx := strings.IndexAny(resolved, "#?"); idx >= 0 {
					resolved = resolved[:idx]
				}
				if resolved == "" {
					continue
				}
				abs := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(resolved)))
				if _, err := os.Stat(abs); err != nil {
					broken = append(broken, "docs/"+rel+" -> "+target)
				}
				continue
			}
			if hasMdSuffix(target) {
				broken = append(broken, "docs/"+rel+" -> "+target+" (.md suffix breaks Mintlify routes; link extensionless)")
				continue
			}
			if !strings.HasPrefix(target, "/") {
				broken = append(broken, "docs/"+rel+" -> "+target+" (published pages use root-relative links)")
				continue
			}
			route := target
			if idx := strings.IndexAny(route, "#?"); idx >= 0 {
				route = route[:idx]
			}
			if !routeExists(docsRoot, route) {
				broken = append(broken, "docs/"+rel+" -> "+target)
			}
		}
	}

	if len(broken) > 0 {
		sort.Strings(broken)
		t.Errorf("broken or misconvention links in docs/ (%d):", len(broken))
		for _, b := range broken {
			t.Errorf("  %s", b)
		}
	}
}

// TestEngdocsAndRootMarkdownLinks: engdocs/ and curated root markdown are
// GitHub-viewed, so local links must be exact file paths.
func TestEngdocsAndRootMarkdownLinks(t *testing.T) {
	root := repoRoot()

	var files []string
	engdocs, err := collectMarkdownFiles(filepath.Join(root, "engdocs"))
	if err != nil {
		t.Fatalf("collecting engdocs markdown: %v", err)
	}
	files = append(files, engdocs...)
	for _, name := range rootDocFiles {
		path := filepath.Join(root, name)
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
		}
	}

	var broken []string
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, target := range extractMarkdownLinks(stripCodeFences(string(data))) {
			if isExternalLink(target) {
				continue
			}
			resolved := target
			if idx := strings.IndexAny(resolved, "#?"); idx >= 0 {
				resolved = resolved[:idx]
			}
			if resolved == "" {
				continue
			}
			abs := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(resolved)))
			if _, err := os.Stat(abs); err != nil {
				relPath, _ := filepath.Rel(root, path)
				broken = append(broken, filepath.ToSlash(relPath)+" -> "+target)
			}
		}
	}

	if len(broken) > 0 {
		sort.Strings(broken)
		t.Errorf("broken local markdown links (%d) — engdocs/ and root files are GitHub-viewed and need exact paths:", len(broken))
		for _, b := range broken {
			t.Errorf("  %s", b)
		}
	}
}

// TestMintRedirectsResolve: every entry in the docs.json redirects array must
// be well-formed and point at a page that exists. Nothing else validates the
// redirects, so a future page rename would otherwise dangle its redirect
// invisibly. Dynamic destinations (path-to-regexp `:slug*` wildcards) are
// checked for shape only.
func TestMintRedirectsResolve(t *testing.T) {
	root := repoRoot()
	data, err := os.ReadFile(filepath.Join(root, "docs", "docs.json"))
	if err != nil {
		t.Fatalf("reading docs.json: %v", err)
	}
	var decoded struct {
		Redirects []struct {
			Source      string `json:"source"`
			Destination string `json:"destination"`
		} `json:"redirects"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("parsing docs.json: %v", err)
	}
	if len(decoded.Redirects) == 0 {
		t.Fatal("docs.json has no redirects array; the old-route coverage is gone")
	}

	navPages := make(map[string]bool)
	for _, p := range mintNavPages(t) {
		navPages[strings.TrimSuffix(filepath.ToSlash(p), filepath.Ext(p))] = true
	}

	seen := make(map[string]bool)
	for _, r := range decoded.Redirects {
		if !strings.HasPrefix(r.Source, "/") || !strings.HasPrefix(r.Destination, "/") {
			t.Errorf("redirect %q -> %q: source and destination must be root-relative", r.Source, r.Destination)
			continue
		}
		if seen[r.Source] {
			t.Errorf("duplicate redirect source %q", r.Source)
		}
		seen[r.Source] = true

		if strings.Contains(r.Destination, ":") {
			// Wildcard destination; requires a wildcard source.
			if !strings.Contains(r.Source, ":") {
				t.Errorf("redirect %q -> %q: static source with dynamic destination", r.Source, r.Destination)
			}
			continue
		}
		dest := strings.TrimPrefix(r.Destination, "/")
		if navPages[dest] {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "docs", filepath.FromSlash(dest)+".md")); err != nil {
			t.Errorf("redirect %q -> %q: destination page does not exist", r.Source, r.Destination)
		}
	}
}
