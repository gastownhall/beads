package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestValidateMigration0059ExecutionPreservesFrozenBytes(t *testing.T) {
	const name = "0059_recompute_null_gate_is_blocked.up.sql"
	original, err := mainSource.files.ReadFile(mainSource.dir + "/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	before := append([]byte(nil), original...)

	if err := validateMigration0059Execution(mainSource, migrationFile{version: 59, name: name}, original); err != nil {
		t.Fatalf("validateMigration0059Execution: %v", err)
	}
	if string(original) != string(before) {
		t.Fatal("validateMigration0059Execution mutated the embedded migration bytes")
	}
}

func TestMigration0059BlockedClosureProductionShape(t *testing.T) {
	graph := migration0059Graph{nodes: make(map[migration0059Node]migration0059NodeState)}
	open := sql.NullString{String: "open", Valid: true}
	for i := 0; i < 285; i++ {
		graph.nodes[migration0059Node{kind: migration0059IssueKind, id: fmt.Sprintf("issue-%03d", i)}] = migration0059NodeState{status: open}
	}
	for i := 0; i < 11654; i++ {
		graph.nodes[migration0059Node{kind: migration0059WispKind, id: fmt.Sprintf("wisp-%05d", i)}] = migration0059NodeState{status: open}
	}
	graph.dependencies = append(graph.dependencies, migration0059Dependency{
		source:   migration0059Node{kind: migration0059WispKind, id: "wisp-00000"},
		target:   migration0059Node{kind: migration0059IssueKind, id: "issue-000"},
		typeName: "blocks",
	})
	for i := 1; i < 11654; i++ {
		graph.dependencies = append(graph.dependencies, migration0059Dependency{
			source:   migration0059Node{kind: migration0059WispKind, id: fmt.Sprintf("wisp-%05d", i)},
			target:   migration0059Node{kind: migration0059WispKind, id: fmt.Sprintf("wisp-%05d", i-1)},
			typeName: "parent-child",
		})
	}
	for len(graph.dependencies) < 14146 {
		i := len(graph.dependencies) % 285
		graph.dependencies = append(graph.dependencies, migration0059Dependency{
			source:   migration0059Node{kind: migration0059IssueKind, id: fmt.Sprintf("issue-%03d", i)},
			target:   migration0059Node{kind: migration0059IssueKind, id: fmt.Sprintf("issue-%03d", (i+1)%285)},
			typeName: "relates-to",
		})
	}

	blocked := migration0059BlockedClosure(graph)
	if got := len(blocked); got != 11654 {
		t.Fatalf("blocked closure size = %d, want 11654", got)
	}
	if !blocked[migration0059Node{kind: migration0059WispKind, id: "wisp-11653"}] {
		t.Fatal("deepest production-shaped descendant is not blocked")
	}
}

func TestValidateMigration0059ExecutionFailsClosedOnDrift(t *testing.T) {
	const name = "0059_recompute_null_gate_is_blocked.up.sql"
	original, err := mainSource.files.ReadFile(mainSource.dir + "/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	drifted := append([]byte(nil), original...)
	drifted = append(drifted, []byte("\n-- unexpected drift\n")...)

	err = validateMigration0059Execution(mainSource, migrationFile{version: 59, name: name}, drifted)
	if err == nil {
		t.Fatal("validateMigration0059Execution accepted drifted migration 0059 bytes")
	}
	if !strings.Contains(err.Error(), "unexpected content hash") {
		t.Fatalf("error = %q, want unexpected content hash", err)
	}
}

func TestValidateMigration0059ExecutionFailsClosedOnNameDrift(t *testing.T) {
	err := validateMigration0059Execution(mainSource, migrationFile{version: 59, name: "0059_replaced.up.sql"}, []byte("SELECT 1;"))
	if err == nil {
		t.Fatal("validateMigration0059Execution accepted a renamed main migration 0059")
	}
	if !strings.Contains(err.Error(), "unexpected filename") {
		t.Fatalf("error = %q, want unexpected filename", err)
	}
}

func TestRunMigrationsExecutesLinear0059AndRecordsFrozenHash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	previousCounter := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 285, nil }
	t.Cleanup(func() { issueRowCounter = previousCounter })

	const name = "0059_recompute_null_gate_is_blocked.up.sql"
	original, err := mainSource.files.ReadFile(mainSource.dir + "/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	sum := sha256.Sum256(original)
	wantHash := hex.EncodeToString(sum[:])

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, is_blocked FROM issues")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "is_blocked"}).
			AddRow("blocked", "open", 0).
			AddRow("target", "open", 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, type, metadata FROM dependencies")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "depends_on_issue_id", "depends_on_wisp_id", "type", "metadata"}).
			AddRow("blocked", "target", nil, "blocks", `{}`))
	mock.ExpectQuery(`(?s)SELECT.*INFORMATION_SCHEMA.TABLES.*INFORMATION_SCHEMA.COLUMNS`).
		WillReturnRows(sqlmock.NewRows([]string{"tables", "columns"}).AddRow(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE issues\nSET is_blocked = ?, updated_at = updated_at\nWHERE id = ? AND NOT (is_blocked <=> ?)")).
		WithArgs(1, "blocked", 1).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO schema_migrations (version, content_hash) VALUES (?, ?)")).
		WithArgs(59, wantHash).
		WillReturnResult(sqlmock.NewResult(0, 1))

	applied, err := runMigrations(context.Background(), db, mainSource, 58, 59, false)
	if err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if applied != 1 {
		t.Fatalf("runMigrations applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
