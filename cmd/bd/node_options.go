package main

import (
	"os"
	"path/filepath"
	"strings"
)

// scrubStaleNodeOptionsRequire drops inherited NODE_OPTIONS --require/-r
// entries whose filesystem-path target no longer exists.
//
// Hosts like cmux inject:
//
//	NODE_OPTIONS=--require=$TMPDIR/cmux-claude-node-options/restore-node-options.cjs
//
// into Claude. When that temp preload disappears, SessionStart/`bd prime`
// crashes with MODULE_NOT_FOUND. Module specifiers (e.g. ts-node/register,
// node:module) are left alone because Node resolves those via require(),
// not as paths. Scrubbing missing require paths keeps unrelated flags
// (e.g. --max-old-space-size) and unsets NODE_OPTIONS when nothing remains.
func scrubStaleNodeOptionsRequire() {
	raw := os.Getenv("NODE_OPTIONS")
	if raw == "" {
		return
	}

	cleaned := scrubStaleNodeOptionsRequireValue(raw)
	if cleaned == "" {
		_ = os.Unsetenv("NODE_OPTIONS")
		return
	}
	_ = os.Setenv("NODE_OPTIONS", cleaned)
}

func scrubStaleNodeOptionsRequireValue(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}

	out := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		tok := fields[i]
		switch {
		case strings.HasPrefix(tok, "--require="), strings.HasPrefix(tok, "-r="):
			target := tok[strings.IndexByte(tok, '=')+1:]
			if target == "" || !keepNodeOptionsRequireTarget(target) {
				continue
			}
			out = append(out, tok)
		case tok == "--require", tok == "-r":
			if i+1 >= len(fields) {
				continue
			}
			target := fields[i+1]
			i++
			if target == "" || strings.HasPrefix(target, "-") || !keepNodeOptionsRequireTarget(target) {
				continue
			}
			out = append(out, tok, target)
		default:
			out = append(out, tok)
		}
	}
	return strings.Join(out, " ")
}

// keepNodeOptionsRequireTarget reports whether a --require/-r target should
// stay in NODE_OPTIONS. Module specifiers are always kept. Filesystem paths
// are kept only when the file still exists.
func keepNodeOptionsRequireTarget(target string) bool {
	if !nodeOptionsRequireIsFilesystemPath(target) {
		return true
	}
	_, err := os.Stat(target)
	return err == nil
}

// nodeOptionsRequireIsFilesystemPath reports whether target is a filesystem
// path in the same sense Node's require() uses: absolute, or relative
// starting with ./ or ../ (and the Windows backslash forms). Everything
// else is a module specifier — including package subpaths like
// ts-node/register and node: protocol builtins.
func nodeOptionsRequireIsFilesystemPath(target string) bool {
	if target == "" {
		return false
	}
	if filepath.IsAbs(target) {
		return true
	}
	return strings.HasPrefix(target, "./") ||
		strings.HasPrefix(target, "../") ||
		strings.HasPrefix(target, `.\`) ||
		strings.HasPrefix(target, `..\`)
}
