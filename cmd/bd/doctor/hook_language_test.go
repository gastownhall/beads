package doctor

import (
	"testing"
)

// TestNonShellHookInterpreter pins the shared interpreter classifier behind
// the non-shell hook policy (GH#6348): bd may only inject its POSIX-sh
// section into hooks a shell actually runs, and must skip anything it cannot
// resolve unambiguously — an appended sh block in a Python or Node hook is a
// syntax error that breaks the whole file, not just an unreachable section.
func TestNonShellHookInterpreter(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantKind   HookLanguageKind
		wantInterp string
	}{
		// Plain text with no shebang keeps the historical shell treatment.
		{"no shebang plain text", "echo hi\n", HookLanguageShell, ""},
		{"empty content", "", HookLanguageShell, ""},
		{"shell direct path", "#!/bin/sh\necho hi\n", HookLanguageShell, "sh"},
		{"shell direct path with flag", "#!/bin/sh -e\nset -x\n", HookLanguageShell, "sh"},
		{"bash via env", "#!/usr/bin/env bash\necho hi\n", HookLanguageShell, "bash"},
		{"whitespace after shebang", "#! /bin/sh\necho hi\n", HookLanguageShell, "sh"},
		{"tab after shebang", "#!\t/bin/sh\necho hi\n", HookLanguageShell, "sh"},
		{"CRLF first line", "#!/bin/sh\r\necho hi\r\n", HookLanguageShell, "sh"},
		{"dash", "#!/usr/bin/env dash\nset -e\n", HookLanguageShell, "dash"},
		{"ash", "#!/bin/ash\nset -e\n", HookLanguageShell, "ash"},
		{"ksh", "#!/usr/bin/env ksh\necho hi\n", HookLanguageShell, "ksh"},
		{"ksh93", "#!/usr/bin/ksh93\necho hi\n", HookLanguageShell, "ksh93"},
		{"mksh", "#!/bin/mksh\necho hi\n", HookLanguageShell, "mksh"},
		{"posh", "#!/usr/bin/env posh\necho hi\n", HookLanguageShell, "posh"},
		{"yash", "#!/bin/yash\necho hi\n", HookLanguageShell, "yash"},
		{"zsh", "#!/usr/bin/env zsh\necho hi\n", HookLanguageShell, "zsh"},
		{"shell .exe basename", "#!/usr/bin/bin/sh.exe\necho hi\n", HookLanguageShell, "sh"},
		{"windows path sh.exe", "#!/C/Git/usr/bin/sh.exe\necho hi\n", HookLanguageShell, "sh"},
		{"env -S bash -e", "#!/usr/bin/env -S bash -e\necho hi\n", HookLanguageShell, "bash"},
		{"env -S with assignment", "#!/usr/bin/env -S BD_GIT_HOOK=1 sh\necho hi\n", HookLanguageShell, "sh"},
		{"env assignment then shell", "#!/usr/bin/env FOO=bar /bin/sh\necho hi\n", HookLanguageShell, "sh"},
		{"env -u assignment shell", "#!/usr/bin/env -u FOO sh\necho hi\n", HookLanguageShell, "sh"},
		{"env -i shell", "#!/usr/bin/env -i sh\necho hi\n", HookLanguageShell, "sh"},
		{"env double dash shell", "#!/usr/bin/env -- sh\necho hi\n", HookLanguageShell, "sh"},
		{"busybox sh direct", "#!/bin/busybox sh\necho hi\n", HookLanguageShell, "sh"},
		{"busybox ash via env", "#!/usr/bin/env busybox ash\necho hi\n", HookLanguageShell, "ash"},

		// Non-shell interpreters: skip, never inject.
		{"python3 via env", "#!/usr/bin/env python3\nimport sys\n", HookLanguageNonShell, "python3"},
		{"python direct path", "#!/usr/bin/python3\nimport sys\n", HookLanguageNonShell, "python3"},
		{"node via env", "#!/usr/bin/env node\nconsole.log('hi')\n", HookLanguageNonShell, "node"},
		{"ruby via env", "#!/usr/bin/env ruby\nputs 'hi'\n", HookLanguageNonShell, "ruby"},
		{"perl standalone", "#!/usr/bin/perl\nprint \"hi\";\n", HookLanguageNonShell, "perl"},
		{"fish", "#!/usr/bin/fish\necho hi\n", HookLanguageNonShell, "fish"},
		{"pythonw", "#!/usr/bin/env pythonw\nimport sys\n", HookLanguageNonShell, "pythonw"},

		// Ambiguous wrappers: skip conservatively rather than guess.
		{"env with no program", "#!/usr/bin/env\n", HookLanguageNonShell, "unknown interpreter"},
		{"bare shebang", "#!\n", HookLanguageNonShell, "unknown interpreter"},
		{"env with unknown option", "#!/usr/bin/env -X sh\necho hi\n", HookLanguageNonShell, "unknown interpreter"},
		{"busybox without applet", "#!/bin/busybox\n", HookLanguageNonShell, "busybox (no applet named)"},

		// Binary content is never rewritten, whatever the first line says.
		{"binary content", "#!/bin/sh\n\x00binary\x00\n", HookLanguageBinary, "binary content"},
		{"binary without shebang", "\x00\x01\x02", HookLanguageBinary, "binary content"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyHookLanguage([]byte(tc.content))
			if got.Kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q (interpreter=%q)", got.Kind, tc.wantKind, got.Interpreter)
			}
			if got.Kind != HookLanguageShell && got.Interpreter != tc.wantInterp {
				t.Fatalf("interpreter = %q, want %q", got.Interpreter, tc.wantInterp)
			}
		})
	}
}

// The tag-inside-a-line case: a marker embedded in a Python string literal
// must not validate as a strippable pair.
func TestNonShellHookInterpreter_MarkerInStringIsAmbiguous(t *testing.T) {
	content := "#!/usr/bin/env python3\ns = \"# --- BEGIN BEADS INTEGRATION v1.2.3 ---\"\n"
	if got := InspectBdSectionMarkers(content); got != BdSectionMarkersAmbiguous {
		t.Fatalf("InspectBdSectionMarkers = %v, want Ambiguous for a tag inside a string", got)
	}
	if ClassifyHookLanguage([]byte(content)).Kind != HookLanguageNonShell {
		t.Fatal("python3 string-literal fixture classified as shell")
	}
}

func TestInspectBdSectionMarkers(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    BdSectionMarkers
	}{
		{"no markers", "#!/bin/sh\necho hi\n", BdSectionMarkersNone},
		{"clean pair", "#!/bin/sh\n" + hookMarkerLineBegin + " v1.2.3 ---\nfoo\n" + hookMarkerLineEnd + " v1.2.3 ---\n", BdSectionMarkersPaired},
		{"two disjoint pairs", "#!/bin/sh\n" + hookMarkerLineBegin + " v1 ---\n" + hookMarkerLineEnd + " v1 ---\necho mid\n" + hookMarkerLineBegin + " v2 ---\n" + hookMarkerLineEnd + " v2 ---\n", BdSectionMarkersPaired},
		{"begin only", "#!/bin/sh\n" + hookMarkerLineBegin + " v1 ---\nfoo\n", BdSectionMarkersAmbiguous},
		{"end only", "#!/bin/sh\nfoo\n" + hookMarkerLineEnd + " v1 ---\n", BdSectionMarkersAmbiguous},
		{"reversed", hookMarkerLineEnd + " v1 ---\nfoo\n" + hookMarkerLineBegin + " v1 ---\n", BdSectionMarkersAmbiguous},
		{"nested", hookMarkerLineBegin + " v1 ---\n" + hookMarkerLineBegin + " v1 ---\n" + hookMarkerLineEnd + " v1 ---\n" + hookMarkerLineEnd + " v1 ---\n", BdSectionMarkersAmbiguous},
		{"tag inside another line", "#!/usr/bin/env python3\nx = \"BEGIN BEADS INTEGRATION\"\n", BdSectionMarkersAmbiguous},
		{"indented whole-line markers", "#!/bin/sh\n  " + hookMarkerLineBegin + " v1 ---\nfoo\n\t" + hookMarkerLineEnd + " v1 ---\n", BdSectionMarkersPaired},
		{"CRLF marker lines", "#!/bin/sh\n" + hookMarkerLineBegin + " v1 ---\r\nfoo\r\n" + hookMarkerLineEnd + " v1 ---\r\n", BdSectionMarkersPaired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InspectBdSectionMarkers(tc.content); got != tc.want {
				t.Fatalf("InspectBdSectionMarkers = %v, want %v", got, tc.want)
			}
		})
	}
}

// IsBdHooksRunIntegration must recognize the delegation shape bd
// prescribes for external managers (GH#946) so a non-shell hook that already
// calls bd stays silent.
func TestIsBdHooksRunIntegration(t *testing.T) {
	if !IsBdHooksRunIntegration("#!/usr/bin/env python3\nimport subprocess\nsubprocess.call(\"bd hooks run pre-push\", shell=True)\n") {
		t.Fatal("python delegation not recognized")
	}
	if IsBdHooksRunIntegration("#!/usr/bin/env python3\nprint('no bd here')\n") {
		t.Fatal("plain python hook misclassified as integration")
	}
}
