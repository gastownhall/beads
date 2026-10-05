// Package ceiling bounds the upward directory searches bd uses to discover a
// .beads workspace or its config.yaml.
//
// BEADS_CEILING_DIRECTORIES is a list of absolute directories separated by the
// OS path-list separator (":" on POSIX, ";" on Windows), like git's
// GIT_CEILING_DIRECTORIES. A search that starts inside a ceiling never examines
// the ceiling itself or anything above it; the start directory is always
// examined. Ceilings that do not contain the start have no effect, and empty or
// relative entries are ignored. When the variable is unset nothing changes.
//
// It exists for sandboxes such as `bazel test`, whose working directory lives
// below the developer's home: without a ceiling, discovery walks out of the
// sandbox and reads or writes the developer's own ~/.beads.
package ceiling

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvVar names the ceiling list.
const EnvVar = "BEADS_CEILING_DIRECTORIES"

// Bound is the ceiling of one upward search. A nil Bound permits every
// directory, so callers can use it unconditionally.
type Bound struct {
	start    []string
	ceilings []string
}

// For returns the bound for a search starting at start, or nil when
// BEADS_CEILING_DIRECTORIES names no ceiling containing start.
func For(start string) *Bound {
	list := os.Getenv(EnvVar)
	if list == "" {
		return nil
	}
	startForms := forms(start)
	var ceilings []string
	for _, entry := range filepath.SplitList(list) {
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		for _, c := range forms(entry) {
			if anyWithin(startForms, c) {
				ceilings = append(ceilings, c)
			}
		}
	}
	if len(ceilings) == 0 {
		return nil
	}
	return &Bound{start: startForms, ceilings: ceilings}
}

// Excludes reports whether the search must stop before examining dir: dir is
// not the start and is a ceiling or lies above one.
func (b *Bound) Excludes(dir string) bool {
	if b == nil {
		return false
	}
	dirForms := forms(dir)
	for _, d := range dirForms {
		for _, s := range b.start {
			if d == s {
				return false
			}
		}
	}
	for _, c := range b.ceilings {
		for _, d := range dirForms {
			if within(c, d) {
				return true
			}
		}
	}
	return false
}

// GitEnv returns env with GIT_CEILING_DIRECTORIES set to the beads ceilings,
// for git probes that locate a workspace (and have scrubbed inherited git
// routing). env is returned unchanged when BEADS_CEILING_DIRECTORIES is unset.
func GitEnv(env []string) []string {
	list := os.Getenv(EnvVar)
	if list == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, "GIT_CEILING_DIRECTORIES=") {
			out = append(out, e)
		}
	}
	return append(out, "GIT_CEILING_DIRECTORIES="+list)
}

// forms returns the cleaned absolute path and, when it differs, its
// symlink-resolved form, so a ceiling and a walk that spell the same directory
// differently still meet.
func forms(path string) []string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return []string{filepath.Clean(path)}
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != abs {
		return []string{abs, resolved}
	}
	return []string{abs}
}

// anyWithin reports whether any of paths is parent or lies below it.
func anyWithin(paths []string, parent string) bool {
	for _, p := range paths {
		if within(p, parent) {
			return true
		}
	}
	return false
}

func within(path, parent string) bool {
	if path == parent {
		return true
	}
	if !strings.HasSuffix(parent, string(filepath.Separator)) {
		parent += string(filepath.Separator)
	}
	return strings.HasPrefix(path, parent)
}
