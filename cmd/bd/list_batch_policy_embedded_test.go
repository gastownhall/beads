//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

// TestEmbeddedListAcrossAHydrationBatchKeepsItsLimitPolicy (AC-7, GH#4370):
// `bd list` now answers a page of up to sqlbuild.QueryBatchSize rows by
// selecting its ids first and hydrating only those, and keeps the predicate
// form past that (#7635). Over more rows than one batch, on both sides of that
// switch, the CLI's limit policy and output are what they were: piped stdout
// lists every row, an explicit --limit and a configured BD_LIST_LIMIT bound the
// page, --limit 0 lifts a configured limit, --json is still a bare array, and
// the opt-in --max-rows cap fires one row past the result set and not at it.
func TestEmbeddedListAcrossAHydrationBatchKeepsItsLimitPolicy(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "lbp")

	total := sqlbuild.QueryBatchSize + 5
	nodes := make([]string, 0, total)
	for i := range total {
		nodes = append(nodes, fmt.Sprintf(`{"key":"n%d","title":"batch policy %d","type":"task"}`, i, i))
	}
	planFile := filepath.Join(dir, "batch-plan.json")
	if err := os.WriteFile(planFile, []byte(`{"nodes":[`+strings.Join(nodes, ",")+`]}`), 0o600); err != nil {
		t.Fatalf("write graph plan: %v", err)
	}
	if got := len(bdCreateGraph(t, bd, dir, planFile).IDs); got != total {
		t.Fatalf("graph create made %d issues, want %d", got, total)
	}

	listed := func(t *testing.T, env []string, args ...string) int {
		t.Helper()
		out, code := bdRunRaw(t, bd, dir, env, append([]string{"list", "--json"}, args...)...)
		if code != 0 {
			t.Fatalf("bd list --json %s exited %d:\n%s", strings.Join(args, " "), code, out)
		}
		start := strings.Index(out, "[")
		if start < 0 || strings.Contains(out[:start], "{") {
			t.Fatalf("bd list --json %s is not a bare JSON array:\n%s", strings.Join(args, " "), out)
		}
		var rows []json.RawMessage
		if err := json.Unmarshal([]byte(out[start:]), &rows); err != nil {
			t.Fatalf("bd list --json %s: %v\n%s", strings.Join(args, " "), err, out[start:])
		}
		return len(rows)
	}

	for _, tc := range []struct {
		name string
		env  []string
		args []string
		want int
	}{
		{"piped default lists every row", nil, nil, total},
		{"explicit limit one row into the second batch", nil, []string{"--limit", fmt.Sprint(sqlbuild.QueryBatchSize + 1)}, sqlbuild.QueryBatchSize + 1},
		{"configured limit", []string{"BD_LIST_LIMIT=201"}, nil, 201},
		{"explicit zero lifts a configured limit", []string{"BD_LIST_LIMIT=201"}, []string{"--limit", "0"}, total},
		{"cap at the result set", nil, []string{"--max-rows", fmt.Sprint(total)}, total},
		{"limit equal to the cap truncates", nil, []string{"--limit", "201", "--max-rows", "201"}, 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := listed(t, tc.env, tc.args...); got != tc.want {
				t.Errorf("listed %d rows, want %d", got, tc.want)
			}
		})
	}

	t.Run("cap one row under the result set fires", func(t *testing.T) {
		capFlag := fmt.Sprint(total - 1)
		out, code := bdRunRaw(t, bd, dir, nil, "list", "--json", "--max-rows", capFlag)
		if code != 2 {
			t.Fatalf("exit %d, want 2 (cap exceeded):\n%s", code, out)
		}
		if !strings.Contains(out, "too many rows") || !strings.Contains(out, "--max-rows="+capFlag) {
			t.Errorf("cap refusal does not name the cap and its source:\n%s", out)
		}
	})
}
