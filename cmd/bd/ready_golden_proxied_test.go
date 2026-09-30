//go:build cgo

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestProxiedReadyGoldenOutput pins `bd ready`'s PROXIED-route output byte for
// byte, the twin of TestEmbeddedReadyGoldenOutput over the same seeded
// workspace: JSON, text, pretty, brief, the pagination envelope, the truncation
// hint on both streams, --limit 0 (including gc's exact
// `bd ready --json --include-ephemeral --limit 0`, a plain array), --offset,
// and --claim in both renderings.
//
// Regenerate with -update-ready-golden. The claim cases mutate the workspace,
// so they run last and in a fixed order.
func TestProxiedReadyGoldenOutput(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "gd")

	run := func(args ...string) {
		t.Helper()
		if out, err := bdProxiedRun(t, bd, p.dir, args...); err != nil {
			t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	seedReadyGoldenWorkspace(t, p.dir, newReadyGoldenForeignProject(t, bd), run)

	// The claimant is pinned: the fallback actor is the machine's user name.
	claimEnv := []string{"BEADS_ACTOR=golden-claimer"}
	cases := append([]readyGoldenCase{
		{name: "json_default", args: []string{"ready", "--json"}},
		{name: "json_limit2", args: []string{"ready", "--json", "--limit", "2"}},
		{name: "json_limit2_envelope", args: []string{"ready", "--json", "--limit", "2"}, env: []string{"BD_JSON_ENVELOPE=1"}},
		{name: "json_limit0", args: []string{"ready", "--json", "--limit", "0"}},
		{name: "json_limit0_envelope", args: []string{"ready", "--json", "--limit", "0"}, env: []string{"BD_JSON_ENVELOPE=1"}},
		{name: "json_gc_include_ephemeral_limit0", args: []string{"ready", "--json", "--include-ephemeral", "--limit", "0"}},
		{name: "json_brief", args: []string{"ready", "--json", "--brief", "--limit", "0"}},
		{name: "json_label", args: []string{"ready", "--json", "--label", "team-a"}},
		{name: "json_assignee_priority_sort", args: []string{"ready", "--json", "--assignee", "alice", "--sort", "priority"}},
		{name: "json_offset1_limit2", args: []string{"ready", "--json", "--offset", "1", "--limit", "2"}},
		{name: "json_offset1_limit2_envelope", args: []string{"ready", "--json", "--offset", "1", "--limit", "2"}, env: []string{"BD_JSON_ENVELOPE=1"}},
		{name: "json_empty", args: []string{"ready", "--json", "--label", "nobody"}},
		{name: "text_default", args: []string{"ready"}},
		{name: "text_limit2", args: []string{"ready", "--limit", "2"}},
		{name: "text_limit0", args: []string{"ready", "--limit", "0"}},
		{name: "text_pretty", args: []string{"ready", "--pretty"}},
		{name: "text_pretty_limit2", args: []string{"ready", "--pretty", "--limit", "2"}},
		{name: "text_include_ephemeral", args: []string{"ready", "--include-ephemeral", "--limit", "0"}},
		{name: "text_empty", args: []string{"ready", "--label", "nobody"}},
		{name: "text_max_rows_refused", args: []string{"ready", "--limit", "0", "--max-rows", "2"}},
	}, sharedReadyGoldenCases...)
	// The claim cases mutate the workspace, so they run last and in order.
	cases = append(cases,
		readyGoldenCase{name: "json_claim", args: []string{"ready", "--claim", "--json"}, env: claimEnv},
		readyGoldenCase{name: "text_claim", args: []string{"ready", "--claim"}, env: claimEnv},
		readyGoldenCase{name: "json_claim_none", args: []string{"ready", "--claim", "--json", "--label", "nobody"}, env: claimEnv},
		readyGoldenCase{name: "text_claim_none", args: []string{"ready", "--claim", "--label", "nobody"}, env: claimEnv},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bd, tc.args...)
			cmd.Dir = filepath.Join(p.dir, tc.sub)
			cmd.Env = append(bdProxiedEnv(p.dir), tc.env...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			checkReadyGolden(t, "ready_golden_proxied", tc, stdout.String(), stderr.String(), err)
		})
	}
}

// TestReadyGoldenRoutesAgree holds the two routes' goldens to ONE output: for
// every invocation both golden sets pin over the shared seed, the proxied
// route's bytes must equal the direct route's — the JSON shape, the pagination
// envelope and its total, and the "Showing X of N" hint on either stream. The
// --max-rows refusal is the one deliberate difference (the proxied route
// cannot enforce the cap and refuses it outright), so it is excluded by name.
//
// It reads files only, so it runs in every lane; the two golden tests above
// are what keep the files honest.
func TestReadyGoldenRoutesAgree(t *testing.T) {
	routeSpecific := map[string]bool{
		"text_max_rows_refused.golden": true,
		"json_max_rows_refused.golden": true,
	}
	direct, err := filepath.Glob(filepath.Join(packageDir(t), "testdata", "ready_golden", "*.golden"))
	if err != nil {
		t.Fatal(err)
	}
	compared := 0
	for _, path := range direct {
		name := filepath.Base(path)
		if routeSpecific[name] {
			continue
		}
		proxied, err := os.ReadFile(filepath.Join(packageDir(t), "testdata", "ready_golden_proxied", name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		compared++
		if !bytes.Equal(proxied, want) {
			t.Errorf("%s: the proxied route prints differently from the direct route.\n--- proxied\n%s\n--- direct\n%s", name, proxied, want)
		}
	}
	if compared < 10 {
		t.Errorf("compared only %d shared goldens; the two sets have drifted apart", compared)
	}
}
