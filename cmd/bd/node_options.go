package main

import (
	"os"
	"strings"
)

// scrubStaleNodeOptionsRequire drops inherited NODE_OPTIONS --require/-r
// entries whose target file no longer exists.
//
// Hosts like cmux inject:
//
//	NODE_OPTIONS=--require=$TMPDIR/cmux-claude-node-options/restore-node-options.cjs
//
// into Claude. When that temp preload disappears, SessionStart/`bd prime`
// crashes with MODULE_NOT_FOUND. Scrubbing missing require paths keeps
// unrelated flags (e.g. --max-old-space-size) and unsets NODE_OPTIONS when
// nothing remains.
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
			path := tok[strings.IndexByte(tok, '=')+1:]
			if path == "" || !nodeOptionsRequireTargetExists(path) {
				continue
			}
			out = append(out, tok)
		case tok == "--require", tok == "-r":
			if i+1 >= len(fields) {
				continue
			}
			path := fields[i+1]
			i++
			if path == "" || strings.HasPrefix(path, "-") || !nodeOptionsRequireTargetExists(path) {
				continue
			}
			out = append(out, tok, path)
		default:
			out = append(out, tok)
		}
	}
	return strings.Join(out, " ")
}

func nodeOptionsRequireTargetExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
