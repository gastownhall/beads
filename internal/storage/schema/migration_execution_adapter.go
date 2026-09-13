package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	migration0059Name = "0059_recompute_null_gate_is_blocked.up.sql"
	migration0059Hash = "f0186029a1c87d579b67e0540ec22ad0bd0fc59793b07f7d24a24e122b77c394"

	migration0059IssueKind = "issue"
	migration0059WispKind  = "wisp"
)

type migration0059Node struct {
	kind string
	id   string
}

type migration0059NodeState struct {
	status    sql.NullString
	isBlocked int
}

type migration0059Dependency struct {
	source   migration0059Node
	target   migration0059Node
	typeName string
	gate     string
}

type migration0059Graph struct {
	nodes        map[migration0059Node]migration0059NodeState
	dependencies []migration0059Dependency
}

type migrationTxBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// validateMigration0059Execution pins the runtime repair to the exact frozen
// migration identity. The embedded bytes stay unchanged and runMigrations
// records their original hash. Unknown source, name, version, or content fails
// closed rather than applying these semantics to a different migration.
func validateMigration0059Execution(src migrationSource, mf migrationFile, original []byte) error {
	if src.cursorTable != mainSource.cursorTable || src.dir != mainSource.dir || mf.version != 59 {
		return fmt.Errorf("migration 0059 execution requested for non-target source/version")
	}
	if mf.name != migration0059Name {
		return fmt.Errorf("main migration 0059 has unexpected filename %q (want %q)", mf.name, migration0059Name)
	}
	sum := sha256.Sum256(original)
	gotHash := hex.EncodeToString(sum[:])
	if gotHash != migration0059Hash {
		return fmt.Errorf("migration %s has unexpected content hash %s (want %s)", mf.name, gotHash, migration0059Hash)
	}
	return nil
}

// execMigration0059 computes the frozen migration's result without executing
// its pathological recursive SQL. It loads each graph row once, computes the
// typed blocked closure in memory, and updates only issue flags that differ.
// No helper DDL is created, so an interrupted attempt cannot leave stand-ins
// that trip the next migration's dirty-table guard. The reads and writes share
// one SQL transaction when the caller is a DB or pinned connection; a caller-
// owned transaction is used as-is.
func execMigration0059(ctx context.Context, db DBConn) error {
	runner := db
	var tx *sql.Tx
	if beginner, ok := db.(migrationTxBeginner); ok {
		var err error
		tx, err = beginner.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration 0059 execution transaction: %w", err)
		}
		runner = tx
	}

	err := execMigration0059InTx(ctx, runner)
	if tx == nil {
		return err
	}
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return fmt.Errorf("%w (rollback migration 0059 execution: %v)", err, rollbackErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration 0059 execution transaction: %w", err)
	}
	return nil
}

func execMigration0059InTx(ctx context.Context, db DBConn) error {
	graph, err := loadMigration0059Graph(ctx, db)
	if err != nil {
		return err
	}
	if err := runMigration0059FaultHook("after_load"); err != nil {
		return err
	}

	blocked := migration0059BlockedClosure(graph)
	if err := runMigration0059FaultHook("after_compute"); err != nil {
		return err
	}

	issueIDs := make([]string, 0, len(graph.nodes))
	for node := range graph.nodes {
		if node.kind == migration0059IssueKind {
			issueIDs = append(issueIDs, node.id)
		}
	}
	sort.Strings(issueIDs)
	for _, issueID := range issueIDs {
		node := migration0059Node{kind: migration0059IssueKind, id: issueID}
		state := graph.nodes[node]
		value := 0
		if migration0059NodeOpen(state.status) && blocked[node] {
			value = 1
		}
		if value == state.isBlocked {
			continue
		}
		if _, err := db.ExecContext(ctx, `UPDATE issues
SET is_blocked = ?, updated_at = updated_at
WHERE id = ? AND NOT (is_blocked <=> ?)`, value, issueID, value); err != nil {
			return fmt.Errorf("update migration 0059 blocked state for %s: %w", issueID, err)
		}
	}
	if err := runMigration0059FaultHook("after_apply"); err != nil {
		return err
	}
	return nil
}

func loadMigration0059Graph(ctx context.Context, db DBConn) (migration0059Graph, error) {
	graph := migration0059Graph{nodes: make(map[migration0059Node]migration0059NodeState)}
	rows, err := db.QueryContext(ctx, "SELECT id, status, is_blocked FROM issues")
	if err != nil {
		return graph, fmt.Errorf("load migration 0059 issues: %w", err)
	}
	for rows.Next() {
		var id string
		var status sql.NullString
		var isBlocked int
		if err := rows.Scan(&id, &status, &isBlocked); err != nil {
			if closeErr := rows.Close(); closeErr != nil {
				return graph, fmt.Errorf("scan migration 0059 issue: %w (close issue rows: %v)", err, closeErr)
			}
			return graph, fmt.Errorf("scan migration 0059 issue: %w", err)
		}
		graph.nodes[migration0059Node{kind: migration0059IssueKind, id: id}] = migration0059NodeState{
			status: status, isBlocked: isBlocked,
		}
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		if closeErr != nil {
			return graph, fmt.Errorf("iterate migration 0059 issues: %w (close issue rows: %v)", iterationErr, closeErr)
		}
		return graph, fmt.Errorf("iterate migration 0059 issues: %w", iterationErr)
	}
	if closeErr != nil {
		return graph, fmt.Errorf("close migration 0059 issue rows: %w", closeErr)
	}

	deps, err := loadMigration0059Dependencies(ctx, db, "dependencies", migration0059IssueKind)
	if err != nil {
		return graph, err
	}
	graph.dependencies = append(graph.dependencies, deps...)

	var tableCount, splitColumnCount int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
   WHERE TABLE_SCHEMA = DATABASE()
     AND TABLE_NAME IN ('wisps', 'wisp_dependencies')),
  (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE()
     AND TABLE_NAME = 'wisp_dependencies'
     AND COLUMN_NAME IN ('depends_on_issue_id', 'depends_on_wisp_id'))`).Scan(&tableCount, &splitColumnCount); err != nil {
		return graph, fmt.Errorf("inspect migration 0059 wisp shape: %w", err)
	}
	// This matches the frozen migration's guarded copy: absent or legacy-
	// shaped clone-local wisp tables degrade to an empty wisp plane.
	if tableCount <= 1 || splitColumnCount <= 1 {
		return graph, nil
	}

	rows, err = db.QueryContext(ctx, "SELECT id, status FROM wisps")
	if err != nil {
		return graph, fmt.Errorf("load migration 0059 wisps: %w", err)
	}
	for rows.Next() {
		var id string
		var status sql.NullString
		if err := rows.Scan(&id, &status); err != nil {
			if closeErr := rows.Close(); closeErr != nil {
				return graph, fmt.Errorf("scan migration 0059 wisp: %w (close wisp rows: %v)", err, closeErr)
			}
			return graph, fmt.Errorf("scan migration 0059 wisp: %w", err)
		}
		graph.nodes[migration0059Node{kind: migration0059WispKind, id: id}] = migration0059NodeState{status: status}
	}
	iterationErr = rows.Err()
	closeErr = rows.Close()
	if iterationErr != nil {
		if closeErr != nil {
			return graph, fmt.Errorf("iterate migration 0059 wisps: %w (close wisp rows: %v)", iterationErr, closeErr)
		}
		return graph, fmt.Errorf("iterate migration 0059 wisps: %w", iterationErr)
	}
	if closeErr != nil {
		return graph, fmt.Errorf("close migration 0059 wisp rows: %w", closeErr)
	}

	deps, err = loadMigration0059Dependencies(ctx, db, "wisp_dependencies", migration0059WispKind)
	if err != nil {
		return graph, err
	}
	graph.dependencies = append(graph.dependencies, deps...)
	return graph, nil
}

// loadMigration0059Dependencies accepts only the two hard-coded call sites
// below; keeping the choice explicit prevents a future caller from turning the
// table interpolation into an injection surface.
func loadMigration0059Dependencies(ctx context.Context, db DBConn, table, sourceKind string) ([]migration0059Dependency, error) {
	if (table != "dependencies" || sourceKind != migration0059IssueKind) &&
		(table != "wisp_dependencies" || sourceKind != migration0059WispKind) {
		return nil, fmt.Errorf("refusing migration 0059 dependency source %q/%q", table, sourceKind)
	}
	//nolint:gosec // table is validated against the fixed allowlist above.
	rows, err := db.QueryContext(ctx, "SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, type, metadata FROM "+table)
	if err != nil {
		return nil, fmt.Errorf("load migration 0059 %s: %w", table, err)
	}
	defer rows.Close()

	var dependencies []migration0059Dependency
	for rows.Next() {
		var issueID, issueTarget, wispTarget, typeName, metadata sql.NullString
		if err := rows.Scan(&issueID, &issueTarget, &wispTarget, &typeName, &metadata); err != nil {
			return nil, fmt.Errorf("scan migration 0059 %s: %w", table, err)
		}
		if !issueID.Valid || !typeName.Valid {
			continue
		}
		target := migration0059Node{}
		switch {
		case issueTarget.Valid:
			target = migration0059Node{kind: migration0059IssueKind, id: issueTarget.String}
		case wispTarget.Valid:
			target = migration0059Node{kind: migration0059WispKind, id: wispTarget.String}
		default:
			continue
		}
		dependencies = append(dependencies, migration0059Dependency{
			source:   migration0059Node{kind: sourceKind, id: issueID.String},
			target:   target,
			typeName: typeName.String,
			gate:     migration0059Gate(metadata),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration 0059 %s: %w", table, err)
	}
	return dependencies, nil
}

func migration0059Gate(metadata sql.NullString) string {
	if !metadata.Valid || metadata.String == "" {
		return "all-children"
	}
	var value struct {
		Gate any `json:"gate"`
	}
	if err := json.Unmarshal([]byte(metadata.String), &value); err != nil {
		// JSON columns reject malformed values. Treating an unexpected driver
		// value like SQL's non-matching JSON_UNQUOTE preserves fail-safe default
		// all-children behavior without aborting an otherwise readable graph.
		return "all-children"
	}
	gate, ok := value.Gate.(string)
	if !ok {
		return "all-children"
	}
	return gate
}

func migration0059BlockedClosure(graph migration0059Graph) map[migration0059Node]bool {
	children := make(map[migration0059Node][]migration0059Node)
	for _, dependency := range graph.dependencies {
		if dependency.typeName == "parent-child" {
			children[dependency.target] = append(children[dependency.target], dependency.source)
		}
	}

	blocked := make(map[migration0059Node]bool)
	queue := make([]migration0059Node, 0)
	for _, dependency := range graph.dependencies {
		state, exists := graph.nodes[dependency.source]
		if !exists || !migration0059NodeOpen(state.status) || blocked[dependency.source] {
			continue
		}
		isDirect := false
		switch dependency.typeName {
		case "blocks", "conditional-blocks":
			target, exists := graph.nodes[dependency.target]
			isDirect = exists && migration0059NodeOpen(target.status)
		case "waits-for":
			var hasOpen, hasClosed bool
			for _, child := range children[dependency.target] {
				childState, exists := graph.nodes[child]
				if !exists {
					continue
				}
				hasOpen = hasOpen || migration0059NodeOpen(childState.status)
				hasClosed = hasClosed || migration0059NodeClosed(childState.status)
			}
			isDirect = hasOpen && !(dependency.gate == "any-children" && hasClosed)
		}
		if isDirect {
			blocked[dependency.source] = true
			queue = append(queue, dependency.source)
		}
	}

	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			state, exists := graph.nodes[child]
			if !exists || !migration0059NodeOpen(state.status) || blocked[child] {
				continue
			}
			blocked[child] = true
			queue = append(queue, child)
		}
	}
	return blocked
}

func migration0059NodeOpen(status sql.NullString) bool {
	return status.Valid && status.String != "closed" && status.String != "pinned"
}

func migration0059NodeClosed(status sql.NullString) bool {
	return status.Valid && status.String == "closed"
}

var migration0059FaultHook func(stage string) error

func runMigration0059FaultHook(stage string) error {
	if migration0059FaultHook == nil {
		return nil
	}
	return migration0059FaultHook(stage)
}

// SetMigration0059FaultHookForTest installs a fault at an internal execution
// stage and returns a restore function. Production leaves the hook nil.
func SetMigration0059FaultHookForTest(fn func(stage string) error) func() {
	previous := migration0059FaultHook
	migration0059FaultHook = fn
	return func() { migration0059FaultHook = previous }
}

func execMigration(ctx context.Context, db DBConn, src migrationSource, mf migrationFile, original []byte) error {
	if src.cursorTable == mainSource.cursorTable && src.dir == mainSource.dir && mf.version == 59 {
		if err := validateMigration0059Execution(src, mf, original); err != nil {
			return fmt.Errorf("validate migration 0059 execution: %w", err)
		}
		return execMigration0059(ctx, db)
	}
	return execMigrationBody(ctx, db, string(original))
}
