package doctor

import (
	"bytes"
	"strings"
)

// HookLanguageKind classifies hook content by the interpreter that runs it,
// which decides whether bd's POSIX-sh integration section may be injected
// into the file (GH#6348).
type HookLanguageKind string

const (
	// HookLanguageShell is a script whose shebang names a POSIX-compatible
	// shell, or plain text with no shebang at all — the formats
	// generateHookSection's sh block can be safely injected into, and the
	// formats bd has always treated that way.
	HookLanguageShell HookLanguageKind = "shell"
	// HookLanguageNonShell is a script whose shebang names an interpreter
	// that cannot execute bd's sh block (python3, node, ruby, ...) or a
	// shebang that cannot be resolved unambiguously. Injecting sh into such
	// a hook does not merely leave the section unreachable: languages that
	// parse the whole file up front fail on it outright. bd skips these
	// conservatively — never inject, warn with manual-integration advice.
	HookLanguageNonShell HookLanguageKind = "non_shell"
	// HookLanguageBinary is content containing NUL bytes. It is never
	// rewritten, whatever its first line says.
	HookLanguageBinary HookLanguageKind = "binary"
)

// HookLanguage is the result of classifying hook content.
type HookLanguage struct {
	Kind HookLanguageKind
	// Interpreter names the shebang's interpreter for diagnostics
	// ("python3", "sh"); empty when there is no shebang.
	Interpreter string
}

// shellHookInterpreters lists the interpreters a POSIX-sh section can be
// safely injected after. Everything else — python3, node, ruby, fish, a
// standalone perl script, ... — cannot execute the block, and appending it
// after that language's own exit call (Python's sys.exit, for example) is a
// syntax error that breaks the whole file, not just the section.
var shellHookInterpreters = map[string]bool{
	"sh":    true,
	"bash":  true,
	"dash":  true,
	"ash":   true,
	"ksh":   true,
	"ksh93": true,
	"mksh":  true,
	"posh":  true,
	"yash":  true,
	"zsh":   true,
}

// ClassifyHookLanguage classifies hook content by the interpreter its
// shebang declares. It is the shared predicate behind hook installation and
// hook migration, both of which used to splice the sh section into any file
// regardless of interpreter (GH#6348).
//
//   - Content containing a NUL byte is binary and never rewritten.
//   - No shebang keeps the historical shell treatment: git executes such
//     hooks with the user's shell, and plain-text hooks have always carried
//     the sh section.
//   - A shebang naming a shell in shellHookInterpreters is shell.
//   - Anything else — a known non-shell interpreter, or a shebang bd cannot
//     resolve unambiguously — is non-shell and must be skipped
//     conservatively.
func ClassifyHookLanguage(content []byte) HookLanguage {
	if bytes.IndexByte(content, 0) >= 0 {
		return HookLanguage{Kind: HookLanguageBinary, Interpreter: "binary content"}
	}
	firstLine := content
	if nl := bytes.IndexByte(content, '\n'); nl >= 0 {
		firstLine = content[:nl]
	}
	// TrimSpace also drops the \r of a CRLF first line and any whitespace
	// between "#!" and the interpreter path ("#! /bin/sh").
	line := strings.TrimSpace(string(firstLine))
	if !strings.HasPrefix(line, "#!") {
		return HookLanguage{Kind: HookLanguageShell}
	}
	fields := strings.Fields(line[2:])
	if len(fields) == 0 {
		// A bare "#!" names no interpreter at all.
		return HookLanguage{Kind: HookLanguageNonShell, Interpreter: "unknown interpreter"}
	}
	program := fields[0]
	rest := fields[1:]
	if hookInterpreterName(program) == "env" {
		idx := envProgramIndex(fields[1:])
		if idx < 0 {
			return HookLanguage{Kind: HookLanguageNonShell, Interpreter: "unknown interpreter"}
		}
		program = fields[1+idx]
		rest = fields[2+idx:]
	}
	name := hookInterpreterName(program)
	if name == "busybox" {
		// busybox dispatches on the applet named in the next field
		// ("#!/bin/busybox sh", "#!/usr/bin/env busybox ash").
		if len(rest) == 0 {
			return HookLanguage{Kind: HookLanguageNonShell, Interpreter: "busybox (no applet named)"}
		}
		name = hookInterpreterName(rest[0])
	}
	if shellHookInterpreters[name] {
		return HookLanguage{Kind: HookLanguageShell, Interpreter: name}
	}
	return HookLanguage{Kind: HookLanguageNonShell, Interpreter: name}
}

// hookInterpreterName reduces a shebang token to the interpreter name: the
// last path segment for both Unix and Windows separators, with a .exe
// suffix (case-insensitive) stripped for Windows installs.
func hookInterpreterName(path string) string {
	name := path
	if i := strings.LastIndexAny(name, `/\\`); i >= 0 {
		name = name[i+1:]
	}
	if len(name) > len(".exe") && strings.EqualFold(name[len(name)-len(".exe"):], ".exe") {
		name = name[:len(name)-len(".exe")]
	}
	return name
}

// envProgramIndex resolves which token after "env" names the program to run,
// returning its index in fields or -1 when the shebang cannot be resolved
// unambiguously. Ordinary environment assignments (FOO=bar) and the known
// env options are stepped over; -S's payload is scanned the same way, because
// in a shebang everything after -S forms the command line env re-splits. An
// unrecognized option is ambiguous rather than guessed at: a wrong guess
// either corrupts a foreign hook or silently skips a shell one.
func envProgramIndex(fields []string) int {
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "-S" || f == "--split-string" || strings.HasPrefix(f, "--split-string="):
			// The -S string (the rest of the shebang line) is the command
			// line env will run: keep scanning it as assignments plus
			// program.
			continue
		case f == "-u" || f == "--unset":
			// The option and the NAME it unsets.
			i++
			continue
		case f == "-i" || f == "--ignore-environment" || f == "-":
			continue
		case f == "--":
			// End of options: the next field, if any, is the program.
			if i+1 < len(fields) {
				return i + 1
			}
			return -1
		case strings.HasPrefix(f, "-"):
			// Unknown option: it may or may not consume the next field.
			return -1
		case strings.Contains(f, "=") && !strings.HasPrefix(f, "="):
			// Ordinary environment assignment.
			continue
		default:
			return i
		}
	}
	return -1
}

// IsBdHooksRunIntegration reports whether content delegates to bd via a
// "bd hooks run" call — the hook shape bd prescribes for external hook
// managers (GH#946). A non-shell hook in this shape is already a working
// integration: bd leaves it untouched and silent.
func IsBdHooksRunIntegration(content string) bool {
	return bdHooksRunPattern.MatchString(content)
}
