//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/steveyegge/beads/internal/types"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// gqlResponse is bd gql's stdout.
type gqlResponse struct {
	Data       map[string]json.RawMessage `json:"data"`
	Errors     []map[string]any           `json:"errors"`
	Extensions map[string]any             `json:"extensions"`
}

// bdGQL runs "bd gql" and returns the decoded response and the exit code.
func bdGQL(t *testing.T, bd, dir string, args ...string) (gqlResponse, int) {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"gql"}, args...)...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("bd gql: %v\nstderr:\n%s", err, stderr.String())
	}
	var resp gqlResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("bd gql stdout is not one JSON object: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	return resp, code
}

func requireSchemaVersion(t *testing.T, resp gqlResponse) {
	t.Helper()
	if got, _ := resp.Extensions["schema_version"].(float64); int(got) != JSONSchemaVersion {
		t.Fatalf("extensions = %v, want schema_version %d", resp.Extensions, JSONSchemaVersion)
	}
}

func TestEmbeddedGQLTracer(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tg")
	created := bdCreate(t, bd, dir, "GQL tracer issue", "--type", "task", "--priority", "1")
	shown := bdShow(t, bd, dir, created.ID)

	t.Run("issue_fields", func(t *testing.T) {
		resp, code := bdGQL(t, bd, dir, `{ issue(id: "`+created.ID+`") { id title created_at } }`)
		if code != 0 || len(resp.Errors) != 0 {
			t.Fatalf("exit %d, errors %v; want exit 0 and no errors", code, resp.Errors)
		}
		requireSchemaVersion(t, resp)
		var issue struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			CreatedAt string `json:"created_at"`
		}
		if err := json.Unmarshal(resp.Data["issue"], &issue); err != nil {
			t.Fatalf("data.issue = %s: %v", resp.Data["issue"], err)
		}
		if issue.ID != created.ID || issue.Title != "GQL tracer issue" {
			t.Fatalf("data.issue = %+v", issue)
		}
		at, err := time.Parse(time.RFC3339Nano, issue.CreatedAt)
		if err != nil || !at.Equal(shown.CreatedAt) {
			t.Fatalf("created_at = %q, want bd show's %v", issue.CreatedAt, shown.CreatedAt)
		}
	})

	t.Run("syntax_error", func(t *testing.T) {
		resp, code := bdGQL(t, bd, dir, `{ issue(id: }`)
		if code != 1 || len(resp.Errors) == 0 {
			t.Fatalf("exit %d, errors %v; want exit 1 and an error", code, resp.Errors)
		}
		requireSchemaVersion(t, resp)
	})

	t.Run("missing_id", func(t *testing.T) {
		resp, code := bdGQL(t, bd, dir, `{ issue(id: "missing-1") { id } }`)
		if code != 0 || len(resp.Errors) != 0 {
			t.Fatalf("exit %d, errors %v; want exit 0 and no errors", code, resp.Errors)
		}
		if string(resp.Data["issue"]) != "null" {
			t.Fatalf("data.issue = %s, want null", resp.Data["issue"])
		}
		requireSchemaVersion(t, resp)
	})
}

// gqlRun captures non-JSON rejections as well as JSON GraphQL responses.
func gqlRun(t *testing.T, bd, dir, input string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"gql"}, args...)...)
	cmd.Dir, cmd.Env = dir, bdEnv(dir)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	out, stderr, err := runCommandBuffers(t, cmd)
	if err == nil {
		return out.String(), stderr.String(), 0
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("gql run: %v", err)
	}
	return out.String(), stderr.String(), exit.ExitCode()
}

func gqlPage(t *testing.T, resp gqlResponse, key string) ([]string, bool) {
	t.Helper()
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		HasMore bool `json:"has_more"`
	}
	if len(resp.Errors) > 0 {
		t.Fatalf("%s errors: %v", key, resp.Errors)
	}
	if err := json.Unmarshal(resp.Data[key], &page); err != nil {
		t.Fatalf("%s: %v (%s)", key, err, resp.Data[key])
	}
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.ID)
	}
	return ids, page.HasMore
}

func gqlCLIIDs(rows []map[string]interface{}) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row["id"].(string))
	}
	return ids
}

func gqlReadyJSON(t *testing.T, bd, dir string, args ...string) []map[string]interface{} {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"ready", "--json"}, args...)...)
	cmd.Dir, cmd.Env = dir, bdEnv(dir)
	out, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("ready: %v\n%s", err, stderr.String())
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("ready JSON: %v: %s", err, out.String())
	}
	return rows
}

func gqlSuccess(t *testing.T, bd, dir, doc string) gqlResponse {
	t.Helper()
	resp, code := bdGQL(t, bd, dir, doc)
	if code != 0 || len(resp.Errors) > 0 {
		t.Fatalf("gql exit %d errors %v", code, resp.Errors)
	}
	requireSchemaVersion(t, resp)
	return resp
}

func TestEmbeddedGQLPagesAndRows(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gp")
	made := make([]*types.Issue, 0, 7)
	for i := 0; i < 5; i++ {
		made = append(made, bdCreate(t, bd, dir, fmt.Sprintf("GQL page %d", i), "--type", "task", "--priority", fmt.Sprint(i%3)))
	}
	blocked := bdCreate(t, bd, dir, "GQL blocked", "--type", "task")
	bdDepAdd(t, bd, dir, blocked.ID, made[0].ID)
	made = append(made, blocked)
	baseline := make(map[string]time.Time)
	for _, issue := range made {
		baseline[issue.ID] = bdShow(t, bd, dir, issue.ID).UpdatedAt
	}
	// Exactly six matching rows, one of them blocked from the ready set.
	query := `{ issues(query:"type=task",sort:"priority",limit:3) { items { id } has_more } }`
	first, more := gqlPage(t, gqlSuccess(t, bd, dir, query), "issues")
	cli := gqlCLIIDs(bdQueryJSON(t, bd, dir, "type=task", "--sort", "priority", "--limit", "3"))
	if !reflect.DeepEqual(first, cli) || !more {
		t.Fatalf("issues first=%v cli=%v more=%v", first, cli, more)
	}
	all, more := gqlPage(t, gqlSuccess(t, bd, dir, `{ issues(query:"type=task",sort:"priority",limit:10) { items { id } has_more } }`), "issues")
	want := gqlCLIIDs(bdQueryJSON(t, bd, dir, "type=task", "--sort", "priority", "--limit", "10"))
	if len(all) != 6 || !reflect.DeepEqual(all, want) || more {
		t.Fatalf("issues all=%v want=%v more=%v", all, want, more)
	}
	unsorted, _ := gqlPage(t, gqlSuccess(t, bd, dir, `{ issues(query:"type=task",limit:10) { items { id } has_more } }`), "issues")
	tail, _ := gqlPage(t, gqlSuccess(t, bd, dir, `{ issues(query:"type=task",limit:3,offset:3) { items { id } has_more } }`), "issues")
	if !reflect.DeepEqual(tail, unsorted[3:]) {
		t.Fatalf("issues offset=%v want %v", tail, unsorted[3:])
	}
	resp, code := bdGQL(t, bd, dir, `{ issues(query:"type=task",sort:"priority",offset:1) { items { id } } }`)
	if code != 1 || len(resp.Errors) == 0 {
		t.Fatalf("offset+sort exit %d errors=%v", code, resp.Errors)
	}
	// These five are ready; the sixth is blocked on the first issue.
	r3, _ := gqlPage(t, gqlSuccess(t, bd, dir, `{ ready(limit:3) { items { id } has_more } }`), "ready")
	c3 := gqlCLIIDs(gqlReadyJSON(t, bd, dir, "-n", "3"))
	if !reflect.DeepEqual(r3, c3) {
		t.Fatalf("ready IDs/order gql=%v bd=%v", r3, c3)
	}
	full, _ := gqlPage(t, gqlSuccess(t, bd, dir, `{ ready(limit:100) { items { id } has_more } }`), "ready")
	defaults, _ := gqlPage(t, gqlSuccess(t, bd, dir, `{ ready { items { id } } }`), "ready")
	cDefault := gqlCLIIDs(gqlReadyJSON(t, bd, dir))
	if len(full) != 5 || !reflect.DeepEqual(full, defaults) || !reflect.DeepEqual(full, cDefault) {
		t.Fatalf("ready full=%v defaults=%v cli=%v", full, defaults, cDefault)
	}
	page, more := gqlPage(t, gqlSuccess(t, bd, dir, `{ ready(limit:3,offset:3) { items { id } has_more } }`), "ready")
	if !reflect.DeepEqual(page, full[3:]) || more {
		t.Fatalf("ready offset=%v want=%v more=%v", page, full[3:], more)
	}
	empty, _ := gqlPage(t, gqlSuccess(t, bd, dir, `{ ready(limit:3,offset:10) { items { id } has_more } }`), "ready")
	if len(empty) != 0 {
		t.Fatalf("ready past end=%v", empty)
	}
	resp, code = bdGQL(t, bd, dir, `{ ready(offset:-1) { items { id } } }`)
	if code != 1 || len(resp.Errors) == 0 {
		t.Fatalf("negative offset exit=%d errors=%v", code, resp.Errors)
	}
	for _, issue := range made {
		before := bdShow(t, bd, dir, issue.ID)
		if !before.UpdatedAt.Equal(baseline[issue.ID]) {
			t.Fatalf("gql reads updated %s: %v -> %v", issue.ID, baseline[issue.ID], before.UpdatedAt)
		}
	}
}

func TestEmbeddedGQLInputAndRejections(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gi")
	issue := bdCreate(t, bd, dir, "GQL input")
	baseline := bdShow(t, bd, dir, issue.ID).UpdatedAt
	doc := fmt.Sprintf(`{ issue(id:"%s") { id title } }`, issue.ID)
	file := filepath.Join(dir, "q.graphql")
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	a, code := bdGQL(t, bd, dir, doc)
	if code != 0 {
		t.Fatalf("positional: %v", a.Errors)
	}
	b, code := bdGQL(t, bd, dir, "--file", file)
	if code != 0 || !reflect.DeepEqual(a, b) {
		t.Fatalf("file response=%v versus %v", b, a)
	}
	stdin, stderr, code := gqlRun(t, bd, dir, doc+"\n", "--stdin")
	var c gqlResponse
	if code != 0 || json.Unmarshal([]byte(stdin), &c) != nil || !reflect.DeepEqual(a, c) {
		t.Fatalf("stdin exit=%d stderr=%q stdout=%q", code, stderr, stdin)
	}
	big := filepath.Join(dir, "big.graphql")
	if err := os.WriteFile(big, []byte(`{ issue(id:"x") { id } }`+strings.Repeat(" ", 16385)), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.graphql")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		args   []string
		marker string
	}{
		{"oversize", []string{"--file", big}, "length"},
		{"missing", []string{"--file", filepath.Join(dir, "missing.graphql")}, "missing.graphql"},
		{"position_file", []string{doc, "--file", file}, "cannot combine"},
		{"blank_position_file", []string{"", "--file", file}, "cannot combine"},
		{"stdin_file", []string{"--stdin", "--file", file}, "none of the others can be"},
		{"empty_file", []string{"--file", empty}, "cannot be empty"},
		{"no_source", nil, "Usage:"},
		{"vars_array", []string{"--vars", "[1]", doc}, "JSON object"},
		{"vars_invalid", []string{"--vars", "{bad", doc}, "JSON object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errText, code := gqlRun(t, bd, dir, "", tc.args...)
			if code != 1 || !strings.Contains(out+errText, tc.marker) {
				t.Fatalf("exit=%d stdout=%q stderr=%q want %q", code, out, errText, tc.marker)
			}
		})
	}
	vars := fmt.Sprintf(`{"id":%q}`, issue.ID)
	resp, code := bdGQL(t, bd, dir, "--vars", vars, `query($id:ID!) { issue(id:$id) { id } }`)
	if code != 0 || len(resp.Errors) > 0 {
		t.Fatalf("variables exit=%d errors=%v", code, resp.Errors)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Data["issue"], &result); err != nil || result["id"] != issue.ID {
		t.Fatalf("variables issue=%v err=%v", result, err)
	}
	for _, tc := range []struct{ name, doc, marker string }{
		{"syntax", `{ issue(id: }`, "syntax"},
		{"unknown", `{ issue(id:"x") { unknown_field } }`, "unknown_field"},
		{"depth", `{ issue(id:"x") { dependencies { issue { dependencies { issue { dependencies { issue { dependencies { issue { id } } } } } } } } } }`, "depth"},
		{"limit_zero", `{ issues(limit:0) { has_more } }`, "limit"},
		{"limit_201", `{ ready(limit:201) { has_more } }`, "limit"},
		{"expression", `{ issues(query:"@") { has_more } }`, "invalid"},
		{"mutation", `mutation { x }`, "mutation"},
		{"subscription", `subscription { x }`, "subscription"},
		{"length", `{ issue(id:"x") { id } }` + strings.Repeat(" ", 16385), "length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, code := bdGQL(t, bd, dir, tc.doc)
			if code != 1 || len(r.Errors) == 0 || !strings.Contains(strings.ToLower(fmt.Sprint(r.Errors)), tc.marker) {
				t.Fatalf("exit=%d errors=%v want %q", code, r.Errors, tc.marker)
			}
			requireSchemaVersion(t, r)
		})
	}
	shown := bdShow(t, bd, dir, issue.ID)
	if !shown.UpdatedAt.Equal(baseline) {
		t.Fatalf("rejected queries changed updated_at: %v -> %v", baseline, shown.UpdatedAt)
	}
	rows := bdQueryJSON(t, bd, dir, `id="*"`, "--all")
	if len(rows) != 1 || rows[0]["id"] != issue.ID {
		t.Fatalf("rejected queries changed issue count or row: %v", rows)
	}
}

func TestEmbeddedGQLBatchAndUnfiltered(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gb")
	a := bdCreate(t, bd, dir, "GQL A")
	b := bdCreate(t, bd, dir, "GQL B")
	doc := fmt.Sprintf(`{ issuesById(ids:[%q,"missing-1",%q,%q]) { id title status assignee } }`, b.ID, a.ID, b.ID)
	r := gqlSuccess(t, bd, dir, doc)
	var batch []map[string]any
	if err := json.Unmarshal(r.Data["issuesById"], &batch); err != nil || len(batch) != 4 || batch[1] != nil || batch[0]["id"] != b.ID || batch[2]["id"] != a.ID || batch[3]["id"] != b.ID {
		t.Fatalf("issuesById=%v err=%v", batch, err)
	}
	ids := make([]string, 201)
	for i := range ids {
		ids[i] = fmt.Sprintf("%q", fmt.Sprintf("missing-%d", i))
	}
	resp, code := bdGQL(t, bd, dir, `{ issuesById(ids:[`+strings.Join(ids, ",")+`]) { id } }`)
	if code != 1 || !strings.Contains(strings.ToLower(fmt.Sprint(resp.Errors)), "limit") {
		t.Fatalf("201 IDs exit=%d errors=%v", code, resp.Errors)
	}
	bdCreate(t, bd, dir, "GQL C")
	closed := bdCreate(t, bd, dir, "GQL closed")
	bdClose(t, bd, dir, closed.ID)
	store := openStore(t, beadsDir, "gb")
	if err := store.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
		t.Fatal(err)
	}
	store.Close()
	gate := createGate(t, bd, dir, "GQL gate")
	all, _ := gqlPage(t, gqlSuccess(t, bd, dir, `{ issues(all:true,limit:10) { items { id } has_more } }`), "issues")
	cli := gqlCLIIDs(bdQueryJSON(t, bd, dir, `id="*"`, "--all", "--limit", "10"))
	list := bdListJSON(t, bd, dir, "--all", "--limit", "0")
	if len(all) != 5 || !reflect.DeepEqual(all, cli) {
		t.Fatalf("unfiltered gql=%v bd query=%v", all, cli)
	}
	hasGate := false
	for _, id := range all {
		hasGate = hasGate || id == gate.ID
	}
	if !hasGate {
		t.Fatalf("gate missing from issues: %v", all)
	}
	for _, row := range list {
		if row.ID == gate.ID {
			t.Fatalf("gate unexpectedly in bd list")
		}
	}
}

func TestEmbeddedGQLDetailParity(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gd")
	target := bdCreate(t, bd, dir, "GQL detail target")
	bdGate(t, bd, dir, "create", "--type", "timer", "--blocks", target.ID, "--timeout", "2h", "--title", "GQL detail gate")
	gates := bdQueryJSON(t, bd, dir, `type=gate`, "--all")
	if len(gates) != 1 {
		t.Fatalf("gate fixture: %v", gates)
	}
	id := gates[0]["id"].(string)
	bdUpdate(t, bd, dir, id, "--metadata", `{"key":"value"}`, "--estimate", "90", "--defer", "+24h")
	bdLabel(t, bd, dir, "add", id, "gql-detail")
	shown := bdShowDetails(t, bd, dir, id)
	doc := fmt.Sprintf(`{ issue(id:%q) { id metadata timeout bonded_from estimated_minutes defer_until labels } }`, id)
	resp := gqlSuccess(t, bd, dir, doc)
	var gql map[string]any
	if err := json.Unmarshal(resp.Data["issue"], &gql); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "metadata", "timeout", "bonded_from", "estimated_minutes", "defer_until", "labels"} {
		if !reflect.DeepEqual(gql[key], shown[key]) {
			t.Errorf("%s gql=%v show=%v", key, gql[key], shown[key])
		}
	}
	if gql["timeout"] != float64(7200000000000) {
		t.Errorf("timeout=%v", gql["timeout"])
	}
	// bonded_from is not writable through create/update; its shared absent value still gets compared.
}
