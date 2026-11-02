package main

// Shared export-path containment guard for the two automatic export lanes:
// the pre-commit hook (exportJSONLForCommit) and the post-command
// auto-export (maybeAutoExport). A committed .beads/config.yaml can set
// export.path to anything — ../../x, an absolute path, .git/hooks/x — so
// each lane validates the value against the workspace it selected before
// any reconciliation, mkdir, subprocess or staging (bd-1tmym4 / #7888).
//
// Validation writes nothing and never fails the enclosing command: on
// refusal the lanes warn naming export.path and skip.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// exportGitAnchorTimeout bounds the git rev-parse the anchor resolution
// shells out to, matching the git-add timeout used by the same lanes.
const exportGitAnchorTimeout = 5 * time.Second

// exportGitAnchor resolves the Git context of the store directory dir: the
// worktree/repository root containing it ("" outside any repository) and the
// git metadata directories an export must never enter — the git dir and the
// common dir, covering linked-worktree and separate-metadata layouts. The
// process environment is inherited unchanged so a hook's selected Git
// context (GIT_DIR et al.) is the one honored. Never fails: outside a
// repository both results are empty and the caller falls back to the
// store's parent directory as the anchor.
func exportGitAnchor(dir string) (repoRoot string, gitDirs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), exportGitAnchorTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir,
		"rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return "", nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || lines[0] == "" {
		return "", nil
	}
	repoRoot = lines[0]
	seen := make(map[string]bool, 2)
	for _, gd := range lines[1:] {
		if gd != "" && !seen[gd] {
			seen[gd] = true
			gitDirs = append(gitDirs, gd)
		}
	}
	return repoRoot, gitDirs
}

// validatedExportDestination resolves the absolute export destination for
// the lane-selected JSONL directory (beads.FindBeadsDir's result, or
// hookJSONLDir's for the hook lane) and the configured-or-default
// export.path. It returns the same path filepath.Join(jsonlDir, exportPath)
// produced, or a refusal that names export.path.
func validatedExportDestination(jsonlDir, exportPath string) (string, error) {
	repoRoot, gitDirs := exportGitAnchor(jsonlDir)
	return resolveExportPath(jsonlDir, exportPath, repoRoot, gitDirs)
}

// resolveExportPath is the pure validator behind
// validatedExportDestination. jsonlDir is the directory the lane writes in;
// repoRoot the repository/worktree root containing it ("" when the store is
// external or not in a repository); gitDirs the git metadata directories to
// refuse. Every refusal names export.path.
//
// Containment is validated twice — lexically (the joined path against the
// anchor) and through resolution (the deepest existing ancestor of the
// destination is resolved through symlinks, so a linked parent directory
// cannot smuggle the write outside the anchor, and a dangling linked
// parent is refused outright). A leaf symlink at the destination is refused
// whether live or dangling.
func resolveExportPath(jsonlDir, exportPath, repoRoot string, gitDirs []string) (string, error) {
	const key = "export.path"
	if exportPath == "" {
		return "", fmt.Errorf("%s is empty", key)
	}
	if strings.ContainsRune(exportPath, 0) {
		return "", fmt.Errorf("%s contains a NUL byte", key)
	}
	if filepath.IsAbs(exportPath) || filepath.VolumeName(exportPath) != "" {
		return "", fmt.Errorf("%s %q must be a path relative to the workspace", key, exportPath)
	}
	cleaned := filepath.Clean(exportPath)
	if cleaned == "." {
		return "", fmt.Errorf("%s %q names the workspace root itself, not a file", key, exportPath)
	}
	dest := filepath.Join(jsonlDir, cleaned)

	anchors := exportAnchors(jsonlDir, repoRoot)
	if !exportDestInsideAnchors(dest, anchors) {
		return "", fmt.Errorf("%s %q escapes its workspace anchor", key, exportPath)
	}
	// A .git component (any case, either separator) is Git metadata no
	// matter where the real git dir lives.
	for _, part := range strings.FieldsFunc(cleaned, func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.EqualFold(part, ".git") {
			return "", fmt.Errorf("%s %q enters Git metadata (.git)", key, exportPath)
		}
	}
	if fi, err := os.Lstat(dest); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s %q resolves to a symlink at %s; refusing to write through it", key, exportPath, dest)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("%s: inspecting %s: %w", key, dest, err)
	}
	resolvedDest, err := resolveDeepestExisting(dest)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	if !exportDestInsideAnchors(resolvedDest, anchors) {
		return "", fmt.Errorf("%s %q escapes its workspace anchor through symlinked directories", key, exportPath)
	}
	for _, gd := range gitDirs {
		if gd == "" {
			continue
		}
		if exportPathEntersGitDir(dest, resolvedDest, gd) {
			return "", fmt.Errorf("%s %q enters Git metadata at %s", key, exportPath, gd)
		}
	}
	return dest, nil
}

// exportAnchors returns the directories export.path may resolve inside:
// the repository/worktree root containing the store when there is one,
// otherwise the store directory's parent (external stores keep their
// existing ../issues.jsonl behavior, bounded at their parent). A
// deliberately symlinked store contributes its resolved directory as an
// additional anchor — its own contents, not its whole external parent.
func exportAnchors(jsonlDir, repoRoot string) []string {
	jsonlDir = filepath.Clean(jsonlDir)
	anchors := []string{filepath.Dir(jsonlDir)}
	if repoRoot != "" && pathInsideDir(jsonlDir, repoRoot) {
		anchors[0] = filepath.Clean(repoRoot)
	}
	if resolved, err := filepath.EvalSymlinks(jsonlDir); err == nil && resolved != jsonlDir {
		anchors = append(anchors, resolved)
	}
	return anchors
}

// exportDestInsideAnchors reports whether path is strictly inside one of
// anchors, comparing both lexically and against each anchor's resolved
// form. The exact anchor root itself is rejected: the export is a file.
func exportDestInsideAnchors(path string, anchors []string) bool {
	for _, a := range anchors {
		if exportPathStrictlyUnder(path, a) {
			return true
		}
		if resolved, err := filepath.EvalSymlinks(a); err == nil && resolved != a {
			if exportPathStrictlyUnder(path, resolved) {
				return true
			}
		}
	}
	return false
}

// exportPathStrictlyUnder reports whether path is a file strictly under dir.
func exportPathStrictlyUnder(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// exportPathEntersGitDir reports whether the destination (lexical or
// resolved) is or lands inside gitDir (itself compared lexically and
// through symlink aliases).
func exportPathEntersGitDir(dest, resolvedDest, gitDir string) bool {
	dirs := []string{gitDir}
	if resolved, err := filepath.EvalSymlinks(gitDir); err == nil && resolved != gitDir {
		dirs = append(dirs, resolved)
	}
	for _, d := range dirs {
		for _, p := range []string{dest, resolvedDest} {
			if rel, err := filepath.Rel(d, p); err == nil && (rel == "." || filepath.IsLocal(rel)) {
				return true
			}
		}
	}
	return false
}

// resolveDeepestExisting resolves path through the deepest ancestor that
// already exists on disk, so symlinked parent directories are followed and
// containment can be checked on the resolved form. A dangling symlink in
// the parent chain fails resolution; every Lstat error other than absence
// propagates.
func resolveDeepestExisting(path string) (string, error) {
	p := path
	var suffix []string
	for {
		_, lerr := os.Lstat(p)
		if lerr == nil {
			break
		}
		if !os.IsNotExist(lerr) {
			return "", fmt.Errorf("inspecting %s: %w", p, lerr)
		}
		suffix = append([]string{filepath.Base(p)}, suffix...)
		parent := filepath.Dir(p)
		if parent == p {
			return "", fmt.Errorf("no existing ancestor of %s", path)
		}
		p = parent
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("resolving %s (dangling symlink in parent chain?): %w", p, err)
	}
	return filepath.Join(append([]string{resolved}, suffix...)...), nil
}