//go:build cgo

package bdhttp_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestE2E_BlockedOverHTTP runs `bd blocked --json` (the blocked door gc's
// readiness projection reads) through the real `bd` binary against an
// in-process `bd serve`, and against a local embedded workspace holding the
// same graph.
//
// Over HTTP the CLI takes the store's BlockedLister, which dials
// GET /v0/beads/blocked behind blocked.list. The two answers must be the same
// JSON once the values that cannot agree between two workspaces (each row's
// generated id, and its timestamps) are mapped out: the same rows, the same
// members, the same blockers (local, parent-attributed and external), in the
// same order. Against a server that does not advertise blocked.list (an older
// server's handshake) the client refuses before the dial, naming the token.
func TestE2E_BlockedOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t)

	seedBlocked := func(f *remoteRolesFixture) {
		f.seed()
		// A plain blocks edge, a blocked parent's child attributed to the
		// parent, and a labelled blocked row for the filter arms.
		blocker := f.create("blocker", "-t", "task", "-p", "1")
		gp := f.create("gate-parent", "-t", "epic", "-p", "2")
		// Its own priority: rows tied on priority and created_at fall to the
		// id order, and ids are generated per workspace, so a tie would order
		// the two workspaces differently.
		kid := f.create("gate-kid", "-t", "task", "-p", "3")
		tagged := f.create("tagged", "-t", "task", "-p", "1", "-l", "lane:a")
		// Blocked by nothing but an unsatisfied external ref, as child-x is,
		// and labelled: the decorator lists such rows itself, after the label
		// query ran, so the filter arms must judge them too.
		extTagged := f.create("ext-tagged", "-t", "task", "-p", "4", "-l", "lane:a")
		f.mustRun(nil, "dep", "add", kid, gp, "--type", "parent-child")
		f.mustRun(nil, "dep", "add", gp, blocker)
		f.mustRun(nil, "dep", "add", tagged, blocker)
		f.mustRun(nil, "dep", "add", extTagged, "external:other:cap")
	}

	local := newEmbeddedFixture(t, bin)
	seedBlocked(local)
	served := newHTTPFixture(t, bin, addr)
	seedBlocked(served)

	for _, args := range [][]string{
		{"blocked", "--json"},
		{"blocked", "--json", "--label", "lane:a"},
		{"blocked", "--json", "--label-any", "lane:a"},
		{"blocked", "--json", "--exclude-label", "lane:a"},
		{"blocked", "--json", "--parent", "{gate-parent}"},
		{"blocked", "--json", "--label", "no-such-label"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			want := local.mustRun(nil, local.expand(args)...).stdout
			got := served.mustRun(nil, served.expand(args)...).stdout
			if strings.TrimSpace(want) == "null" || strings.TrimSpace(got) == "null" {
				t.Fatalf("bd blocked --json printed null: local=%s served=%s", want, got)
			}
			if w, g := normalizeBlocked(t, local, want), normalizeBlocked(t, served, got); !reflect.DeepEqual(w, g) {
				t.Errorf("served bd blocked --json differs from the local command:\nlocal:  %v\nserved: %v", w, g)
			}
			if w, g := blockedKeyOrder(t, want), blockedKeyOrder(t, got); !reflect.DeepEqual(w, g) {
				t.Errorf("served member order %v differs from the local command's %v", g, w)
			}
		})
	}

	t.Run("names the external blocker", func(t *testing.T) {
		got := normalizeBlocked(t, served, served.mustRun(nil, "blocked", "--json").stdout)
		found := false
		for _, row := range got {
			if row["id"] == "child-x" {
				found = reflect.DeepEqual(row["blocked_by"], []any{"external:other:cap"})
			}
		}
		if !found {
			t.Errorf("served bd blocked --json does not list child-x blocked by its external ref: %v", got)
		}
	})

	// The arms above compare the two answers, which agree just as well when
	// both list a row the filter rules out. These pin which rows each filter
	// leaves, the external-only ones (child-x unlabelled, ext-tagged
	// labelled) among them.
	for _, tc := range []struct {
		args          []string
		want          []string // the whole listing, when set
		listed, ruled []string
	}{
		{args: []string{"--label", "lane:a"}, want: []string{"ext-tagged", "tagged"}},
		{args: []string{"--label-any", "lane:a"}, want: []string{"ext-tagged", "tagged"}},
		{args: []string{"--label", "no-such-label"}, want: []string{}},
		{args: []string{"--exclude-label", "lane:a"}, listed: []string{"child-x"}, ruled: []string{"ext-tagged", "tagged"}},
	} {
		t.Run("filters external-only rows "+strings.Join(tc.args, " "), func(t *testing.T) {
			for _, f := range []struct {
				name string
				f    *remoteRolesFixture
			}{{"local", local}, {"served", served}} {
				got := f.f.titlesOf(f.f.mustRun(nil, append([]string{"blocked", "--json"}, tc.args...)...).stdout)
				if tc.want != nil && !slices.Equal(got, tc.want) {
					t.Errorf("%s bd blocked %s listed %v, want %v", f.name, strings.Join(tc.args, " "), got, tc.want)
				}
				for _, title := range tc.listed {
					if !slices.Contains(got, title) {
						t.Errorf("%s bd blocked %s does not list %s: %v", f.name, strings.Join(tc.args, " "), title, got)
					}
				}
				for _, title := range tc.ruled {
					if slices.Contains(got, title) {
						t.Errorf("%s bd blocked %s lists %s, which the filter rules out: %v", f.name, strings.Join(tc.args, " "), title, got)
					}
				}
			}
		})
	}

	t.Run("older server mask", func(t *testing.T) {
		proxyAddr, counter := startMaskingProxy(t, addr, mcContextMask)
		f := newHTTPFixture(t, bin, proxyAddr)
		counter.reset()
		r := f.run(nil, "blocked", "--json")
		if r.code != 1 {
			t.Fatalf("bd blocked against a server without blocked.list exited %d, want 1: %s", r.code, r.stdout)
		}
		if !strings.Contains(r.stdout+r.stderr, "blocked.list") {
			t.Errorf("refusal does not name the missing capability: stdout=%s stderr=%s", r.stdout, r.stderr)
		}
		for _, p := range counter.snapshot() {
			if strings.Contains(p, "/v0/beads/blocked") {
				t.Errorf("a refused bd blocked dialed %s; the refusal must come before the dial", p)
			}
		}
	})
}

// normalizeBlocked parses `bd blocked --json` and maps out what two
// workspaces cannot share: each id (and each blocker id) becomes the title the
// fixture created it under, and every timestamp member is blanked.
func normalizeBlocked(t *testing.T, f *remoteRolesFixture, out string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("parse bd blocked --json: %v\n%s", err, out)
	}
	titleOf := map[string]string{}
	for title, id := range f.ids {
		titleOf[id] = title
	}
	var walk func(key string, v any) any
	walk = func(key string, v any) any {
		switch v := v.(type) {
		case map[string]any:
			for k, inner := range v {
				v[k] = walk(k, inner)
			}
			return v
		case []any:
			for i := range v {
				v[i] = walk("", v[i])
			}
			return v
		case string:
			if strings.HasSuffix(key, "_at") {
				return "<time>"
			}
			if title, ok := titleOf[v]; ok {
				return title
			}
		}
		return v
	}
	for _, row := range rows {
		walk("", row)
	}
	return rows
}

// blockedKeyOrder is the member order of each row as printed, so the two
// encodings are compared as text, not only as values.
func blockedKeyOrder(t *testing.T, out string) [][]string {
	t.Helper()
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("parse bd blocked --json: %v\n%s", err, out)
	}
	order := make([][]string, 0, len(rows))
	for _, raw := range rows {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		var keys []string
		depth := 0
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			switch v := tok.(type) {
			case json.Delim:
				if v == '{' || v == '[' {
					depth++
				} else {
					depth--
				}
			case string:
				if depth == 1 {
					keys = append(keys, v)
					// Skip the value.
					var skip json.RawMessage
					if err := dec.Decode(&skip); err != nil {
						t.Fatalf("decode value of %s: %v", v, err)
					}
				}
			}
		}
		order = append(order, keys)
	}
	return order
}
