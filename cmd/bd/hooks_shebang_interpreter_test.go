package main

import "testing"

// TestNonShellShebangInterpreter pins detection of hook files bd must refuse
// to inject its POSIX-sh section into: appending sh code after a non-shell
// script's own exit call is not merely unreachable, it is a syntax error for
// languages that parse the whole file up front (bd-<fixid>).
func TestNonShellShebangInterpreter(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantInterp string
		wantOK     bool
	}{
		{
			name:       "python3 via env",
			content:    "#!/usr/bin/env python3\nimport sys\n",
			wantInterp: "python3",
			wantOK:     true,
		},
		{
			name:       "python direct path",
			content:    "#!/usr/bin/python3\nimport sys\n",
			wantInterp: "python3",
			wantOK:     true,
		},
		{
			name:       "node via env",
			content:    "#!/usr/bin/env node\nconsole.log('hi')\n",
			wantInterp: "node",
			wantOK:     true,
		},
		{
			name:       "ruby via env",
			content:    "#!/usr/bin/env ruby\nputs 'hi'\n",
			wantInterp: "ruby",
			wantOK:     true,
		},
		{
			name:       "sh via env is fine",
			content:    "#!/usr/bin/env sh\necho hi\n",
			wantOK:     false,
		},
		{
			name:       "bash direct path is fine",
			content:    "#!/bin/bash\necho hi\n",
			wantOK:     false,
		},
		{
			name:       "zsh is fine",
			content:    "#!/usr/bin/env zsh\necho hi\n",
			wantOK:     false,
		},
		{
			name:       "no shebang at all",
			content:    "echo hi\n",
			wantOK:     false,
		},
		{
			name:       "env with no interpreter argument",
			content:    "#!/usr/bin/env\n",
			wantOK:     false,
		},
		{
			name:       "empty file",
			content:    "",
			wantOK:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			interp, ok := nonShellShebangInterpreter(tc.content)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (interp=%q)", ok, tc.wantOK, interp)
			}
			if ok && interp != tc.wantInterp {
				t.Fatalf("interp = %q, want %q", interp, tc.wantInterp)
			}
		})
	}
}
