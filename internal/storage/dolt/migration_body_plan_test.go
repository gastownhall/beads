package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// Server-lane coverage for the execution plans of main 0059 and ignored/0015
// (gastownhall/beads#6520, internal/storage/schema/migration_body_plans.go).
// The frozen recompute in both files probes the wisp dependency graph with
// correlated EXISTS subqueries; on a production-shaped clone with ~14k wisp
// dependencies 0059 did not finish in two hours. The graph here is 1/8 of
// that wisp tier and keeps the reported durable tier.

const (
	main59File    = "0059_recompute_null_gate_is_blocked.up.sql"
	ignored15File = "0015_recompute_null_gate_wisp_is_blocked.up.sql"
	// The frozen files' SHA-256, pinned independently of the plan registry.
	main59Hash    = "f0186029a1c87d579b67e0540ec22ad0bd0fc59793b07f7d24a24e122b77c394"
	ignored15Hash = "e761d5c3ceb9610edbb2e1d153e82d235cc7d7f0b87f28a45bec43835e6a4e19"
)

// planEntity is one issue or wisp of a blocked-state fixture graph.
type planEntity struct {
	id      string
	status  string
	updated time.Time
}

// planEdge is one dependency row. wisp says which table holds it
// (wisp_dependencies, else dependencies); exactly one of onIssue/onWisp is
// set; metadata is the raw JSON, or "" for SQL NULL.
type planEdge struct {
	wisp     bool
	src      string
	onIssue  string
	onWisp   string
	typ      string
	metadata string
}

type planGraph struct {
	name   string
	issues []planEntity
	wisps  []planEntity
	edges  []planEdge
}

func (g *planGraph) count(wisp bool) int {
	n := 0
	for _, e := range g.edges {
		if e.wisp == wisp {
			n++
		}
	}
	return n
}

var planStatuses = []string{
	"open", "open", "open", "open", "open", "open", "open", "open", "open", "open", "open",
	"in_progress", "in_progress", "blocked", "deferred", "hooked",
	"closed", "closed", "closed", "closed", "pinned",
}

var planGates = []string{"", "{}", `{"gate":"any-children"}`, `{"gate":"all-children"}`, `{"gate":"any-children"}`}

// genPlanGraph builds a deterministic mixed graph: parent-child trees within
// and across both entity kinds (so recursion propagates issue->wisp and
// wisp->issue), blocks/conditional-blocks to open and closed targets of both
// kinds, waits-for rows with every gate shape including NULL metadata, a few
// dangling targets, and non-blocking noise.
//
// The per-kind counts are exact: issueDeps rows in dependencies and wispDeps
// rows in wisp_dependencies.
func genPlanGraph(name string, seed int64, nIssues, issueDeps, nWisps, wispDeps int) *planGraph {
	rng := rand.New(rand.NewSource(seed))
	g := &planGraph{name: name}
	base := time.Date(2025, time.February, 3, 4, 5, 6, 0, time.UTC)
	for i := 0; i < nIssues; i++ {
		g.issues = append(g.issues, planEntity{
			id:      fmt.Sprintf("%s-i%04d", name, i),
			status:  planStatuses[rng.Intn(len(planStatuses))],
			updated: base.Add(time.Duration(i) * time.Minute),
		})
	}
	for i := 0; i < nWisps; i++ {
		g.wisps = append(g.wisps, planEntity{
			id:      fmt.Sprintf("%s-w%05d", name, i),
			status:  planStatuses[rng.Intn(len(planStatuses))],
			updated: base.Add(time.Duration(i) * time.Second).Add(72 * time.Hour),
		})
	}

	type key struct {
		wisp            bool
		src, on, onKind string
	}
	seen := map[key]bool{}
	add := func(wisp bool, src, onKind, on, typ, metadata string) bool {
		k := key{wisp, src, on, onKind}
		if src == on || seen[k] {
			return false
		}
		seen[k] = true
		e := planEdge{wisp: wisp, src: src, typ: typ, metadata: metadata}
		if onKind == "issue" {
			e.onIssue = on
		} else {
			e.onWisp = on
		}
		g.edges = append(g.edges, e)
		return true
	}
	pickIssue := func() string { return g.issues[rng.Intn(len(g.issues))].id }
	pickWisp := func() string { return g.wisps[rng.Intn(len(g.wisps))].id }
	// target picks a target of kind, occasionally one that does not exist.
	target := func(kind string) string {
		if rng.Intn(40) == 0 {
			return fmt.Sprintf("%s-missing-%d", name, rng.Intn(5))
		}
		if kind == "issue" {
			return pickIssue()
		}
		return pickWisp()
	}
	gate := func() string { return planGates[rng.Intn(len(planGates))] }

	fill := func(wisp bool, want int, srcPick func() string) {
		have := 0
		// Parent-child first: wisp i (or issue i) gets a parent among the
		// earlier entities, so the trees have real depth.
		entities := g.issues
		if wisp {
			entities = g.wisps
		}
		pc := want * 3 / 5
		for i := 1; i < len(entities) && have < pc; i++ {
			onKind := "wisp"
			if wisp == (rng.Intn(10) == 0) {
				onKind = "issue"
			}
			var on string
			if onKind == "wisp" {
				if wisp {
					on = g.wisps[rng.Intn(i)].id
				} else {
					on = pickWisp()
				}
			} else {
				if wisp {
					on = pickIssue()
				} else {
					on = g.issues[rng.Intn(i)].id
				}
			}
			if add(wisp, entities[i].id, onKind, on, "parent-child", "{}") {
				have++
			}
		}
		for have < want {
			src := srcPick()
			onKind := "issue"
			if rng.Intn(2) == 0 {
				onKind = "wisp"
			}
			var ok bool
			switch r := rng.Intn(20); {
			case r < 7:
				ok = add(wisp, src, onKind, target(onKind), "blocks", "{}")
			case r < 9:
				ok = add(wisp, src, onKind, target(onKind), "conditional-blocks", "{}")
			case r < 17:
				ok = add(wisp, src, onKind, target(onKind), "waits-for", gate())
			case r < 18:
				ok = add(wisp, src, onKind, target(onKind), "parent-child", "{}")
			default:
				ok = add(wisp, src, onKind, target(onKind), "related", "{}")
			}
			if ok {
				have++
			}
		}
	}
	fill(false, issueDeps, pickIssue)
	fill(true, wispDeps, pickWisp)
	return g
}

// scaledPlanGraph is the AC-1 graph: #6520's durable tier (285 issues, 113
// dependencies) and 1/8 of its wisp tier (11,654 wisps, 14,146 wisp
// dependencies).
func scaledPlanGraph() *planGraph {
	return genPlanGraph("p6520", 6520, 285, 113, 1457, 1770)
}

// blockedModel evaluates the frozen 0059/ignored-0015 predicate directly over
// g and returns the ids it marks blocked, per kind.
func blockedModel(g *planGraph) (issues, wisps map[string]bool) {
	status := map[[2]string]string{}
	for _, e := range g.issues {
		status[[2]string{"issue", e.id}] = e.status
	}
	for _, e := range g.wisps {
		status[[2]string{"wisp", e.id}] = e.status
	}
	exists := func(kind, id string) bool { _, ok := status[[2]string{kind, id}]; return ok }
	open := func(kind, id string) bool {
		s, ok := status[[2]string{kind, id}]
		return ok && s != "closed" && s != "pinned"
	}
	kindOf := func(wisp bool) string {
		if wisp {
			return "wisp"
		}
		return "issue"
	}
	targetOf := func(e planEdge) [2]string {
		if e.onIssue != "" {
			return [2]string{"issue", e.onIssue}
		}
		return [2]string{"wisp", e.onWisp}
	}
	// child counts per parent target, from both tables.
	openChild, closedChild := map[[2]string]bool{}, map[[2]string]bool{}
	for _, e := range g.edges {
		if e.typ != "parent-child" || !exists(kindOf(e.wisp), e.src) {
			continue
		}
		t := targetOf(e)
		if open(kindOf(e.wisp), e.src) {
			openChild[t] = true
		}
		if status[[2]string{kindOf(e.wisp), e.src}] == "closed" {
			closedChild[t] = true
		}
	}
	reach := map[[2]string]bool{}
	for _, e := range g.edges {
		src := [2]string{kindOf(e.wisp), e.src}
		if !open(src[0], src[1]) {
			continue
		}
		t := targetOf(e)
		switch e.typ {
		case "blocks", "conditional-blocks":
			if open(t[0], t[1]) {
				reach[src] = true
			}
		case "waits-for":
			gate := "all-children"
			if strings.Contains(e.metadata, `"gate":"any-children"`) {
				gate = "any-children"
			}
			if openChild[t] && !(gate == "any-children" && closedChild[t]) {
				reach[src] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, e := range g.edges {
			src := [2]string{kindOf(e.wisp), e.src}
			if e.typ != "parent-child" || reach[src] || !open(src[0], src[1]) || !reach[targetOf(e)] {
				continue
			}
			reach[src] = true
			changed = true
		}
	}
	issues, wisps = map[string]bool{}, map[string]bool{}
	for k := range reach {
		if k[0] == "issue" {
			issues[k[1]] = true
		} else {
			wisps[k[1]] = true
		}
	}
	return issues, wisps
}

// planTestDB is one isolated database on the shared test server, with a
// pinned migration connection and a separate control connection.
type planTestDB struct {
	t     *testing.T
	ctx   context.Context
	name  string
	conn  *sql.Conn
	admin *sql.DB
}

func newPlanTestDB(t *testing.T, ctx context.Context) *planTestDB {
	t.Helper()
	dbName := uniqueTestDBName(t)
	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	// No DROP DATABASE on cleanup, as in cross_upgrade_merge_test.go: rapid
	// CREATE/DROP cycles crash the shared Dolt server.
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	db, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName, Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &planTestDB{t: t, ctx: ctx, name: dbName, conn: conn, admin: admin}
}

func (d *planTestDB) exec(query string, args ...any) {
	d.t.Helper()
	if _, err := d.conn.ExecContext(d.ctx, query, args...); err != nil {
		d.t.Fatalf("exec %.200q: %v", query, err)
	}
}

// migrateToV58I14 brings an empty database to main v58 / ignored v14, the
// state #6520's clone was in before 0059 and ignored/0015.
func (d *planTestDB) migrateToV58I14() {
	d.t.Helper()
	if _, err := schema.MigrateUpTo(d.ctx, d.conn, 58); err != nil {
		d.t.Fatalf("migrate main to 58: %v", err)
	}
	if _, err := schema.MigrateIgnoredUpTo(d.ctx, d.conn, 14); err != nil {
		d.t.Fatalf("migrate ignored to 14: %v", err)
	}
}

// load inserts g with is_blocked scrambled (so a recompute that changes
// nothing is caught) and g's fixed updated_at stamps.
func (d *planTestDB) load(g *planGraph) {
	d.t.Helper()
	insertEntities := func(table string, rows []planEntity) {
		for start := 0; start < len(rows); start += 200 {
			end := min(start+200, len(rows))
			var vals []string
			var args []any
			for i, e := range rows[start:end] {
				var closedAt any
				if e.status == "closed" {
					closedAt = e.updated
				}
				vals = append(vals, "(?, ?, '', '', '', '', ?, 2, 'task', ?, ?, ?, ?)")
				args = append(args, e.id, "fixture "+e.id, e.status, e.updated, e.updated, closedAt, (start+i)%3 == 0)
			}
			d.exec("INSERT INTO "+table+" (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at, closed_at, is_blocked) VALUES "+
				strings.Join(vals, ", "), args...)
		}
	}
	insertEntities("issues", g.issues)
	insertEntities("wisps", g.wisps)
	for _, wisp := range []bool{false, true} {
		table := "dependencies"
		if wisp {
			table = "wisp_dependencies"
		}
		var rows []planEdge
		for _, e := range g.edges {
			if e.wisp == wisp {
				rows = append(rows, e)
			}
		}
		for start := 0; start < len(rows); start += 200 {
			end := min(start+200, len(rows))
			var vals []string
			var args []any
			for _, e := range rows[start:end] {
				var onIssue, onWisp, metadata any
				if e.onIssue != "" {
					onIssue = e.onIssue
				}
				if e.onWisp != "" {
					onWisp = e.onWisp
				}
				if e.metadata != "" {
					metadata = e.metadata
				}
				vals = append(vals, "(UUID(), ?, ?, ?, ?, 'fixture', ?, '2025-02-03 04:05:06')")
				args = append(args, e.src, onIssue, onWisp, e.typ, metadata)
			}
			d.exec("SET FOREIGN_KEY_CHECKS = 0")
			d.exec("INSERT INTO "+table+" (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type, created_by, metadata, created_at) VALUES "+
				strings.Join(vals, ", "), args...)
			d.exec("SET FOREIGN_KEY_CHECKS = 1")
		}
	}
}

// commit records the working set so a later check can compare against HEAD.
func (d *planTestDB) commit(msg string) {
	d.t.Helper()
	d.exec("CALL DOLT_ADD('-A')")
	d.exec("CALL DOLT_COMMIT('-m', ?)", msg)
}

// bounded runs fn on d.conn and returns its elapsed time. Once bound passes,
// a control connection kills whatever query d.conn is running (its id is
// read before fn starts), so a slow body fails the test at the bound instead
// of running for hours.
func (d *planTestDB) bounded(bound time.Duration, fn func() error) (time.Duration, error) {
	d.t.Helper()
	var connID int64
	if err := d.conn.QueryRowContext(d.ctx, "SELECT CONNECTION_ID()").Scan(&connID); err != nil {
		d.t.Fatalf("read connection id: %v", err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-done:
			return
		case <-time.After(bound):
		}
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			_, _ = d.admin.ExecContext(context.Background(), fmt.Sprintf("KILL QUERY %d", connID))
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	start := time.Now()
	err := fn()
	elapsed := time.Since(start)
	close(done)
	wg.Wait()
	return elapsed, err
}

func (d *planTestDB) blockedIDs(table string) map[string]bool {
	d.t.Helper()
	rows, err := d.conn.QueryContext(d.ctx, "SELECT id FROM "+table+" WHERE is_blocked = 1")
	if err != nil {
		d.t.Fatalf("read blocked %s: %v", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			d.t.Fatalf("scan blocked %s: %v", table, err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		d.t.Fatalf("read blocked %s: %v", table, err)
	}
	return out
}

func (d *planTestDB) cursorHash(table string, version int) string {
	d.t.Helper()
	var h sql.NullString
	if err := d.conn.QueryRowContext(d.ctx, "SELECT content_hash FROM "+table+" WHERE version = ?", version).Scan(&h); err != nil {
		d.t.Fatalf("read %s row %d: %v", table, version, err)
	}
	return h.String
}

func requireSameIDs(t *testing.T, what string, got, want map[string]bool) {
	t.Helper()
	var missing, extra []string
	for id := range want {
		if !got[id] {
			missing = append(missing, id)
		}
	}
	for id := range got {
		if !want[id] {
			extra = append(extra, id)
		}
	}
	if len(missing)+len(extra) > 0 {
		slices.Sort(missing)
		slices.Sort(extra)
		t.Errorf("%s differs from the frozen-predicate model: missing %d %v, unexpected %d %v",
			what, len(missing), firstTen(missing), len(extra), firstTen(extra))
	}
}

func firstTen(ids []string) []string {
	if len(ids) > 10 {
		return ids[:10]
	}
	return ids
}

// TestMigrationBodyPlanScaledGraphMain59 is #6520 AC-1: the runtime runner
// applies main 0059 to the 1/8-scale graph within 30 seconds, with the
// frozen predicate's issue blocked state and the frozen file's hash.
func TestMigrationBodyPlanScaledGraphMain59(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)

	g := scaledPlanGraph()
	d := newPlanTestDB(t, ctx)
	d.migrateToV58I14()
	d.load(g)
	wantIssues, wantWisps := blockedModel(g)
	if len(wantIssues) == 0 || len(wantWisps) == 0 {
		t.Fatalf("fixture graph blocks %d issues and %d wisps; both must be non-zero", len(wantIssues), len(wantWisps))
	}

	const bound = 30 * time.Second
	elapsed, err := d.bounded(bound, func() error {
		_, err := schema.MigrateUpTo(ctx, d.conn, 59)
		return err
	})
	t.Logf("main 0059 over %d issues/%d deps, %d wisps/%d wisp deps: %s (%d issues blocked)",
		len(g.issues), g.count(false), len(g.wisps), g.count(true), elapsed.Round(time.Millisecond), len(wantIssues))
	if elapsed > bound {
		t.Fatalf("main 0059 did not finish within %s (stopped at %s; #6520)", bound, elapsed.Round(time.Millisecond))
	}
	if err != nil {
		t.Fatalf("migrate main to 59: %v", err)
	}
	requireSameIDs(t, "blocked issues after main 0059", d.blockedIDs("issues"), wantIssues)
	if got := d.cursorHash("schema_migrations", 59); got != main59Hash {
		t.Errorf("schema_migrations row 59 content_hash = %q, want the frozen file's %q", got, main59Hash)
	}
}

// TestMigrationBodyPlanScaledGraphIgnored15 is #6520 AC-2: the runtime runner
// applies ignored/0015, the wisps twin of 0059 over the real wisp tables, to
// the same graph within 30 seconds with the frozen predicate's wisp blocked
// state and the frozen file's hash.
func TestMigrationBodyPlanScaledGraphIgnored15(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)

	g := scaledPlanGraph()
	d := newPlanTestDB(t, ctx)
	d.migrateToV58I14()
	d.load(g)
	_, wantWisps := blockedModel(g)

	const bound = 30 * time.Second
	elapsed, err := d.bounded(bound, func() error {
		_, err := schema.MigrateIgnoredUpTo(ctx, d.conn, 15)
		return err
	})
	t.Logf("ignored 0015 over %d issues/%d deps, %d wisps/%d wisp deps: %s (%d wisps blocked)",
		len(g.issues), g.count(false), len(g.wisps), g.count(true), elapsed.Round(time.Millisecond), len(wantWisps))
	if elapsed > bound {
		t.Fatalf("ignored 0015 did not finish within %s (stopped at %s; #6520)", bound, elapsed.Round(time.Millisecond))
	}
	if err != nil {
		t.Fatalf("migrate ignored to 15: %v", err)
	}
	requireSameIDs(t, "blocked wisps after ignored 0015", d.blockedIDs("wisps"), wantWisps)
	if got := d.cursorHash("ignored_schema_migrations", 15); got != ignored15Hash {
		t.Errorf("ignored_schema_migrations row 15 content_hash = %q, want the frozen file's %q", got, ignored15Hash)
	}
}

// snapshotRows renders every row of table, ordered by id, column by column.
func (d *planTestDB) snapshotRows(table string) []string {
	d.t.Helper()
	rows, err := d.conn.QueryContext(d.ctx, "SELECT * FROM "+table+" ORDER BY id")
	if err != nil {
		d.t.Fatalf("snapshot %s: %v", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		d.t.Fatalf("snapshot %s columns: %v", table, err)
	}
	var out []string
	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			d.t.Fatalf("snapshot %s scan: %v", table, err)
		}
		var b strings.Builder
		for i, v := range vals {
			if v == nil {
				fmt.Fprintf(&b, "%s=NULL ", cols[i])
			} else {
				fmt.Fprintf(&b, "%s=%q ", cols[i], v)
			}
		}
		out = append(out, b.String())
	}
	if err := rows.Err(); err != nil {
		d.t.Fatalf("snapshot %s: %v", table, err)
	}
	return out
}

func (d *planTestDB) standIns() []string {
	d.t.Helper()
	rows, err := d.conn.QueryContext(d.ctx,
		"SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME LIKE '\\_\\_bd\\_0059%' ORDER BY TABLE_NAME")
	if err != nil {
		d.t.Fatalf("list stand-ins: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			d.t.Fatalf("scan stand-in: %v", err)
		}
		out = append(out, name)
	}
	return out
}

func (d *planTestDB) clearGraph() {
	d.t.Helper()
	for _, table := range []string{"wisp_dependencies", "dependencies", "wisps", "issues"} {
		d.exec("DELETE FROM " + table)
	}
}

// explicitPlanGraph builds a hand-written planGraph. Ids are given without
// the graph-name prefix; entities get fixed old timestamps in list order.
func explicitPlanGraph(name string, issues, wisps [][2]string, edges []planEdge) *planGraph {
	g := &planGraph{name: name}
	base := time.Date(2024, time.June, 1, 12, 0, 0, 0, time.UTC)
	for i, e := range issues {
		g.issues = append(g.issues, planEntity{id: name + "-" + e[0], status: e[1], updated: base.Add(time.Duration(i) * time.Minute)})
	}
	for i, e := range wisps {
		g.wisps = append(g.wisps, planEntity{id: name + "-" + e[0], status: e[1], updated: base.Add(time.Duration(i) * time.Minute).Add(time.Hour)})
	}
	for _, e := range edges {
		e.src = name + "-" + e.src
		if e.onIssue != "" {
			e.onIssue = name + "-" + e.onIssue
		}
		if e.onWisp != "" {
			e.onWisp = name + "-" + e.onWisp
		}
		g.edges = append(g.edges, e)
	}
	return g
}

const anyChildren = `{"gate":"any-children"}`

// gatePlanGraph exercises every clause of the frozen predicate by hand:
// open/closed/pinned children in both kinds, waits-for with NULL metadata
// and any-children against issue, wisp, closed and childless targets,
// blocks against open, closed and pinned targets, a conditional-block, and
// a cross-kind parent-child chain.
func gatePlanGraph() *planGraph {
	return explicitPlanGraph("gates",
		[][2]string{
			{"i000", "open"}, {"i001", "open"}, {"i002", "closed"}, {"i003", "open"},
			{"i004", "open"}, {"i005", "open"}, {"i006", "open"}, {"i007", "open"},
			{"i008", "open"}, {"i009", "open"}, {"i010", "open"}, {"i011", "open"},
			{"i012", "closed"}, {"i013", "pinned"}, {"i014", "open"},
		},
		[][2]string{
			{"w000", "open"}, {"w001", "open"}, {"w002", "closed"}, {"w003", "open"},
			{"w004", "open"}, {"w005", "pinned"},
		},
		[]planEdge{
			// i000 has open child i001 and closed child i002.
			{src: "i001", onIssue: "i000", typ: "parent-child", metadata: "{}"},
			{src: "i002", onIssue: "i000", typ: "parent-child", metadata: "{}"},
			// waits-for on the mixed hub under each gate, on a closed target
			// and on a childless one.
			{src: "i003", onIssue: "i000", typ: "waits-for"},
			{src: "i004", onIssue: "i000", typ: "waits-for", metadata: anyChildren},
			{src: "i005", onIssue: "i002", typ: "waits-for", metadata: anyChildren},
			{src: "i006", onIssue: "i012", typ: "waits-for"},
			// blocks: an open target blocks, closed and pinned ones do not.
			{src: "i007", onIssue: "i000", typ: "blocks", metadata: "{}"},
			{src: "i008", onIssue: "i012", typ: "blocks", metadata: "{}"},
			{src: "i009", onIssue: "i013", typ: "blocks", metadata: "{}"},
			{src: "i010", onIssue: "i000", typ: "conditional-blocks", metadata: "{}"},
			// a waiter on an issue that is itself only transitively blocked.
			{src: "i011", onIssue: "i003", typ: "waits-for"},
			// wisp parent-child into an open issue, and into w000, which
			// collects open, closed and pinned wisp children.
			{wisp: true, src: "w001", onIssue: "i014", typ: "parent-child", metadata: "{}"},
			{wisp: true, src: "w002", onWisp: "w000", typ: "parent-child", metadata: "{}"},
			{wisp: true, src: "w003", onWisp: "w000", typ: "parent-child", metadata: "{}"},
			{wisp: true, src: "w005", onWisp: "w000", typ: "parent-child", metadata: "{}"},
			// waits-for onto the mixed wisp hub under each gate, and onto a
			// childless wisp.
			{wisp: true, src: "w001", onWisp: "w000", typ: "waits-for", metadata: anyChildren},
			{wisp: true, src: "w004", onWisp: "w000", typ: "waits-for"},
			{wisp: true, src: "w003", onWisp: "w004", typ: "waits-for"},
			// blocks across kinds: an open issue target blocks, a pinned wisp
			// target does not.
			{wisp: true, src: "w004", onIssue: "i014", typ: "blocks", metadata: "{}"},
			{wisp: true, src: "w001", onWisp: "w005", typ: "blocks", metadata: "{}"},
			// a pinned wisp child of the waiting issue i003.
			{wisp: true, src: "w005", onIssue: "i003", typ: "parent-child", metadata: "{}"},
		})
}

// cyclePlanGraph is a mixed-kind parent-child cycle (issue i015 is a child
// of wisp w015, which is a child of i015) with a blocking seed on i015, so
// the recursive UNION DISTINCT must reach a fixed point across both kinds.
func cyclePlanGraph() *planGraph {
	return explicitPlanGraph("cycle",
		[][2]string{{"i001", "open"}, {"i015", "open"}},
		[][2]string{{"w015", "open"}},
		[]planEdge{
			{src: "i015", onWisp: "w015", typ: "parent-child", metadata: "{}"},
			{src: "i015", onIssue: "i001", typ: "blocks", metadata: "{}"},
			{wisp: true, src: "w015", onIssue: "i015", typ: "parent-child", metadata: "{}"},
		})
}

// TestMigrationBodyPlanMatchesFrozenBody is #6520 AC-3: on small mixed graphs
// (both target kinds, waits-for with every gate shape, NULL metadata,
// dangling targets, cross-kind parent-child trees and a cross-kind cycle)
// with scrambled is_blocked and fixed old timestamps, the frozen and planned
// body of main 0059, and separately of ignored/0015, each run alone from the
// same reset graph, leave identical issues and wisps rows, and the planned
// 0059 leaves no stand-in behind.
func TestMigrationBodyPlanMatchesFrozenBody(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)

	frozen59, err := schema.MigrationSQL(main59File)
	if err != nil {
		t.Fatal(err)
	}
	frozen15, err := schema.IgnoredMigrationSQL(ignored15File)
	if err != nil {
		t.Fatal(err)
	}
	planned59, err := schema.PlannedMigrationSQL(main59File)
	if err != nil {
		t.Fatal(err)
	}
	planned15, err := schema.PlannedIgnoredMigrationSQL(ignored15File)
	if err != nil {
		t.Fatal(err)
	}
	if planned59 == frozen59 || planned15 == frozen15 {
		t.Fatal("no execution plan is registered for main 0059 or ignored/0015; nothing to compare")
	}

	d := newPlanTestDB(t, ctx)
	d.migrateToV58I14()

	// run resets the graph and executes one body alone, so a body that
	// touched the other entity table shows up in the comparison.
	run := func(g *planGraph, body string) (issues, wisps []string) {
		d.clearGraph()
		d.load(g)
		d.exec(body)
		if left := d.standIns(); len(left) > 0 {
			t.Fatalf("%s: stand-ins left behind: %v", g.name, left)
		}
		return d.snapshotRows("issues"), d.snapshotRows("wisps")
	}

	graphs := []*planGraph{gatePlanGraph(), cyclePlanGraph()}
	for seed := int64(1); seed <= 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		nIssues, nWisps := 4+rng.Intn(12), 4+rng.Intn(12)
		graphs = append(graphs, genPlanGraph(fmt.Sprintf("diff%02d", seed), seed, nIssues, nIssues+rng.Intn(2*nIssues), nWisps, nWisps+rng.Intn(2*nWisps)))
	}

	blockedSeen := 0
	for _, g := range graphs {
		wantIssues, wantWisps := blockedModel(g)
		blockedSeen += len(wantIssues) + len(wantWisps)
		for _, step := range []struct {
			name, frozen, planned, table string
			want                         map[string]bool
		}{
			{"main 0059", frozen59, planned59, "issues", wantIssues},
			{"ignored 0015", frozen15, planned15, "wisps", wantWisps},
		} {
			frozenIssues, frozenWisps := run(g, step.frozen)
			plannedIssues, plannedWisps := run(g, step.planned)
			if !slices.Equal(frozenIssues, plannedIssues) {
				t.Errorf("%s %s: issues differ between frozen and planned bodies:\nfrozen:  %v\nplanned: %v", g.name, step.name, frozenIssues, plannedIssues)
			}
			if !slices.Equal(frozenWisps, plannedWisps) {
				t.Errorf("%s %s: wisps differ between frozen and planned bodies:\nfrozen:  %v\nplanned: %v", g.name, step.name, frozenWisps, plannedWisps)
			}
			requireSameIDs(t, g.name+" blocked "+step.table+" after "+step.name, d.blockedIDs(step.table), step.want)
		}
	}
	if blockedSeen == 0 {
		t.Fatalf("no graph blocked anything; the differential compared only no-ops")
	}
}

// indexShape lists every index column in the database, stand-ins included,
// with its uniqueness.
func (d *planTestDB) indexShape() []string {
	d.t.Helper()
	rows, err := d.conn.QueryContext(d.ctx,
		"SELECT TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX, COLUMN_NAME, NON_UNIQUE FROM INFORMATION_SCHEMA.STATISTICS "+
			"WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX")
	if err != nil {
		d.t.Fatalf("read index shape: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var table, index, column string
		var seq, nonUnique int
		if err := rows.Scan(&table, &index, &seq, &column, &nonUnique); err != nil {
			d.t.Fatalf("scan index shape: %v", err)
		}
		out = append(out, fmt.Sprintf("%s.%s[%d]=%s non_unique=%d", table, index, seq, column, nonUnique))
	}
	return out
}

// TestMigrationBodyPlanReplaysInterruptedPrefix is #6520 AC-6: a 0059 step
// interrupted after part of its old (frozen) or planned body ran, with no
// version-59 cursor row, replays through the bounded runner to the same
// rows as an uninterrupted step, records the frozen hash, leaves no
// stand-in, and leaves every real table's indexes as they were.
func TestMigrationBodyPlanReplaysInterruptedPrefix(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)

	frozen, err := schema.MigrationSQL(main59File)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := schema.PlannedMigrationSQL(main59File)
	if err != nil {
		t.Fatal(err)
	}
	// A prefix ends just before marker, or with throughLine just after the
	// line that contains it; planIndexes is how many plan indexes it leaves
	// on the stand-in.
	prefixes := []struct {
		name, body, marker string
		throughLine        bool
		planIndexes        int
	}{
		{"frozen through stand-in creation", frozen, "SET @has_wisps", false, 0},
		{"frozen through stand-in copies", frozen, "-- Self-assign updated_at", false, 0},
		{"frozen through zeroing update", frozen, "WITH RECURSIVE", false, 0},
		{"planned through first stand-in index", planned, "CREATE INDEX idx_bd0059_deps_issue_type", true, 1},
		{"planned through all stand-in indexes", planned, "CREATE INDEX idx_bd0059_deps_type_wisp", true, 3},
	}
	const bound = 30 * time.Second

	g := scaledPlanGraph()
	wantIssues, _ := blockedModel(g)

	ref := newPlanTestDB(t, ctx)
	ref.migrateToV58I14()
	ref.load(g)
	elapsed, err := ref.bounded(bound, func() error {
		_, err := schema.MigrateUpTo(ctx, ref.conn, 59)
		return err
	})
	if elapsed > bound {
		t.Fatalf("uninterrupted main 0059 did not finish within %s (stopped at %s; #6520)", bound, elapsed.Round(time.Millisecond))
	}
	if err != nil {
		t.Fatalf("uninterrupted migrate to 59 (%s): %v", elapsed.Round(time.Millisecond), err)
	}
	wantRows := ref.snapshotRows("issues")

	for _, p := range prefixes {
		t.Run(p.name, func(t *testing.T) {
			d := newPlanTestDB(t, ctx)
			d.migrateToV58I14()
			d.load(g)
			indexesBefore := d.indexShape()

			cut := strings.Index(p.body, p.marker)
			if cut < 0 {
				t.Fatalf("0059 body has no %q", p.marker)
			}
			if p.throughLine {
				cut += strings.Index(p.body[cut:], "\n") + 1
			}
			d.exec(p.body[:cut])
			if len(d.standIns()) == 0 {
				t.Fatalf("prefix left no stand-in; it does not model an interrupted 0059")
			}
			if got := schemaVersion(t, d); got != 58 {
				t.Fatalf("schema cursor after the prefix = %d, want 58", got)
			}
			var planIndexes int
			if err := d.conn.QueryRowContext(ctx,
				"SELECT COUNT(DISTINCT INDEX_NAME) FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() "+
					"AND TABLE_NAME = '__bd_0059_recompute_wisp_deps' AND INDEX_NAME LIKE 'idx\\_bd0059%'").Scan(&planIndexes); err != nil {
				t.Fatalf("read plan indexes: %v", err)
			}
			if planIndexes != p.planIndexes {
				t.Fatalf("prefix left %d plan indexes on the stand-in, want %d", planIndexes, p.planIndexes)
			}

			elapsed, err := d.bounded(bound, func() error {
				_, err := schema.MigrateUpTo(ctx, d.conn, 59)
				return err
			})
			t.Logf("0059 replay after %q: %s", p.name, elapsed.Round(time.Millisecond))
			if elapsed > bound {
				t.Fatalf("0059 replay did not finish within %s (stopped at %s; #6520)", bound, elapsed.Round(time.Millisecond))
			}
			if err != nil {
				t.Fatalf("replay migrate to 59 (%s): %v", elapsed.Round(time.Millisecond), err)
			}
			if got := d.snapshotRows("issues"); !slices.Equal(got, wantRows) {
				t.Errorf("issues after replay differ from the uninterrupted run")
			}
			requireSameIDs(t, "blocked issues after replay", d.blockedIDs("issues"), wantIssues)
			if got := d.cursorHash("schema_migrations", 59); got != main59Hash {
				t.Errorf("schema_migrations row 59 content_hash = %q, want the frozen file's %q", got, main59Hash)
			}
			if left := d.standIns(); len(left) > 0 {
				t.Errorf("stand-ins left after replay: %v", left)
			}
			if got := d.indexShape(); !slices.Equal(got, indexesBefore) {
				t.Errorf("indexes changed across the interrupted step and its replay:\nbefore: %v\nafter:  %v", indexesBefore, got)
			}
		})
	}
}

func schemaVersion(t *testing.T, d *planTestDB) int {
	t.Helper()
	var v int
	if err := d.conn.QueryRowContext(d.ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v); err != nil {
		t.Fatalf("read schema cursor: %v", err)
	}
	return v
}

// updatedStamps reads every row's updated_at as a UTC second-precision string.
func (d *planTestDB) updatedStamps(table string) map[string]string {
	d.t.Helper()
	rows, err := d.conn.QueryContext(d.ctx, "SELECT id, DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s') FROM "+table)
	if err != nil {
		d.t.Fatalf("read %s updated_at: %v", table, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, stamp string
		if err := rows.Scan(&id, &stamp); err != nil {
			d.t.Fatalf("scan %s updated_at: %v", table, err)
		}
		out[id] = stamp
	}
	if err := rows.Err(); err != nil {
		d.t.Fatalf("read %s updated_at: %v", table, err)
	}
	return out
}

// TestMigrationBodyPlanFullChainFromV58 is #6520 AC-7: from a clean,
// committed main-v58/ignored-v14 database holding the AC-1 graph, the
// designated migrator (MigrateUpWithLock on a pinned session) brings both
// cursors to this binary's latest versions within 120 seconds, keeps the
// frozen hashes on rows 59 and ignored 15, and leaves both entity kinds with
// the frozen predicate's blocked state and their fixture updated_at stamps.
func TestMigrationBodyPlanFullChainFromV58(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)

	g := scaledPlanGraph()
	d := newPlanTestDB(t, ctx)
	d.migrateToV58I14()
	d.load(g)
	d.commit("fixture: #6520 graph at main v58 / ignored v14")
	wantIssues, wantWisps := blockedModel(g)

	const bound = 120 * time.Second
	elapsed, err := d.bounded(bound, func() error {
		_, err := schema.MigrateUpWithLock(ctx, d.conn, d.name)
		return err
	})
	t.Logf("full migration pass from main v58 / ignored v14: %s", elapsed.Round(time.Millisecond))
	if elapsed > bound {
		t.Fatalf("full migration pass did not finish within %s (stopped at %s; #6520)", bound, elapsed.Round(time.Millisecond))
	}
	if err != nil {
		t.Fatalf("full migration pass: %v", err)
	}

	if got, err := schema.CurrentVersion(ctx, d.conn); err != nil || got != schema.LatestVersion() {
		t.Errorf("main cursor = %d (%v), want %d", got, err, schema.LatestVersion())
	}
	if got, err := schema.CurrentIgnoredVersion(ctx, d.conn); err != nil || got != schema.LatestIgnoredVersion() {
		t.Errorf("ignored cursor = %d (%v), want %d", got, err, schema.LatestIgnoredVersion())
	}
	if got := d.cursorHash("schema_migrations", 59); got != main59Hash {
		t.Errorf("schema_migrations row 59 content_hash = %q, want the frozen file's %q", got, main59Hash)
	}
	if got := d.cursorHash("ignored_schema_migrations", 15); got != ignored15Hash {
		t.Errorf("ignored_schema_migrations row 15 content_hash = %q, want the frozen file's %q", got, ignored15Hash)
	}
	requireSameIDs(t, "blocked issues after the full pass", d.blockedIDs("issues"), wantIssues)
	requireSameIDs(t, "blocked wisps after the full pass", d.blockedIDs("wisps"), wantWisps)

	for _, kind := range []struct {
		table string
		rows  []planEntity
	}{{"issues", g.issues}, {"wisps", g.wisps}} {
		stamps := d.updatedStamps(kind.table)
		if len(stamps) != len(kind.rows) {
			t.Errorf("%s has %d rows after the full pass, want %d", kind.table, len(stamps), len(kind.rows))
		}
		changed := 0
		for _, e := range kind.rows {
			if want := e.updated.UTC().Format(time.DateTime); stamps[e.id] != want {
				if changed < 3 {
					t.Errorf("%s %s updated_at = %q, want the fixture's %q", kind.table, e.id, stamps[e.id], want)
				}
				changed++
			}
		}
		if changed > 0 {
			t.Errorf("%d %s rows lost their fixture updated_at", changed, kind.table)
		}
	}
}
