//go:build linux

package git

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// discoveryEnvOverrides are the environment variables that change how Git
// finds or validates a repository. When any is present, discovery is left to
// git itself. The list is deliberately broader than strictly necessary: a
// fallback costs one subprocess, a wrong answer costs a wrong workspace.
var discoveryEnvOverrides = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_COMMON_DIR",
	"GIT_CEILING_DIRECTORIES",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",
	"GIT_OBJECT_DIRECTORY",
	"GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT",
	"GIT_TEST_ASSUME_DIFFERENT_OWNER",
}

// discoverGitInProcess answers `git rev-parse --git-dir --git-common-dir
// --show-toplevel` for the process working directory without running git. ok
// is false whenever the layout is outside what it reproduces exactly; the
// caller then runs git.
func discoverGitInProcess() (revParseResult, bool) {
	for _, name := range discoveryEnvOverrides {
		if _, set := os.LookupEnv(name); set {
			return revParseResult{}, false
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return revParseResult{}, false
	}
	return discoverGitFrom(wd)
}

// discoverGitFrom mirrors setup_git_directory_gently's discovery walk (Git
// 2.x) from cwd. Git walks the physical directory (getcwd), checks each
// directory's .git as a gitfile or a git directory, then the directory itself
// as a bare repository, and stops at the first filesystem boundary.
func discoverGitFrom(cwd string) (revParseResult, bool) {
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil || !filepath.IsAbs(cwd) {
		return revParseResult{}, false
	}
	cwd = filepath.Clean(cwd)
	cwdStat := statOf(cwd)
	if cwdStat == nil {
		return revParseResult{}, false
	}

	for dir := cwd; ; {
		dotGit := filepath.Join(dir, ".git")
		info, err := os.Lstat(dotGit)
		switch {
		case err == nil && info.Mode().IsDir():
			return discoveredGitDir(cwd, dir, dotGit)
		case err == nil && info.Mode().IsRegular():
			return discoveredGitFile(dir, dotGit)
		case err == nil:
			// Symlinked .git, sockets, ...: let git decide.
			return revParseResult{}, false
		case !os.IsNotExist(err):
			return revParseResult{}, false
		}
		// The directory itself may be a git directory (a bare repository, or
		// somewhere inside a .git directory); git answers those differently.
		if looksLikeGitDir(dir) {
			return revParseResult{}, false
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return revParseResult{notRepo: true}, true
		}
		parentStat := statOf(parent)
		if parentStat == nil {
			return revParseResult{}, false
		}
		if parentStat.Dev != cwdStat.Dev {
			// Git stops at a mount point unless
			// GIT_DISCOVERY_ACROSS_FILESYSTEM is set (handled by the caller).
			return revParseResult{notRepo: true}, true
		}
		dir = parent
	}
}

// discoveredGitDir handles <top>/.git being a directory.
func discoveredGitDir(cwd, top, gitDir string) (revParseResult, bool) {
	// A .git directory carrying a commondir file is unusual; leave it to git.
	if exists(filepath.Join(gitDir, "commondir")) {
		return revParseResult{}, false
	}
	if !isGitDirectory(gitDir, gitDir) {
		// Git would keep walking upward past an invalid .git directory.
		return revParseResult{}, false
	}
	if !ownedByCurrentUser(top) || !ownedByCurrentUser(gitDir) {
		return revParseResult{}, false
	}
	if worktree, ok := repoConfigAllowsDiscovery(gitDir); !ok || worktree != "" {
		return revParseResult{}, false
	}
	if cwd == top {
		return revParseResult{gitDir: ".git", commonDir: ".git", topLevel: top}, true
	}
	// From a subdirectory Git prints the git directory absolute and the common
	// directory relative to the working directory.
	rel, err := filepath.Rel(top, cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return revParseResult{}, false
	}
	depth := len(strings.Split(rel, string(filepath.Separator)))
	return revParseResult{
		gitDir:    gitDir,
		commonDir: strings.Repeat("../", depth) + ".git",
		topLevel:  top,
	}, true
}

// discoveredGitFile handles <top>/.git being a gitfile ("gitdir: <path>"), as
// in linked worktrees and submodules.
func discoveredGitFile(top, gitFile string) (revParseResult, bool) {
	data, err := os.ReadFile(gitFile) // #nosec G304 -- the .git file Git itself would read during discovery
	if err != nil {
		return revParseResult{}, false
	}
	content := strings.TrimRight(string(data), "\r\n")
	target, found := strings.CutPrefix(content, "gitdir: ")
	if !found || target == "" || strings.ContainsAny(target, "\n\r\x00") {
		return revParseResult{}, false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(top, target)
	}
	gitDir, err := filepath.EvalSymlinks(target)
	if err != nil {
		return revParseResult{}, false
	}
	commonDir := gitDir
	if raw, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimRight(string(raw), "\r\n")
		if common == "" || strings.ContainsAny(common, "\n\r\x00") {
			return revParseResult{}, false
		}
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		if commonDir, err = filepath.EvalSymlinks(common); err != nil {
			return revParseResult{}, false
		}
	} else if !os.IsNotExist(err) {
		return revParseResult{}, false
	}
	if !isGitDirectory(gitDir, commonDir) {
		return revParseResult{}, false
	}
	if !ownedByCurrentUser(gitFile) || !ownedByCurrentUser(top) || !ownedByCurrentUser(gitDir) {
		return revParseResult{}, false
	}
	worktree, ok := repoConfigAllowsDiscovery(commonDir)
	if !ok {
		return revParseResult{}, false
	}
	if worktree != "" && !submoduleWorktreeIs(gitDir, commonDir, worktree, top) {
		return revParseResult{}, false
	}
	// Git prints both absolute from anywhere in the work tree.
	return revParseResult{gitDir: gitDir, commonDir: commonDir, topLevel: top}, true
}

// isGitDirectory mirrors Git's is_git_directory: a valid HEAD in the git
// directory, objects/ and refs/ in the common directory.
func isGitDirectory(gitDir, commonDir string) bool {
	if !validHeadRef(filepath.Join(gitDir, "HEAD")) {
		return false
	}
	return isDir(filepath.Join(commonDir, "objects")) && isDir(filepath.Join(commonDir, "refs"))
}

// looksLikeGitDir is a cheap superset test for "git might treat dir as a git
// directory"; a hit only means discovery is handed to git.
func looksLikeGitDir(dir string) bool {
	return exists(filepath.Join(dir, "HEAD")) &&
		(exists(filepath.Join(dir, "objects")) || exists(filepath.Join(dir, "commondir")))
}

// validHeadRef accepts the HEAD forms Git's validate_headref does for the
// files it writes itself: "ref: refs/..." or a full object id. Symlinked HEAD
// (an ancient layout) is left to git.
func validHeadRef(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- HEAD of the discovered git directory
	if err != nil {
		return false
	}
	if ref, ok := bytes.CutPrefix(data, []byte("ref:")); ok {
		return bytes.HasPrefix(bytes.TrimLeft(ref, " \t"), []byte("refs/"))
	}
	line := strings.TrimRight(string(data), "\r\n")
	if len(line) != 40 && len(line) != 64 {
		return false
	}
	for _, c := range line {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// submoduleWorktreeIs reports whether a gitfile repository's core.worktree
// names exactly the directory holding the gitfile. Git writes that for every
// submodule (core.worktree = ../../../sub in .git/modules/sub/config), and Git
// then resolves the work tree by chdir(gitdir) + chdir(core.worktree), which
// lands on top again. Only that plain shape is accepted: a separate git
// directory (no shared common directory) and a value of leading "../"
// segments followed by plain names, which resolves lexically from the
// already-physical git directory exactly as the chdirs do.
func submoduleWorktreeIs(gitDir, commonDir, worktree, top string) bool {
	if gitDir != commonDir || filepath.IsAbs(worktree) {
		return false
	}
	rest := filepath.ToSlash(filepath.Clean(worktree))
	if rest != worktree && rest+"/" != worktree {
		return false
	}
	for strings.HasPrefix(rest, "../") {
		rest = rest[len("../"):]
	}
	for _, part := range strings.Split(rest, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(gitDir, worktree))
	return err == nil && resolved == top
}

// repoConfigAllowsDiscovery reports whether the repository config leaves
// Git's answer to the plain discovery result, and returns core.worktree when
// it is set (the caller decides whether it is the submodule shape). Git
// consults only the repository's own config file here
// (read_repository_format), so global and system config cannot move the work
// tree. Anything else that could — core.bare, extensions (worktreeConfig,
// unknown ones Git rejects), a format version Git refuses, include
// directives, a repeated core.worktree, syntax this scanner does not model —
// sends discovery to git.
func repoConfigAllowsDiscovery(commonDir string) (worktree string, ok bool) {
	f, err := os.Open(filepath.Join(commonDir, "config")) // #nosec G304 -- the discovered repository config
	if os.IsNotExist(err) {
		return "", true
	}
	if err != nil {
		return "", false
	}
	defer f.Close()

	section := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			return "", false // continuation lines are not modeled
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return "", false
			}
			header := strings.TrimSpace(line[1:end])
			if strings.ContainsAny(header, " \t\"") {
				section = "" // a subsection: [remote "origin"], [branch "main"], ...
				if name := strings.ToLower(strings.Fields(header)[0]); strings.HasPrefix(name, "include") {
					return "", false
				}
			} else {
				section = strings.ToLower(header)
			}
			if strings.HasPrefix(section, "include") || section == "extensions" {
				return "", false
			}
			line = strings.TrimSpace(line[end+1:])
			if line == "" || line[0] == '#' || line[0] == ';' {
				continue
			}
		}
		if section != "core" {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		rawValue := value
		value = strings.ToLower(strings.Trim(strings.TrimSpace(value), "\""))
		switch key {
		case "worktree":
			rawValue = strings.TrimSpace(rawValue)
			if worktree != "" || rawValue == "" || strings.ContainsAny(rawValue, "\\\"#;") {
				return "", false
			}
			worktree = rawValue
		case "bare":
			if value != "false" && value != "no" && value != "off" && value != "0" {
				return "", false
			}
		case "repositoryformatversion":
			if v, err := strconv.Atoi(value); err != nil || v < 0 || v > 1 {
				return "", false
			}
		}
	}
	return worktree, scanner.Err() == nil
}

// ownedByCurrentUser mirrors Git's is_path_owned_by_current_uid (lstat, owner
// equals the effective uid). Git's sudo allowance (root with SUDO_UID) is left
// to git. A path owned by someone else engages safe.directory, which only git
// evaluates.
func ownedByCurrentUser(path string) bool {
	euid := os.Geteuid()
	if euid == 0 {
		if _, set := os.LookupEnv("SUDO_UID"); set {
			return false
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == euid
}

// statOf returns path's stat data (following symlinks, as Git's
// get_device_or_die does), or nil.
func statOf(path string) *syscall.Stat_t {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	st, _ := info.Sys().(*syscall.Stat_t)
	return st
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
