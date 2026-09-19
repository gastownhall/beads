//go:build cgo

package legacysqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func TestExportAuthenticatedLegacySQLite(t *testing.T) {
	db, path := legacyFixture(t, "0.50.3")
	if _, err := db.Exec(`INSERT INTO labels VALUES ('old-1','legacy')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO dependencies VALUES ('old-1','old-2','blocks','2026-01-02T03:04:05Z','old',NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO comments VALUES (1,'old-1','old','preserve me','2026-01-02T03:04:05Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Export(context.Background(), path, "-", &out); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("reader modified source database")
	}
	var issues []types.Issue
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'}) {
		var issue types.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			t.Fatal(err)
		}
		if err := issue.Validate(); err != nil {
			t.Fatalf("not import-safe: %v", err)
		}
		issues = append(issues, issue)
	}
	if len(issues) != 2 || issues[0].ID != "old-1" || len(issues[0].Labels) != 1 || len(issues[0].Dependencies) != 1 || len(issues[0].Comments) != 1 {
		t.Fatalf("canonical JSONL lost child data: %#v", issues)
	}
}

func TestExportAcceptsAuthenticEmptyOptionalFields(t *testing.T) {
	db, path := legacyFixture(t, "0.49.6")
	if _, err := db.Exec(`UPDATE issues SET metadata='',waiters='' WHERE id='old-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO comments VALUES (1,'old-1','old','ordinary SQLite time','2026-01-02 03:04:05')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Export(context.Background(), path, "-", &out); err != nil {
		t.Fatal(err)
	}
	var issue types.Issue
	if err := json.Unmarshal(bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})[0], &issue); err != nil {
		t.Fatal(err)
	}
	if len(issue.Metadata) != 0 || len(issue.Waiters) != 0 || len(issue.Comments) != 1 {
		t.Fatalf("empty optional legacy values were not normalized: %#v", issue)
	}
}

func TestExportNormalizesNullRemovedFieldsAndCompactionLevel(t *testing.T) {
	db, path := legacyFixture(t, "0.49.6")
	mustExec(t, db, `UPDATE issues
		SET closed_by_session=NULL, role_type=NULL, rig=NULL, compaction_level=NULL,
			description='preserve me', metadata='{"keep":"me"}'
		WHERE id='old-1'`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Export(context.Background(), path, "-", &out); err != nil {
		t.Fatal(err)
	}
	var issues []types.Issue
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'}) {
		var issue types.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			t.Fatal(err)
		}
		issues = append(issues, issue)
	}
	if len(issues) != 2 {
		t.Fatalf("exported %d issues, want 2", len(issues))
	}
	if issues[0].ID != "old-1" {
		t.Fatalf("first issue = %q, want old-1", issues[0].ID)
	}
	if issues[0].ClosedBySession != "" || issues[0].CompactionLevel != 0 {
		t.Fatalf("NULL legacy sentinels were not normalized: %#v", issues[0])
	}
	if issues[0].Title != "old-1" || issues[0].Description != "preserve me" || string(issues[0].Metadata) != `{"keep":"me"}` {
		t.Fatalf("export lost non-sentinel data: %#v", issues[0])
	}
}

func TestExportRejectsUnauthenticatedOrUnsafeLegacySQLite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *sql.DB)
	}{
		{"drift", mutations(`ALTER TABLE issues ADD COLUMN surprise TEXT`)},
		{"metadata-drift", mutations(`ALTER TABLE metadata ADD COLUMN surprise TEXT`)},
		{"wrong-release", mutations(`UPDATE metadata SET value='0.50.4' WHERE key='bd_version'`)},
		{"missing-release", mutations(`DELETE FROM metadata WHERE key='bd_version'`)},
		{"tombstone", mutations(`UPDATE issues SET status='tombstone' WHERE id='old-1'`)},
		{"removed-field", mutations(`UPDATE issues SET closed_by_session='legacy' WHERE id='old-1'`)},
		{"invalid-json", mutations(`UPDATE issues SET metadata='{' WHERE id='old-1'`)},
		{"invalid-time", mutations(`UPDATE issues SET updated_at='not-a-time' WHERE id='old-1'`)},
		{"empty-id", mutations(`INSERT INTO issues (id,title,status,priority,issue_type,created_at,updated_at,metadata) VALUES ('','empty','open',2,'task','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z','{}')`)},
		{"orphan", mutations(`INSERT INTO labels VALUES ('missing','orphan')`)},
		{"oversize-label", func(t *testing.T, db *sql.DB) {
			mustExec(t, db, `INSERT INTO labels VALUES ('old-1', ?)`, strings.Repeat("x", types.MaxFieldLen+1))
		}},
		{"dependency-metadata", mutations(`INSERT INTO dependencies VALUES ('old-1','old-2','blocks','2026-01-02T03:04:05Z','old','{}',NULL)`)},
		{"duplicate-dependency-target", mutations(`INSERT INTO dependencies VALUES ('old-1','old-2','blocks','2026-01-02T03:04:05Z','old',NULL,NULL),('old-1','old-2','related','2026-01-02T03:04:05Z','old',NULL,NULL)`)},
		{"crosses-ephemeral-storage", mutations(
			`UPDATE issues SET ephemeral=1 WHERE id='old-2'`,
			`INSERT INTO dependencies VALUES ('old-1','old-2','blocks','2026-01-02T03:04:05Z','old',NULL,NULL)`,
		)},
		{"self-dependency", mutations(`INSERT INTO dependencies VALUES ('old-1','old-1','related','2026-01-02T03:04:05Z','old',NULL,NULL)`)},
		{"scheduling-cycle", mutations(`INSERT INTO dependencies VALUES ('old-1','old-2','blocks','2026-01-02T03:04:05Z','old',NULL,NULL),('old-2','old-1','conditional-blocks','2026-01-02T03:04:05Z','old',NULL,NULL)`)},
		{"hierarchy-conflict", mutations(
			`INSERT INTO issues (id,title,status,priority,issue_type,created_at,updated_at,metadata) VALUES ('old-3','old-3','open',2,'task','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z','{}')`,
			`INSERT INTO dependencies VALUES ('old-1','old-2','parent-child','2026-01-02T03:04:05Z','old',NULL,NULL),('old-2','old-3','parent-child','2026-01-02T03:04:05Z','old',NULL,NULL),('old-1','old-3','blocks','2026-01-02T03:04:05Z','old',NULL,NULL)`,
		)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.49.6")
			tc.mutate(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := Export(context.Background(), path, "-", &bytes.Buffer{}); err == nil {
				t.Fatal("Export accepted unsafe legacy database")
			}
		})
	}
}

func TestExportRejectsCurrentVarcharOverflowBeforePublication(t *testing.T) {
	tests := []struct {
		name, field, statement string
		limit                  int
	}{
		{"issue-id", "issue id", `UPDATE issues SET id=? WHERE id='old-1'`, types.MaxFieldLen},
		{"issue-title", "issue title", `UPDATE issues SET title=? WHERE id='old-1'`, currentTitleVarcharRunes},
		{"issue-status", "issue status", `UPDATE issues SET status=? WHERE id='old-1'`, currentShortVarcharRunes},
		{"issue-type", "issue type", `UPDATE issues SET issue_type=? WHERE id='old-1'`, currentShortVarcharRunes},
		{"issue-assignee", "issue assignee", `UPDATE issues SET assignee=? WHERE id='old-1'`, types.MaxFieldLen},
		{"issue-created-by", "issue created_by", `UPDATE issues SET created_by=? WHERE id='old-1'`, types.MaxFieldLen},
		{"issue-owner", "issue owner", `UPDATE issues SET owner=? WHERE id='old-1'`, types.MaxFieldLen},
		{"issue-external-ref", "issue external_ref", `UPDATE issues SET external_ref=? WHERE id='old-1'`, types.MaxFieldLen},
		{"issue-spec-id", "issue spec_id", `UPDATE issues SET spec_id=? WHERE id='old-1'`, currentSpecIDVarcharRunes},
		{"issue-compacted-at-commit", "issue compacted_at_commit", `UPDATE issues SET compacted_at_commit=? WHERE id='old-1'`, currentCommitVarcharRunes},
		{"issue-sender", "issue sender", `UPDATE issues SET sender=? WHERE id='old-1'`, types.MaxFieldLen},
		{"issue-wisp-type", "issue wisp_type", `UPDATE issues SET wisp_type=? WHERE id='old-1'`, currentShortVarcharRunes},
		{"issue-await-type", "issue await_type", `UPDATE issues SET await_type=? WHERE id='old-1'`, currentShortVarcharRunes},
		{"issue-await-id", "issue await_id", `UPDATE issues SET await_id=? WHERE id='old-1'`, types.MaxFieldLen},
		{"issue-mol-type", "issue mol_type", `UPDATE issues SET mol_type=? WHERE id='old-1'`, currentShortVarcharRunes},
		{"issue-event-kind", "issue event_kind", `UPDATE issues SET event_kind=? WHERE id='old-1'`, currentShortVarcharRunes},
		{"issue-actor", "issue actor", `UPDATE issues SET actor=? WHERE id='old-1'`, types.MaxFieldLen},
		{"issue-target", "issue target", `UPDATE issues SET target=? WHERE id='old-1'`, types.MaxFieldLen},
		{"issue-work-type", "issue work_type", `UPDATE issues SET work_type=? WHERE id='old-1'`, currentShortVarcharRunes},
		{"issue-source-system", "issue source_system", `UPDATE issues SET source_system=? WHERE id='old-1'`, types.MaxFieldLen},
		{"label-issue-id", "label issue_id", `INSERT INTO labels VALUES (?,'legacy')`, types.MaxFieldLen},
		{"label", "label", `INSERT INTO labels VALUES ('old-1',?)`, types.MaxFieldLen},
		{"dependency-source", "dependency issue_id", `INSERT INTO dependencies VALUES (?,'old-2','blocks','2026-01-02T03:04:05Z','old',NULL,NULL)`, types.MaxFieldLen},
		{"dependency-target", "dependency depends_on_id", `INSERT INTO dependencies VALUES ('old-1',?,'blocks','2026-01-02T03:04:05Z','old',NULL,NULL)`, types.MaxFieldLen},
		{"dependency-type", "dependency type", `INSERT INTO dependencies VALUES ('old-1','old-2',?,'2026-01-02T03:04:05Z','old',NULL,NULL)`, currentShortVarcharRunes},
		{"dependency-created-by", "dependency created_by", `INSERT INTO dependencies VALUES ('old-1','old-2','blocks','2026-01-02T03:04:05Z',?,NULL,NULL)`, types.MaxFieldLen},
		{"comment-issue-id", "comment issue_id", `INSERT INTO comments VALUES (1,?,'old','text','2026-01-02T03:04:05Z')`, types.MaxFieldLen},
		{"comment-author", "comment author", `INSERT INTO comments VALUES (1,'old-1',?,'text','2026-01-02T03:04:05Z')`, types.MaxFieldLen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.49.6")
			mustExec(t, db, tc.statement, strings.Repeat("é", tc.limit+1))
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "issues.jsonl")
			err := Export(context.Background(), path, output, &bytes.Buffer{})
			if err == nil {
				t.Fatal("Export accepted a value beyond the current VARCHAR limit")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Export error %q does not identify %q", err, tc.field)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("invalid output was published: %v", statErr)
			}
		})
	}
}

func TestExportMeasuresCurrentVarcharLimitsInRunes(t *testing.T) {
	db, path := legacyFixture(t, "0.49.6")
	id := strings.Repeat("é", types.MaxFieldLen)
	createdBy := strings.Repeat("界", types.MaxFieldLen)
	mustExec(t, db, `UPDATE issues SET id=?,created_by=? WHERE id='old-1'`, id, createdBy)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Export(context.Background(), path, "-", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), id) || !strings.Contains(out.String(), createdBy) {
		t.Fatal("at-limit multibyte values were not preserved")
	}
}

func TestExportRejectsDuplicateCommentImportIdentityBeforePublication(t *testing.T) {
	db, path := legacyFixture(t, "0.50.3")
	mustExec(t, db, `INSERT INTO comments VALUES
		(1,'old-1','old','same comment','2026-01-02T03:04:05Z'),
		(2,'old-1','old','same comment','2026-01-02T04:04:05+01:00')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Export(context.Background(), path, "-", &out)
	if err == nil {
		t.Fatal("Export accepted comments that the current importer would deduplicate")
	}
	if out.Len() != 0 {
		t.Fatalf("invalid output was published: %q", out.String())
	}
}

func TestExportRejectsChildValuesCurrentImporterWouldDefaultBeforePublication(t *testing.T) {
	tests := []struct {
		name, field, statement string
	}{
		{"dependency-zero-created-at", "dependency created_at", `INSERT INTO dependencies VALUES ('old-1','old-2','blocks','0001-01-01T00:00:00.4Z','old',NULL,NULL)`},
		{"dependency-empty-created-by", "dependency created_by", `INSERT INTO dependencies VALUES ('old-1','old-2','blocks','2026-01-02T03:04:05Z','',NULL,NULL)`},
		{"comment-zero-created-at", "comment created_at", `INSERT INTO comments VALUES (1,'old-1','old','text','0001-01-01T00:00:00.4Z')`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.49.6")
			mustExec(t, db, tc.statement)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "issues.jsonl")
			err := Export(context.Background(), path, output, &bytes.Buffer{})
			if err == nil {
				t.Fatal("Export accepted a child value the current importer would replace")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Export error %q does not identify %q", err, tc.field)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("invalid output was published: %v", statErr)
			}
		})
	}
}

func TestExportRejectsIssueTimestampsCurrentImporterWouldDefaultAfterRounding(t *testing.T) {
	for _, tc := range []struct {
		name, field string
	}{
		{"created-at", "created_at"},
		{"updated-at", "updated_at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.50.3")
			mustExec(t, db, `UPDATE issues SET `+tc.field+`='0001-01-01T00:00:00.4Z' WHERE id='old-1'`)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "issues.jsonl")
			err := Export(context.Background(), path, output, &bytes.Buffer{})
			if err == nil {
				t.Fatal("Export accepted an issue timestamp the current importer would replace")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Export error %q does not identify %q", err, tc.field)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("invalid output was published: %v", statErr)
			}
		})
	}
}

func TestExportRejectsNonStringWaitersBeforePublication(t *testing.T) {
	for _, tc := range []struct {
		name, waiters string
	}{
		{"null-element", `["a",null,"b"]`},
		{"number-element", `["a",1]`},
		{"not-an-array", `"a"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.50.3")
			mustExec(t, db, `UPDATE issues SET waiters=? WHERE id='old-1'`, tc.waiters)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := Export(context.Background(), path, "-", &out)
			if err == nil {
				t.Fatal("Export accepted waiters that are not an array of strings")
			}
			if out.Len() != 0 {
				t.Fatalf("invalid output was published: %q", out.String())
			}
		})
	}
}

func TestExportAcceptsStringWaitersAndValidMetadata(t *testing.T) {
	db, path := legacyFixture(t, "0.50.3")
	mustExec(t, db, `UPDATE issues SET metadata='{"legacy":true}',waiters='["a","é"]' WHERE id='old-1'`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Export(context.Background(), path, "-", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"metadata":{"legacy":true}`) ||
		!strings.Contains(out.String(), `"waiters":["a","é"]`) {
		t.Fatalf("valid metadata or string waiters were not preserved: %s", out.String())
	}
}

func TestExportRejectsLoneJSONSurrogatesBeforePublication(t *testing.T) {
	for _, tc := range []struct {
		name, column, value string
	}{
		{"waiters-high", "waiters", `["\ud800"]`},
		{"waiters-low", "waiters", `["\udc00"]`},
		{"metadata-high", "metadata", `{"x":"\ud800"}`},
		{"metadata-low", "metadata", `{"x":"\udc00"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.49.6")
			statement := "UPDATE issues SET " + tc.column + "=? WHERE id='old-1'"
			mustExec(t, db, statement, tc.value)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "issues.jsonl")
			err := Export(context.Background(), path, output, &bytes.Buffer{})
			if err == nil {
				t.Fatal("Export accepted a lone UTF-16 surrogate escape")
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("invalid output was published: %v", statErr)
			}
		})
	}
}

func TestExportAcceptsPairedAndEscapedBackslashJSONSurrogates(t *testing.T) {
	db, path := legacyFixture(t, "0.50.3")
	mustExec(t, db, `UPDATE issues SET metadata=?,waiters=? WHERE id='old-1'`,
		`{"pair":"\ud83d\ude00","literal":"\\ud800"}`,
		`["\ud83d\ude00","\\ud800"]`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Export(context.Background(), path, "-", &out); err != nil {
		t.Fatal(err)
	}
	var issue types.Issue
	if err := json.Unmarshal(bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})[0], &issue); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]string
	if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["pair"] != "😀" || metadata["literal"] != `\ud800` ||
		len(issue.Waiters) != 2 || issue.Waiters[0] != "😀" || issue.Waiters[1] != `\ud800` {
		t.Fatalf("valid surrogate JSON changed semantics: metadata=%#v waiters=%#v", metadata, issue.Waiters)
	}
}

func TestExportRejectsCurrentTextByteOverflowBeforePublication(t *testing.T) {
	const limit = 65_535
	baseWaiter := "<é"
	waiter := strings.Repeat("a", limit-len(issueops.FormatJSONStringArray([]string{baseWaiter}))) + baseWaiter + "b"
	encodedWaiters := issueops.FormatJSONStringArray([]string{waiter})
	if len(encodedWaiters) != limit+1 {
		t.Fatalf("waiter overflow fixture is %d bytes, want %d", len(encodedWaiters), limit+1)
	}
	rawWaiters := `["` + waiter + `"]`
	if len(rawWaiters) > limit {
		t.Fatalf("legacy waiter fixture is already %d bytes", len(rawWaiters))
	}
	for _, tc := range []struct {
		name, column, value string
	}{
		{"payload", "payload", strings.Repeat("p", limit+1)},
		{"waiters", "waiters", rawWaiters},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.49.6")
			statement := "UPDATE issues SET " + tc.column + "=? WHERE id='old-1'"
			mustExec(t, db, statement, tc.value)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "issues.jsonl")
			err := Export(context.Background(), path, output, &bytes.Buffer{})
			if err == nil {
				t.Fatal("Export accepted a value beyond the current TEXT byte limit")
			}
			if !strings.Contains(err.Error(), tc.column) {
				t.Fatalf("Export error %q does not identify %q", err, tc.column)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("invalid output was published: %v", statErr)
			}
		})
	}
}

func TestExportAcceptsCurrentTextByteBoundaries(t *testing.T) {
	const limit = 65_535
	baseWaiter := "<é"
	waiter := strings.Repeat("a", limit-len(issueops.FormatJSONStringArray([]string{baseWaiter}))) + baseWaiter
	encodedWaiters := issueops.FormatJSONStringArray([]string{waiter})
	if len(encodedWaiters) != limit {
		t.Fatalf("waiter boundary fixture is %d bytes, want %d", len(encodedWaiters), limit)
	}
	rawWaiters := `["` + waiter + `"]`
	if len(rawWaiters) >= limit {
		t.Fatalf("legacy waiter fixture is %d bytes; expected HTML escaping to expand it", len(rawWaiters))
	}
	db, path := legacyFixture(t, "0.50.3")
	mustExec(t, db, `UPDATE issues SET payload=?,waiters=? WHERE id='old-1'`,
		strings.Repeat("p", limit), rawWaiters)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Export(context.Background(), path, "-", &out); err != nil {
		t.Fatal(err)
	}
	var issue types.Issue
	if err := json.Unmarshal(bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})[0], &issue); err != nil {
		t.Fatal(err)
	}
	if len(issue.Payload) != limit || len(issueops.FormatJSONStringArray(issue.Waiters)) != limit ||
		len(issue.Waiters) != 1 || !strings.HasSuffix(issue.Waiters[0], baseWaiter) {
		t.Fatal("exact TEXT byte boundaries were not preserved")
	}
}

func TestExportRejectsEphemeralCommentTextOverflowBeforePublication(t *testing.T) {
	db, path := legacyFixture(t, "0.49.6")
	mustExec(t, db, `UPDATE issues SET ephemeral=1 WHERE id='old-1'`)
	mustExec(t, db, `INSERT INTO comments VALUES (1,'old-1','old',?,'2026-01-02T03:04:05Z')`,
		strings.Repeat("c", currentTextBytes+1))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "issues.jsonl")
	err := Export(context.Background(), path, output, &bytes.Buffer{})
	if err == nil {
		t.Fatal("Export accepted an ephemeral comment beyond the current TEXT byte limit")
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("invalid output was published: %v", statErr)
	}
}

func TestExportAcceptsEphemeralAndDurableCommentTextBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ephemeral bool
		size      int
	}{
		{"ephemeral-at-limit", true, currentTextBytes},
		{"durable-over-text-limit", false, currentTextBytes + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.50.3")
			if tc.ephemeral {
				mustExec(t, db, `UPDATE issues SET ephemeral=1 WHERE id='old-1'`)
			}
			mustExec(t, db, `INSERT INTO comments VALUES (1,'old-1','old',?,'2026-01-02T03:04:05Z')`,
				strings.Repeat("c", tc.size))
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := Export(context.Background(), path, "-", &out); err != nil {
				t.Fatal(err)
			}
			var issue types.Issue
			if err := json.Unmarshal(bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})[0], &issue); err != nil {
				t.Fatal(err)
			}
			if len(issue.Comments) != 1 || len(issue.Comments[0].Text) != tc.size {
				t.Fatal("comment text boundary was not preserved")
			}
		})
	}
}

func TestExportRejectsInvalidOptionalTimesBeforePublication(t *testing.T) {
	for _, tc := range []struct {
		name, field, statement string
	}{
		{"closed-at", "closed_at", `UPDATE issues SET status='closed',closed_at='not-a-time' WHERE id='old-1'`},
		{"compacted-at", "compacted_at", `UPDATE issues SET compacted_at='not-a-time' WHERE id='old-1'`},
		{"due-at", "due_at", `UPDATE issues SET due_at='not-a-time' WHERE id='old-1'`},
		{"defer-until", "defer_until", `UPDATE issues SET defer_until='not-a-time' WHERE id='old-1'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.49.6")
			mustExec(t, db, tc.statement)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "issues.jsonl")
			err := Export(context.Background(), path, output, &bytes.Buffer{})
			if err == nil {
				t.Fatal("Export accepted an invalid optional timestamp")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Export error %q does not identify %q", err, tc.field)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("invalid output was published: %v", statErr)
			}
		})
	}
}

func TestExportCanonicalizesTimestampsForCurrentImport(t *testing.T) {
	db, path := legacyFixture(t, "0.50.3")
	mustExec(t, db, `UPDATE issues SET status='closed',
		created_at='2026-01-02T03:04:05.987654321-02:00',
		updated_at='2026-01-02T03:04:06.876543210Z',
		closed_at='2026-01-02 03:04:07.765432100',
		compacted_at='2026-01-03T04:05:06.5Z',
		due_at='2026-01-04 05:06:07.123456789',
		defer_until='2026-01-05T07:08:09.999999999+02:00' WHERE id='old-1'`)
	mustExec(t, db, `UPDATE issues SET due_at='0001-01-01T00:00:00Z' WHERE id='old-2'`)
	mustExec(t, db, `INSERT INTO dependencies VALUES
		('old-1','old-2','blocks','2026-01-06T10:11:12.555555555-03:00','old',NULL,NULL)`)
	mustExec(t, db, `INSERT INTO comments VALUES
		(1,'old-1','old','fractional','2026-01-07 11:12:13.444444444')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Export(context.Background(), path, "-", &out); err != nil {
		t.Fatal(err)
	}
	var issues []types.Issue
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'}) {
		var issue types.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			t.Fatal(err)
		}
		issues = append(issues, issue)
	}
	if len(issues) != 2 {
		t.Fatalf("exported %d issues, want 2", len(issues))
	}
	first, second := issues[0], issues[1]
	if !first.CreatedAt.Equal(time.Date(2026, 1, 2, 5, 4, 6, 0, time.UTC)) ||
		!first.UpdatedAt.Equal(time.Date(2026, 1, 2, 3, 4, 7, 0, time.UTC)) ||
		first.ClosedAt == nil || !first.ClosedAt.Equal(time.Date(2026, 1, 2, 3, 4, 8, 0, time.UTC)) ||
		first.CompactedAt == nil || !first.CompactedAt.Equal(time.Date(2026, 1, 3, 4, 5, 7, 0, time.UTC)) ||
		first.DueAt == nil || !first.DueAt.Equal(time.Date(2026, 1, 4, 5, 6, 7, 0, time.UTC)) ||
		first.DeferUntil == nil || !first.DeferUntil.Equal(time.Date(2026, 1, 5, 5, 8, 10, 0, time.UTC)) {
		t.Fatalf("issue timestamps were not canonicalized: %#v", first)
	}
	if len(first.Dependencies) != 1 ||
		!first.Dependencies[0].CreatedAt.Equal(time.Date(2026, 1, 6, 13, 11, 13, 0, time.UTC)) ||
		len(first.Comments) != 1 ||
		!first.Comments[0].CreatedAt.Equal(time.Date(2026, 1, 7, 11, 12, 13, 0, time.UTC)) {
		t.Fatalf("child timestamps were not canonicalized: %#v %#v", first.Dependencies, first.Comments)
	}
	if second.DueAt == nil || !second.DueAt.IsZero() {
		t.Fatalf("parseable year-one optional timestamp was not preserved: %#v", second.DueAt)
	}
}

func TestExportPreservesCurrentDatetimeBoundaryYears(t *testing.T) {
	db, path := legacyFixture(t, "0.50.3")
	mustExec(t, db, `UPDATE issues SET
		created_at='0000-01-01T00:00:00Z',
		updated_at='9999-12-31T23:59:59Z',
		due_at='0000-01-01T00:00:00Z',
		defer_until='9999-12-31T23:59:59Z' WHERE id='old-1'`)
	mustExec(t, db, `INSERT INTO dependencies VALUES
		('old-1','old-2','blocks','0000-01-01T00:00:00Z','old',NULL,NULL)`)
	mustExec(t, db, `INSERT INTO comments VALUES
		(1,'old-1','old','boundary','9999-12-31T23:59:59Z')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Export(context.Background(), path, "-", &out); err != nil {
		t.Fatal(err)
	}
	var issue types.Issue
	if err := json.Unmarshal(bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})[0], &issue); err != nil {
		t.Fatal(err)
	}
	if issue.CreatedAt.Year() != 0 || issue.UpdatedAt.Year() != 9999 ||
		issue.DueAt == nil || issue.DueAt.Year() != 0 ||
		issue.DeferUntil == nil || issue.DeferUntil.Year() != 9999 ||
		len(issue.Dependencies) != 1 || issue.Dependencies[0].CreatedAt.Year() != 0 ||
		len(issue.Comments) != 1 || issue.Comments[0].CreatedAt.Year() != 9999 {
		t.Fatalf("current DATETIME boundary years were not preserved: %#v", issue)
	}
}

func TestExportRejectsTimestampsOutsideCurrentDatetimeRangeAfterCanonicalization(t *testing.T) {
	tests := []struct {
		name      string
		statement string
	}{
		{"issue-lower-offset", `UPDATE issues SET created_at='0000-01-01T00:00:00+14:00' WHERE id='old-1'`},
		{"issue-upper-offset", `UPDATE issues SET updated_at='9999-12-31T23:59:59-14:00' WHERE id='old-1'`},
		{"optional-round-overflow", `UPDATE issues SET due_at='9999-12-31T23:59:59.5Z' WHERE id='old-1'`},
		{"dependency-round-overflow", `INSERT INTO dependencies VALUES ('old-1','old-2','blocks','9999-12-31T23:59:59.5Z','old',NULL,NULL)`},
		{"comment-round-overflow", `INSERT INTO comments VALUES (1,'old-1','old','overflow','9999-12-31T23:59:59.5Z')`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.50.3")
			mustExec(t, db, tc.statement)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "issues.jsonl")
			err := Export(context.Background(), path, output, &bytes.Buffer{})
			if err == nil {
				t.Fatal("Export accepted a timestamp outside the current DATETIME range")
			}
			if !strings.Contains(err.Error(), "outside current DATETIME range") {
				t.Fatalf("Export returned unexpected error: %v", err)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("invalid output was published: %v", statErr)
			}
		})
	}
}

func TestExportRejectsCurrentIntOverflowBeforePublication(t *testing.T) {
	tests := []struct {
		name, field, statement string
		value                  int64
	}{
		{"estimated-minutes-overflow", "estimated_minutes", `UPDATE issues SET estimated_minutes=? WHERE id='old-1'`, math.MaxInt32 + 1},
		{"estimated-minutes-underflow", "estimated_minutes", `UPDATE issues SET estimated_minutes=? WHERE id='old-1'`, math.MinInt32 - 1},
		{"estimated-minutes-negative", "estimated_minutes", `UPDATE issues SET estimated_minutes=? WHERE id='old-1'`, -1},
		{"compaction-level-overflow", "compaction_level", `UPDATE issues SET compaction_level=? WHERE id='old-1'`, math.MaxInt32 + 1},
		{"compaction-level-underflow", "compaction_level", `UPDATE issues SET compaction_level=? WHERE id='old-1'`, math.MinInt32 - 1},
		{"original-size-overflow", "original_size", `UPDATE issues SET original_size=? WHERE id='old-1'`, math.MaxInt32 + 1},
		{"original-size-underflow", "original_size", `UPDATE issues SET original_size=? WHERE id='old-1'`, math.MinInt32 - 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.49.6")
			mustExec(t, db, tc.statement, tc.value)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "issues.jsonl")
			err := Export(context.Background(), path, output, &bytes.Buffer{})
			if err == nil {
				t.Fatal("Export accepted a value outside the current INT range")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Export error %q does not identify %q", err, tc.field)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("invalid output was published: %v", statErr)
			}
		})
	}
}

func TestExportAcceptsCurrentIntBoundaries(t *testing.T) {
	db, path := legacyFixture(t, "0.50.3")
	mustExec(t, db, `UPDATE issues SET estimated_minutes=?,compaction_level=?,original_size=? WHERE id='old-1'`,
		math.MaxInt32, math.MinInt32, math.MaxInt32)
	mustExec(t, db, `UPDATE issues SET compaction_level=?,original_size=? WHERE id='old-2'`,
		math.MaxInt32, math.MinInt32)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Export(context.Background(), path, "-", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"estimated_minutes":2147483647`) ||
		!strings.Contains(out.String(), `"compaction_level":-2147483648`) ||
		!strings.Contains(out.String(), `"original_size":-2147483648`) {
		t.Fatalf("INT boundary values were not preserved: %s", out.String())
	}
}

func TestExportRejectsInvalidUTF8BeforePublication(t *testing.T) {
	tests := []struct {
		name, field, statement string
	}{
		{"issue-id", "issue id", `UPDATE issues SET id=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-title", "issue title", `UPDATE issues SET title=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-description", "issue description", `UPDATE issues SET description=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-design", "issue design", `UPDATE issues SET design=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-acceptance", "issue acceptance_criteria", `UPDATE issues SET acceptance_criteria=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-notes", "issue notes", `UPDATE issues SET notes=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-status", "issue status", `UPDATE issues SET status=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-type", "issue type", `UPDATE issues SET issue_type=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-assignee", "issue assignee", `UPDATE issues SET assignee=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-created-by", "issue created_by", `UPDATE issues SET created_by=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-owner", "issue owner", `UPDATE issues SET owner=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-external-ref", "issue external_ref", `UPDATE issues SET external_ref=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-spec-id", "issue spec_id", `UPDATE issues SET spec_id=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-compacted-at-commit", "issue compacted_at_commit", `UPDATE issues SET compacted_at_commit=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-close-reason", "issue close_reason", `UPDATE issues SET close_reason=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-sender", "issue sender", `UPDATE issues SET sender=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-wisp-type", "issue wisp_type", `UPDATE issues SET wisp_type=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-await-type", "issue await_type", `UPDATE issues SET await_type=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-await-id", "issue await_id", `UPDATE issues SET await_id=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-waiters", "issue waiters", `UPDATE issues SET waiters=CAST(X'5b2280225d' AS TEXT) WHERE id='old-1'`},
		{"issue-mol-type", "issue mol_type", `UPDATE issues SET mol_type=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-work-type", "issue work_type", `UPDATE issues SET work_type=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-source-system", "issue source_system", `UPDATE issues SET source_system=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-metadata", "issue metadata", `UPDATE issues SET metadata=CAST(X'7b2278223a2280227d' AS TEXT) WHERE id='old-1'`},
		{"issue-event-kind", "issue event_kind", `UPDATE issues SET event_kind=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-actor", "issue actor", `UPDATE issues SET actor=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-target", "issue target", `UPDATE issues SET target=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"issue-payload", "issue payload", `UPDATE issues SET payload=CAST(X'80' AS TEXT) WHERE id='old-1'`},
		{"label-issue-id", "label issue_id", `INSERT INTO labels VALUES (CAST(X'80' AS TEXT),'legacy')`},
		{"label", "label", `INSERT INTO labels VALUES ('old-1',CAST(X'80' AS TEXT))`},
		{"dependency-source", "dependency issue_id", `INSERT INTO dependencies VALUES (CAST(X'80' AS TEXT),'old-2','blocks','2026-01-02T03:04:05Z','old',NULL,NULL)`},
		{"dependency-target", "dependency depends_on_id", `INSERT INTO dependencies VALUES ('old-1',CAST(X'80' AS TEXT),'blocks','2026-01-02T03:04:05Z','old',NULL,NULL)`},
		{"dependency-type", "dependency type", `INSERT INTO dependencies VALUES ('old-1','old-2',CAST(X'80' AS TEXT),'2026-01-02T03:04:05Z','old',NULL,NULL)`},
		{"dependency-created-by", "dependency created_by", `INSERT INTO dependencies VALUES ('old-1','old-2','blocks','2026-01-02T03:04:05Z',CAST(X'80' AS TEXT),NULL,NULL)`},
		{"comment-issue-id", "comment issue_id", `INSERT INTO comments VALUES (1,CAST(X'80' AS TEXT),'old','text','2026-01-02T03:04:05Z')`},
		{"comment-author", "comment author", `INSERT INTO comments VALUES (1,'old-1',CAST(X'80' AS TEXT),'text','2026-01-02T03:04:05Z')`},
		{"comment-text", "comment text", `INSERT INTO comments VALUES (1,'old-1','old',CAST(X'80' AS TEXT),'2026-01-02T03:04:05Z')`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, path := legacyFixture(t, "0.49.6")
			mustExec(t, db, tc.statement)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "issues.jsonl")
			err := Export(context.Background(), path, output, &bytes.Buffer{})
			if err == nil {
				t.Fatal("Export accepted invalid UTF-8")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Export error %q does not identify %q", err, tc.field)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("invalid output was published: %v", statErr)
			}
		})
	}
}

func TestHasDirectedCycleLargeChain(t *testing.T) {
	const edgeCount = 10_000
	graph := make(map[string][]string, edgeCount+1)
	for i := 0; i < edgeCount; i++ {
		graph[strconv.Itoa(i)] = []string{strconv.Itoa(i + 1)}
	}
	if hasDirectedCycle(graph) {
		t.Fatal("acyclic chain reported as cyclic")
	}
	graph[strconv.Itoa(edgeCount)] = []string{"0"}
	if !hasDirectedCycle(graph) {
		t.Fatal("cycle was not detected")
	}
}

func TestExportRejectsSourceSidecarSymlinks(t *testing.T) {
	db, path := legacyFixture(t, "0.50.3")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, path+"-journal"); err != nil {
		t.Fatal(err)
	}
	if err := Export(context.Background(), path, "-", &bytes.Buffer{}); err == nil {
		t.Fatal("accepted dangling journal symlink")
	}
	if err := os.Remove(path + "-journal"); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "source-alias.db")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := Export(context.Background(), alias, "-", &bytes.Buffer{}); err == nil {
		t.Fatal("accepted source symlink")
	}
}

func TestExportPublishesOnlyCompleteSpoolAndRejectsAliases(t *testing.T) {
	db, path := legacyFixture(t, "0.50.3")
	if _, err := db.Exec(`INSERT INTO issues (id,title,status,priority,issue_type,created_at,updated_at,metadata) VALUES ('bad','bad','open',2,'task','2026-01-02T03:04:05Z','bad-time','{}')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := Export(context.Background(), path, output, &bytes.Buffer{}); err == nil {
		t.Fatal("Export accepted invalid data")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("partial output was published: %v", err)
	}
	for _, output := range []string{path, path + "-wal", path + "-shm"} {
		if err := Export(context.Background(), path, output, &bytes.Buffer{}); err == nil {
			t.Fatalf("Export accepted output alias %q", output)
		}
	}
	hardLink := filepath.Join(t.TempDir(), "hard-link.jsonl")
	if err := os.Link(path, hardLink); err != nil {
		t.Fatal(err)
	}
	if err := Export(context.Background(), path, hardLink, &bytes.Buffer{}); err == nil {
		t.Fatal("Export accepted hard-link output alias")
	}
	symlink := filepath.Join(t.TempDir(), "symlink.jsonl")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if err := Export(context.Background(), path, symlink, &bytes.Buffer{}); err == nil {
		t.Fatal("Export accepted symlink output alias")
	}
	aliasDir := filepath.Join(t.TempDir(), "alias-dir")
	if err := os.Symlink(filepath.Dir(path), aliasDir); err != nil {
		t.Fatal(err)
	}
	if err := Export(context.Background(), path, filepath.Join(aliasDir, filepath.Base(path)+"-journal"), &bytes.Buffer{}); err == nil {
		t.Fatal("Export accepted output alias through symlinked parent")
	}
}

func TestExportReadsCommittedWALCopy(t *testing.T) {
	db, path := legacyFixture(t, "0.50.3")
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO issues (id,title,status,priority,issue_type,created_at,updated_at,metadata) VALUES ('wal-row','wal row','open',2,'task','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("expected WAL sidecar: %v", err)
	}
	var out bytes.Buffer
	if err := Export(context.Background(), path, "-", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":"wal-row"`) {
		t.Fatal("WAL-resident committed row was not exported")
	}
}

func TestSourceFingerprintTracksChange(t *testing.T) {
	db, path := legacyFixture(t, "0.49.6")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := fingerprintSource(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE issues SET title='changed' WHERE id='old-1'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := fingerprintSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if sameSet(before, after) {
		t.Fatal("fingerprint did not detect changed source")
	}
}

func mutations(statements ...string) func(*testing.T, *sql.DB) {
	return func(t *testing.T, db *sql.DB) {
		for _, statement := range statements {
			mustExec(t, db, statement)
		}
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func legacyFixture(t *testing.T, version string) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "beads.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range authenticDDL() {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO metadata VALUES ('bd_version', ?)`, version); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old-1", "old-2"} {
		if _, err := db.Exec(`INSERT INTO issues (id,title,status,priority,issue_type,created_at,updated_at,metadata) VALUES (?,?,'open',2,'task','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z','{}')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	return db, path
}

func authenticDDL() []string {
	return []string{
		`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE issues (id TEXT PRIMARY KEY, content_hash TEXT, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', design TEXT NOT NULL DEFAULT '', acceptance_criteria TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'open', priority INTEGER NOT NULL DEFAULT 2, issue_type TEXT NOT NULL DEFAULT 'task', assignee TEXT, estimated_minutes INTEGER, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, created_by TEXT DEFAULT '', owner TEXT DEFAULT '', updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, closed_at DATETIME, closed_by_session TEXT DEFAULT '', external_ref TEXT, spec_id TEXT, compaction_level INTEGER DEFAULT 0, compacted_at DATETIME, compacted_at_commit TEXT, original_size INTEGER, deleted_at DATETIME, deleted_by TEXT DEFAULT '', delete_reason TEXT DEFAULT '', original_type TEXT DEFAULT '', sender TEXT DEFAULT '', ephemeral INTEGER DEFAULT 0, wisp_type TEXT DEFAULT '', pinned INTEGER DEFAULT 0, is_template INTEGER DEFAULT 0, crystallizes INTEGER DEFAULT 0, mol_type TEXT DEFAULT '', work_type TEXT DEFAULT 'mutex', quality_score REAL, source_system TEXT DEFAULT '', metadata TEXT NOT NULL DEFAULT '{}', event_kind TEXT DEFAULT '', actor TEXT DEFAULT '', target TEXT DEFAULT '', payload TEXT DEFAULT '', source_repo TEXT DEFAULT '.', close_reason TEXT DEFAULT '', await_type TEXT, await_id TEXT, timeout_ns INTEGER, waiters TEXT, hook_bead TEXT DEFAULT '', role_bead TEXT DEFAULT '', agent_state TEXT DEFAULT '', last_activity DATETIME, role_type TEXT DEFAULT '', rig TEXT DEFAULT '', due_at DATETIME, defer_until DATETIME)`,
		`CREATE TABLE dependencies (issue_id TEXT NOT NULL, depends_on_id TEXT NOT NULL, type TEXT NOT NULL DEFAULT 'blocks', created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, created_by TEXT NOT NULL, metadata TEXT, thread_id TEXT, PRIMARY KEY(issue_id,depends_on_id,type), FOREIGN KEY(issue_id) REFERENCES issues(id) ON DELETE CASCADE)`,
		`CREATE TABLE labels (issue_id TEXT NOT NULL, label TEXT NOT NULL, PRIMARY KEY(issue_id,label), FOREIGN KEY(issue_id) REFERENCES issues(id) ON DELETE CASCADE)`,
		`CREATE TABLE comments (id INTEGER PRIMARY KEY, issue_id TEXT NOT NULL, author TEXT NOT NULL, text TEXT NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, FOREIGN KEY(issue_id) REFERENCES issues(id) ON DELETE CASCADE)`,
	}
}

// TestLoadIssuesProjectionArity guards the positional contract between
// loadIssuesProjection and issueops.ScanIssueFrom. ScanIssueFrom appends the
// legacy trailing dests through a variadic `...any`, so a column added to the
// canonical issueops.IssueSelectColumns without a matching NULL/0 placeholder in
// loadIssuesProjection still compiles and only fails at runtime, mid-migration
// (exactly the storage_class drift this test was written for). Asserting the
// column counts agree turns that drift into a test failure instead.
func TestLoadIssuesProjectionArity(t *testing.T) {
	canonical := topLevelColumns(issueops.IssueSelectColumns)
	trailing := len((&legacyExtras{}).scanDests())
	got := topLevelColumns(loadIssuesProjection)
	if want := canonical + trailing; got != want {
		t.Fatalf("loadIssuesProjection selects %d columns but ScanIssueFrom scans %d "+
			"(%d canonical issueops.IssueSelectColumns + %d legacy trailing dests); "+
			"a canonical column was likely added without a matching NULL/0 placeholder in loadIssuesProjection",
			got, want, canonical, trailing)
	}
}

// topLevelColumns counts the comma-separated columns in a SQL projection,
// ignoring commas nested inside parenthesized expressions such as
// CAST(x AS TEXT).
func topLevelColumns(projection string) int {
	depth, start, count := 0, 0, 0
	for i, r := range projection {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				if strings.TrimSpace(projection[start:i]) != "" {
					count++
				}
				start = i + 1
			}
		}
	}
	if strings.TrimSpace(projection[start:]) != "" {
		count++
	}
	return count
}
