package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type candidateIndex struct {
	Name  string
	Table string
	SQL   string
	Drop  string
}

type queryCase struct {
	Name  string
	Query string
	Args  []any
}

type result struct {
	Name    string
	Count   int
	Errors  int
	Min     time.Duration
	P50     time.Duration
	P95     time.Duration
	Max     time.Duration
	Elapsed time.Duration
}

var indexes = []candidateIndex{
	{
		Name:  "idx_dependencies_type_issue_target",
		Table: "dependencies",
		SQL:   "CREATE INDEX idx_dependencies_type_issue_target ON dependencies (type, issue_id, depends_on_issue_id)",
		Drop:  "DROP INDEX idx_dependencies_type_issue_target ON dependencies",
	},
	{
		Name:  "idx_issues_ready_assignee",
		Table: "issues",
		SQL:   "CREATE INDEX idx_issues_ready_assignee ON issues (assignee, status, priority, created_at, id)",
		Drop:  "DROP INDEX idx_issues_ready_assignee ON issues",
	},
	{
		Name:  "idx_issues_ready_status",
		Table: "issues",
		SQL:   "CREATE INDEX idx_issues_ready_status ON issues (status, priority, created_at, id)",
		Drop:  "DROP INDEX idx_issues_ready_status ON issues",
	},
	{
		Name:  "idx_issues_defer_until",
		Table: "issues",
		SQL:   "CREATE INDEX idx_issues_defer_until ON issues (defer_until)",
		Drop:  "DROP INDEX idx_issues_defer_until ON issues",
	},
}

var sqlOpen = sql.Open

const dependencyTargetExpr = "COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)"

func main() {
	if err := run(context.Background()); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	return runWithArgs(ctx, os.Args[1:])
}

func runWithArgs(ctx context.Context, args []string) error {
	var dsn string
	var concurrency int
	var iterations int
	var keepIndexes bool
	fs := flag.NewFlagSet("bench-ready-indexes", flag.ContinueOnError)
	fs.StringVar(&dsn, "dsn", "root@tcp(127.0.0.1:33307)/mc?timeout=30s&readTimeout=30s&writeTimeout=30s", "MySQL DSN")
	fs.IntVar(&concurrency, "concurrency", 64, "concurrent query workers")
	fs.IntVar(&iterations, "iterations", 2, "query executions per worker per case")
	fs.BoolVar(&keepIndexes, "keep-indexes", false, "leave candidate indexes installed after the final benchmark state")
	if err := fs.Parse(args); err != nil {
		return err
	}

	db, err := sqlOpen("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(concurrency + 8)
	db.SetMaxIdleConns(concurrency + 8)
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	defer func() {
		if err := cleanupCandidateIndexes(context.Background(), db, keepIndexes); err != nil {
			log.Printf("cleanup candidate indexes: %v", err)
		}
	}()

	queries, err := buildQueries(ctx, db)
	if err != nil {
		return err
	}

	states := [][]candidateIndex{nil}
	for _, idx := range indexes {
		states = append(states, []candidateIndex{idx})
	}
	states = append(states, indexes)

	for _, state := range states {
		if err := dropAll(ctx, db); err != nil {
			return err
		}
		stateName := "baseline"
		if len(state) > 0 {
			names := make([]string, 0, len(state))
			for _, idx := range state {
				start := time.Now()
				if _, err := db.ExecContext(ctx, idx.SQL); err != nil {
					return fmt.Errorf("create %s: %w", idx.Name, err)
				}
				fmt.Printf("created %s in %s\n", idx.Name, time.Since(start).Round(time.Millisecond))
				names = append(names, idx.Name)
			}
			stateName = strings.Join(names, "+")
		}

		fmt.Printf("\n## %s\n", stateName)
		for _, qc := range queries {
			r := benchQuery(ctx, db, qc, concurrency, iterations)
			fmt.Printf("%-32s count=%4d errors=%2d min=%7s p50=%7s p95=%7s max=%7s wall=%7s\n",
				r.Name, r.Count, r.Errors, r.Min.Round(time.Millisecond), r.P50.Round(time.Millisecond),
				r.P95.Round(time.Millisecond), r.Max.Round(time.Millisecond), r.Elapsed.Round(time.Millisecond))
		}
	}
	return nil
}

func cleanupCandidateIndexes(ctx context.Context, db *sql.DB, keepIndexes bool) error {
	if keepIndexes {
		return nil
	}
	return dropAll(ctx, db)
}

func dropAll(ctx context.Context, db *sql.DB) error {
	for _, idx := range indexes {
		if _, err := db.ExecContext(ctx, idx.Drop); err != nil && !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "does not exist") && !strings.Contains(err.Error(), "can't drop") {
			return fmt.Errorf("drop %s: %w", idx.Name, err)
		}
	}
	return nil
}

func buildQueries(ctx context.Context, db *sql.DB) ([]queryCase, error) {
	ids, err := candidateIDs(ctx, db)
	if err != nil {
		return nil, err
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return []queryCase{
		{
			Name: "ready_assignee_page",
			Query: `SELECT id FROM issues
WHERE status IN ('open','in_progress')
AND (pinned = 0 OR pinned IS NULL)
AND (ephemeral = 0 OR ephemeral IS NULL)
AND id IN (SELECT id FROM issues WHERE issue_type NOT IN ('merge-request','gate','molecule','message','agent','role','rig'))
AND assignee = 'example-org--control-dispatcher'
AND (defer_until IS NULL OR defer_until <= UTC_TIMESTAMP())
ORDER BY priority ASC, created_at DESC, id ASC
LIMIT 100`,
		},
		{
			Name: "ready_routed_page",
			Query: `SELECT id FROM issues
WHERE status IN ('open','in_progress')
AND (pinned = 0 OR pinned IS NULL)
AND (ephemeral = 0 OR ephemeral IS NULL)
AND id IN (SELECT id FROM issues WHERE issue_type NOT IN ('merge-request','gate','molecule','message','agent','role','rig'))
AND (assignee IS NULL OR assignee = '')
AND (defer_until IS NULL OR defer_until <= UTC_TIMESTAMP())
AND JSON_UNQUOTE(JSON_EXTRACT(metadata, '$."route.routed_to"')) = 'example-org/control-dispatcher'
ORDER BY priority ASC, created_at DESC, id ASC
LIMIT 100`,
		},
		{
			Name: "deferred_parents",
			Query: `SELECT id FROM issues
WHERE defer_until IS NOT NULL AND defer_until > UTC_TIMESTAMP()`,
		},
		{
			Name:  "candidate_blocking_deps",
			Query: fmt.Sprintf(`SELECT issue_id, %s AS target_id, type, metadata FROM dependencies WHERE issue_id IN (%s) AND type IN ('blocks','waits-for','conditional-blocks')`, dependencyTargetExpr, placeholders),
			Args:  args,
		},
	}, nil
}

func candidateIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM issues WHERE status IN ('open','in_progress') ORDER BY priority ASC, created_at DESC, id ASC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no candidate IDs")
	}
	return ids, nil
}

func benchQuery(ctx context.Context, db *sql.DB, qc queryCase, concurrency, iterations int) result {
	start := time.Now()
	var mu sync.Mutex
	var latencies []time.Duration
	errors := 0
	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				qStart := time.Now()
				rows, err := db.QueryContext(ctx, qc.Query, qc.Args...)
				if err == nil {
					for rows.Next() {
					}
					if rows.Err() != nil {
						err = rows.Err()
					}
					_ = rows.Close()
				}
				latency := time.Since(qStart)
				mu.Lock()
				if err != nil {
					errors++
				} else {
					latencies = append(latencies, latency)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return result{
		Name:    qc.Name,
		Count:   len(latencies),
		Errors:  errors,
		Min:     percentile(latencies, 0),
		P50:     percentile(latencies, 50),
		P95:     percentile(latencies, 95),
		Max:     percentile(latencies, 100),
		Elapsed: time.Since(start),
	}
}

func percentile(values []time.Duration, pct int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	if pct <= 0 {
		return values[0]
	}
	if pct >= 100 {
		return values[len(values)-1]
	}
	idx := (len(values)*pct + 99) / 100
	if idx < 1 {
		idx = 1
	}
	return values[idx-1]
}
