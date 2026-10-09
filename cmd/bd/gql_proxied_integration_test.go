//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func gqlProxied(t *testing.T, bd, dir, document string) (gqlResponse, int) {
	t.Helper()
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, "gql", document)
	code := 0
	if err != nil {
		exit, ok := err.(interface{ ExitCode() int })
		if !ok {
			t.Fatalf("proxied gql: %v stderr %s", err, stderr)
		}
		code = exit.ExitCode()
	}
	var resp gqlResponse
	if e := json.Unmarshal([]byte(stdout), &resp); e != nil {
		t.Fatalf("proxied gql invalid JSON: %v stdout=%s stderr=%s", e, stdout, stderr)
	}
	return resp, code
}

func TestProxiedServerGQLParity(t *testing.T) {
	requireProxiedServerEnv(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "gqp")
	issues := make([]string, 6)
	for i := range issues {
		issues[i] = bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("GQL proxied %d", i), "--priority", fmt.Sprint(i%3)).ID
	}
	if _, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "dep", "add", issues[0], issues[1]); err != nil {
		t.Fatalf("create relation: %v: %s", err, stderr)
	}
	first := bdProxiedShow(t, bd, p.dir, issues[0])
	r, code := gqlProxied(t, bd, p.dir, fmt.Sprintf(`{ issue(id:%q) { id title created_at } }`, first.ID))
	if code != 0 || len(r.Errors) > 0 {
		t.Fatalf("issue exit=%d errors=%v", code, r.Errors)
	}
	requireSchemaVersion(t, r)
	var issue struct {
		ID, Title string
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(r.Data["issue"], &issue); err != nil || issue.ID != first.ID || issue.Title != first.Title {
		t.Fatalf("issue=%+v err=%v", issue, err)
	}
	at, err := time.Parse(time.RFC3339Nano, issue.CreatedAt)
	if err != nil || !at.Equal(first.CreatedAt) {
		t.Fatalf("created_at=%s want=%s err=%v", issue.CreatedAt, first.CreatedAt, err)
	}
	r, code = gqlProxied(t, bd, p.dir, `{ issue(id: }`)
	if code != 1 || len(r.Errors) == 0 {
		t.Fatalf("syntax exit=%d errors=%v", code, r.Errors)
	}
	requireSchemaVersion(t, r)
	r, code = gqlProxied(t, bd, p.dir, fmt.Sprintf(`{ issue(id:%q) { dependencies { issue { id title status } } } }`, first.ID))
	if code != 0 || len(r.Errors) > 0 {
		t.Fatalf("one-level relation exit=%d errors=%v", code, r.Errors)
	}
	var detail struct {
		Dependencies []struct {
			Issue struct{ ID, Title, Status string } `json:"issue"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(r.Data["issue"], &detail); err != nil || len(detail.Dependencies) != 1 || detail.Dependencies[0].Issue.ID != issues[1] || detail.Dependencies[0].Issue.Title != "GQL proxied 1" {
		t.Fatalf("one-level relation = %+v: %v", detail, err)
	}
	r, code = gqlProxied(t, bd, p.dir, `{ issues(query:"priority>=0",sort:"priority",limit:3) { items { id } has_more } }`)
	if code != 0 {
		t.Fatalf("issues exit=%d errors=%v", code, r.Errors)
	}
	got, more := gqlPage(t, r, "issues")
	cli := bdProxiedQueryJSON(t, bd, p, "priority>=0", "--sort", "priority", "--limit", "3")
	want := make([]string, len(cli))
	for i, row := range cli {
		want[i] = row.ID
	}
	if len(got) != 3 || !reflect.DeepEqual(got, want) || !more {
		t.Fatalf("issues=%v cli=%v more=%v", got, want, more)
	}
	requireSchemaVersion(t, r)
}
