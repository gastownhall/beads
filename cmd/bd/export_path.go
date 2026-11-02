package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/utils"
)

// resolveExportPath returns the file the automatic export lanes write for
// export.path, or an error when that destination is unsafe. jsonlDir is the
// directory the lane owns: hookJSONLDir(beadsDir) for the pre-commit hook,
// beads.FindBeadsDir() for auto-export. It writes nothing.
//
// The destination must stay inside an anchor: the repository or worktree that
// contains jsonlDir, found from jsonlDir's own Git context (never the
// committing checkout's), or jsonlDir's parent when no repository contains
// it. A symlinked store's resolved directory is an additional anchor. Both
// the lexical path and the path its existing parents resolve to, in their
// stored case, are checked.
// Absolute paths, NUL, leaf symlinks, dangling linked parents, .git
// components and the actual Git directories are refused. A Git directory
// that contains jsonlDir itself (a bare repository's <repo.git>/.beads) or
// the worktree (one checked out inside a bare clone) allows only paths
// inside that directory or worktree.
func resolveExportPath(jsonlDir, exportPath string) (string, error) {
	if exportPath == "" || strings.ContainsRune(exportPath, 0) || filepath.IsAbs(exportPath) || filepath.VolumeName(exportPath) != "" {
		return "", errors.New("it must be a relative file path")
	}
	dir, err := filepath.Abs(jsonlDir)
	if err != nil {
		return "", err
	}
	fullPath := filepath.Join(dir, exportPath)
	if fullPath == dir {
		return "", errors.New("it names the export directory itself")
	}
	if info, err := os.Lstat(fullPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symlink", remedy.PrintedValue(fullPath))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	parent, err := resolveExistingPath(filepath.Dir(fullPath))
	if err != nil {
		return "", err
	}
	resolved := filepath.Join(parent, filepath.Base(fullPath))

	top, gitDirs := exportGitContext(dir)
	anchor, worktree := exportAnchor(dir, top)
	if !exportWithinAnchor(anchor, fullPath) {
		return "", fmt.Errorf("it leaves %s", remedy.PrintedValue(anchor))
	}
	rels := []string{exportRel(anchor, fullPath)}
	for _, a := range []string{anchor, dir} {
		if r, err := resolveExistingPath(a); err == nil && exportWithinAnchor(r, resolved) {
			rels = append(rels, exportRel(r, resolved))
		}
	}
	if len(rels) == 1 {
		return "", fmt.Errorf("it resolves to %s, outside %s", remedy.PrintedValue(resolved), remedy.PrintedValue(anchor))
	}
	for _, rel := range rels {
		for _, component := range strings.Split(rel, string(filepath.Separator)) {
			if strings.EqualFold(component, ".git") {
				return "", errors.New("it enters Git metadata")
			}
		}
	}
	workAreas := exportWorkAreas(dir, anchor, worktree)
	for _, gitDir := range append(gitDirs, hookGitDirs()...) {
		if abs, err := filepath.Abs(gitDir); err == nil {
			gitDir = abs
		}
		if r, err := resolveExistingPath(gitDir); err == nil {
			gitDir = r
		}
		if (resolved == gitDir || exportWithinAnchor(gitDir, resolved)) && !exportInWorkArea(gitDir, resolved, workAreas) {
			return "", fmt.Errorf("it enters Git metadata at %s", remedy.PrintedValue(gitDir))
		}
	}
	return fullPath, nil
}

// exportWorkAreas returns the directories a Git directory may contain without
// them being its metadata: the store where it sits (a symlinked store's link,
// never its target, so a link into a Git directory gains nothing) and the
// worktree Git reported for it, never the parent fallback.
func exportWorkAreas(dir, anchor string, worktree bool) []string {
	var areas []string
	if parent, err := resolveExistingPath(filepath.Dir(dir)); err == nil {
		areas = append(areas, filepath.Join(parent, filepath.Base(dir)))
	}
	if worktree {
		if r, err := resolveExistingPath(anchor); err == nil {
			areas = append(areas, r)
		}
	}
	return areas
}

// exportInWorkArea reports whether path lies inside one of workAreas that
// gitDir contains, the only part of gitDir that is not Git metadata.
func exportInWorkArea(gitDir, path string, workAreas []string) bool {
	for _, area := range workAreas {
		if exportWithinAnchor(gitDir, area) && exportWithinAnchor(area, path) {
			return true
		}
	}
	return false
}

// exportCanonicalPath is utils.CanonicalizePath, indirected so tests can stand
// in for a case-insensitive filesystem. On darwin it returns an existing path
// in its stored case, which filepath.EvalSymlinks keeps as the caller spelled
// it: without it ../META/hooks/pre-commit, which darwin opens inside a Git
// directory at meta, compares as meta's sibling. On Windows EvalSymlinks
// already returns the stored case.
var exportCanonicalPath = utils.CanonicalizePath

// resolveExistingPath resolves the deepest existing ancestor of path, in its
// stored case, and appends the missing components. A dangling symlink, or any
// failure other than absence, is an error.
func resolveExistingPath(path string) (string, error) {
	if _, err := os.Lstat(path); err == nil {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		return exportCanonicalPath(resolved), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path, nil
	}
	resolved, err := resolveExistingPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

// exportGitContext reports the worktree root and Git directories of the
// repository containing dir, discovered from dir itself with the hook's Git
// environment scrubbed. A symlinked store is looked up from its parent, so
// the store's own location decides the repository, not its target.
func exportGitContext(dir string) (top string, gitDirs []string) {
	probe := dir
	for {
		if info, err := os.Lstat(probe); err == nil && info.Mode()&os.ModeSymlink == 0 {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", nil
		}
		probe = parent
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitAddTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", probe, "rev-parse", "--absolute-git-dir", "--git-common-dir", "--show-toplevel")
	cmd.Env = scrubGitHookEnv(os.Environ())
	// A bare repository or a probe inside .git prints its Git directories,
	// then fails on --show-toplevel: keep the directories, drop the root.
	out, err := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i, line := range lines {
		if line == "" || i > 1 {
			continue
		}
		if !filepath.IsAbs(line) {
			line = filepath.Join(probe, line)
		}
		gitDirs = append(gitDirs, line)
	}
	if err == nil && len(lines) == 3 {
		top = lines[2]
	}
	return top, gitDirs
}

// hookGitDirs returns the Git directories a running hook selected through
// GIT_DIR, plus its common directory.
func hookGitDirs() []string {
	gitDir := os.Getenv("GIT_DIR")
	if gitDir == "" {
		return nil
	}
	common := os.Getenv("GIT_COMMON_DIR")
	if common == "" {
		//nolint:gosec // G304: path is GIT_DIR/commondir, a well-known git internal file.
		if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
			if common = strings.TrimSpace(string(data)); common != "" && !filepath.IsAbs(common) {
				common = filepath.Join(gitDir, common)
			}
		}
	}
	if common == "" {
		return []string{gitDir}
	}
	return []string{gitDir, common}
}

// exportAnchor returns the lexical ancestor of dir that is the repository
// root top, and true, or dir's parent and false when no repository contains
// dir. Git for Windows prints top with forward slashes, so the paths are
// compared normalized.
func exportAnchor(dir, top string) (string, bool) {
	if top != "" {
		for a := dir; ; a = filepath.Dir(a) {
			if utils.PathsEqual(a, top) {
				return a, true
			}
			if filepath.Dir(a) == a {
				break
			}
		}
	}
	return filepath.Dir(dir), false
}

// exportWithinAnchor reports whether path is strictly inside root.
func exportWithinAnchor(root, path string) bool {
	rel := exportRel(root, path)
	return rel != "" && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func exportRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	return rel
}
