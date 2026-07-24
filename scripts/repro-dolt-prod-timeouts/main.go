// repro-dolt-prod-timeouts runs production-shaped bd CLI timeout scenarios.
//
// It initializes a real server-mode beads workspace, bulk-loads a graph that
// mirrors a large production deployment's skew (large mostly-closed issue table, large
// dependency table, small active frontier), then forks actual bd commands.
//
// Usage:
//
//	go run ./scripts/repro-dolt-prod-timeouts --bd ./bd --scenario all
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

type config struct {
	BDPath        string
	Workspace     string
	SeedMode      string
	Scenario      string
	IssueCount    int
	DepCount      int
	Concurrency   int
	Ops           int
	Timeout       time.Duration
	ChainDepth    int
	KeepWorkdir   bool
	ManagedServer bool
}

type workspace struct {
	Dir      string
	BeadsDir string
	Port     int
	Database string
}

type opResult struct {
	Kind       string        `json:"kind"`
	Argv       []string      `json:"argv"`
	Latency    time.Duration `json:"latency"`
	TimedOut   bool          `json:"timed_out"`
	Err        string        `json:"err,omitempty"`
	StderrTail string        `json:"stderr_tail,omitempty"`
}

type job struct {
	Kind string
	Argv []string
	Env  []string
	Sh   string
}

var subprocessEnvDenylist = []string{
	"BEADS_DIR",
	"BEADS_DOLT_SERVER_PORT",
	"BEADS_DOLT_PORT",
	"BEADS_DOLT_SERVER_HOST",
	"BEADS_DOLT_SERVER_SOCKET",
}

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
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}
	bdPath, err := resolveExecutablePath(cfg.BDPath)
	if err != nil {
		return err
	}
	cfg.BDPath = bdPath

	ws, err := openOrCreateWorkspace(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if cfg.Workspace == "" {
			stopWorkspace(context.Background(), cfg, ws)
		}
		if cfg.KeepWorkdir {
			fmt.Printf("kept workdir: %s\n", ws.Dir)
			return
		}
		if cfg.Workspace == "" {
			_ = os.RemoveAll(ws.Dir)
		}
	}()

	fmt.Printf("workspace=%s port=%d database=%s\n", ws.Dir, ws.Port, ws.Database)
	if err := loadProductionShape(ctx, ws, cfg); err != nil {
		return err
	}

	scenarios, err := scenarioNames(cfg.Scenario)
	if err != nil {
		return err
	}
	for _, scenario := range scenarios {
		report(scenario, runScenario(ctx, cfg, ws, scenario))
	}
	return nil
}

func parseFlags(args []string) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("repro-dolt-prod-timeouts", flag.ContinueOnError)
	fs.StringVar(&cfg.BDPath, "bd", "bd", "bd binary to execute")
	fs.StringVar(&cfg.Workspace, "workspace", "", "existing workspace to test instead of creating a synthetic one")
	fs.StringVar(&cfg.SeedMode, "seed-mode", "full", "fixture seed mode: full, dep-only, none")
	fs.StringVar(&cfg.Scenario, "scenario", "all", "scenario: ready, dep, control, mixed, outage, cycle-current, cycle-deps-only, cycle-wisps-only, cycle-bfs, all")
	fs.IntVar(&cfg.IssueCount, "issues", 100000, "issue rows to seed")
	fs.IntVar(&cfg.DepCount, "deps", 85000, "dependency rows to seed")
	fs.IntVar(&cfg.Concurrency, "concurrency", 20, "concurrent bd processes")
	fs.IntVar(&cfg.Ops, "ops", 80, "total operations per scenario")
	fs.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "per-command timeout")
	fs.IntVar(&cfg.ChainDepth, "chain-depth", 0, "existing blocking chain depth behind each dep-add target")
	fs.BoolVar(&cfg.KeepWorkdir, "keep-workdir", false, "keep temp workspace")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if cfg.Workspace != "" && !flagPassed(fs, "seed-mode") {
		cfg.SeedMode = "none"
	}
	return cfg, nil
}

func flagPassed(fs *flag.FlagSet, name string) bool {
	passed := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			passed = true
		}
	})
	return passed
}

func resolveExecutablePath(path string) (string, error) {
	bdPath, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("find bd binary %q: %w", path, err)
	}
	absPath, err := filepath.Abs(bdPath)
	if err != nil {
		return "", fmt.Errorf("resolve bd binary %q: %w", bdPath, err)
	}
	return absPath, nil
}

var allScenarioNames = []string{
	"ready",
	"dep",
	"control",
	"mixed",
	"outage",
	"cycle-current",
	"cycle-deps-only",
	"cycle-wisps-only",
	"cycle-bfs",
}

func scenarioNames(scenario string) ([]string, error) {
	if scenario == "all" {
		return append([]string(nil), allScenarioNames...), nil
	}
	for _, name := range allScenarioNames {
		if scenario == name {
			return []string{scenario}, nil
		}
	}
	return nil, fmt.Errorf("unknown scenario %q", scenario)
}

func runScenario(ctx context.Context, cfg config, ws *workspace, scenario string) []opResult {
	switch scenario {
	case "ready":
		return runReadyScenario(ctx, cfg, ws)
	case "dep":
		return runDepScenario(ctx, cfg, ws)
	case "control":
		return runControlQueryScenario(ctx, cfg, ws)
	case "mixed":
		return runMixedCityLoadScenario(ctx, cfg, ws)
	case "outage":
		return runOutageScenario(ctx, cfg, ws)
	case "cycle-current":
		return runCycleCheckScenario(ctx, cfg, ws, cycleCheckCurrentSQL)
	case "cycle-deps-only":
		return runCycleCheckScenario(ctx, cfg, ws, cycleCheckDependenciesOnlySQL)
	case "cycle-wisps-only":
		return runCycleCheckScenario(ctx, cfg, ws, cycleCheckWispsOnlySQL)
	case "cycle-bfs":
		return runCycleCheckScenario(ctx, cfg, ws, cycleCheckBatchedBFS)
	default:
		return []opResult{{Kind: scenario, Err: fmt.Sprintf("unknown scenario %q", scenario)}}
	}
}

func openOrCreateWorkspace(ctx context.Context, cfg config) (*workspace, error) {
	if cfg.Workspace != "" {
		return openWorkspace(ctx, cfg, cfg.Workspace)
	}
	return createWorkspace(ctx, cfg)
}

func openWorkspace(ctx context.Context, cfg config, dir string) (*workspace, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	beadsDir := filepath.Join(absDir, ".beads")
	if _, err := os.Stat(beadsDir); err != nil {
		return nil, fmt.Errorf("open .beads in %s: %w", absDir, err)
	}
	port, err := readInt(filepath.Join(beadsDir, "dolt-server.port"))
	if err != nil {
		return nil, fmt.Errorf("read dolt-server.port: %w", err)
	}
	if !isPortOpen(port) {
		if err := startWorkspaceDolt(ctx, cfg, absDir); err != nil {
			return nil, err
		}
		port, err = readInt(filepath.Join(beadsDir, "dolt-server.port"))
		if err != nil {
			return nil, fmt.Errorf("read dolt-server.port: %w", err)
		}
	}
	database, err := readDoltDatabase(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return nil, err
	}
	return &workspace{Dir: absDir, BeadsDir: beadsDir, Port: port, Database: database}, nil
}

func isPortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second) // #nosec G704 -- loopback probe of a locally started dolt server; not attacker-controlled
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func createWorkspace(ctx context.Context, cfg config) (*workspace, error) {
	dir, err := os.MkdirTemp("", "bd-prod-timeout-*")
	if err != nil {
		return nil, err
	}

	initTimeout := cfg.Timeout * 4
	if initTimeout < 2*time.Minute {
		initTimeout = 2 * time.Minute
	}
	initCtx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()

	fmt.Printf("initializing server workspace timeout=%s\n", initTimeout)
	cmd := exec.CommandContext(initCtx, cfg.BDPath, // #nosec G702 -- fixed subcommand args; cfg.BDPath is an operator-supplied local binary path, not attacker input
		"init",
		"--server",
		"--prefix=perf",
		"--non-interactive",
		"--quiet",
		"--skip-hooks",
		"--skip-agents",
	)
	cmd.Dir = dir
	cmd.Env = subprocessEnv("BD_NON_INTERACTIVE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("bd init after %s: %w\n%s", initTimeout, err, string(out))
	}

	beadsDir := filepath.Join(dir, ".beads")
	port, err := readInt(filepath.Join(beadsDir, "dolt-server.port"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("read dolt-server.port: %w", err)
	}
	database, err := readDoltDatabase(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &workspace{Dir: dir, BeadsDir: beadsDir, Port: port, Database: database}, nil
}

func startWorkspaceDolt(ctx context.Context, cfg config, dir string) error {
	startTimeout := cfg.Timeout * 2
	if startTimeout < time.Minute {
		startTimeout = time.Minute
	}
	startCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()

	cmd := exec.CommandContext(startCtx, cfg.BDPath, "dolt", "start") // #nosec G702 -- fixed subcommand args; cfg.BDPath is an operator-supplied local binary path, not attacker input
	cmd.Dir = dir
	cmd.Env = subprocessEnv("BD_NON_INTERACTIVE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd dolt start after %s: %w\n%s", startTimeout, err, string(out))
	}
	return nil
}

func readDoltDatabase(path string) (string, error) {
	//nolint:gosec // G304: benchmark harness reads metadata from the selected workspace.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read metadata.json: %w", err)
	}
	var meta struct {
		DoltDatabase string `json:"dolt_database"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("parse metadata.json: %w", err)
	}
	if meta.DoltDatabase == "" {
		return "", fmt.Errorf("metadata.json missing dolt_database")
	}
	return meta.DoltDatabase, nil
}

func stopWorkspace(ctx context.Context, cfg config, ws *workspace) {
	cmd := exec.CommandContext(ctx, cfg.BDPath, "dolt", "stop")
	cmd.Dir = ws.Dir
	cmd.Env = subprocessEnv("BD_NON_INTERACTIVE=1")
	_ = cmd.Run()
}

func loadProductionShape(ctx context.Context, ws *workspace, cfg config) error {
	if cfg.SeedMode == "none" {
		fmt.Printf("seed skipped seed_mode=%s\n", cfg.SeedMode)
		return nil
	}

	dsn := doltutil.ServerDSN{
		Host:     "127.0.0.1",
		Port:     ws.Port,
		User:     "root",
		Database: ws.Database,
		Timeout:  cfg.Timeout,
	}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	start := time.Now()
	if err := seedProductionShape(ctx, db, cfg); err != nil {
		return err
	}
	fmt.Printf("seeded mode=%s issues=%d deps=%d in %s\n", cfg.SeedMode, cfg.IssueCount, cfg.DepCount, time.Since(start).Round(time.Millisecond))
	return nil
}

func seedProductionShape(ctx context.Context, db *sql.DB, cfg config) error {
	switch cfg.SeedMode {
	case "full":
		if err := insertIssues(ctx, db, cfg.IssueCount, cfg.Ops, cfg.ChainDepth); err != nil {
			return err
		}
		if err := insertDependencies(ctx, db, cfg.DepCount, cfg.IssueCount); err != nil {
			return err
		}
	case "dep-only":
		if err := insertDepIssues(ctx, db, cfg.Ops, cfg.ChainDepth); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown seed mode %q", cfg.SeedMode)
	}
	if cfg.ChainDepth > 0 {
		if err := insertDepAddChains(ctx, db, cfg.Ops, cfg.ChainDepth); err != nil {
			return err
		}
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		return fmt.Errorf("DOLT_ADD fixture: %w", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'seed production timeout fixture')"); err != nil {
		return fmt.Errorf("DOLT_COMMIT fixture: %w", err)
	}
	return nil
}

func insertIssues(ctx context.Context, db *sql.DB, count, depOps, chainDepth int) error {
	const batchSize = 500
	depIssueCount := depOps * 2
	if chainDepth > 0 {
		depIssueCount = depOps*(chainDepth+3) + chainDepth + 2
	}
	total := count + depIssueCount
	for start := 0; start < total; start += batchSize {
		end := start + batchSize
		if end > total {
			end = total
		}
		var q strings.Builder
		q.WriteString(`INSERT INTO issues
			(id, title, description, design, acceptance_criteria, notes,
			 status, priority, issue_type, assignee, metadata)
			VALUES `)
		args := make([]any, 0, (end-start)*11)
		for i := start; i < end; i++ {
			if i > start {
				q.WriteByte(',')
			}
			q.WriteString("(?,?,?,?,?,?,?,?,?,?,?)")
			id := fmt.Sprintf("perf-%06d", i)
			status := "closed"
			assignee := ""
			metadata := "{}"
			if i < 350 {
				status = "open"
			}
			if i < 40 {
				assignee = "example-org--control-dispatcher"
			}
			if i >= 40 && i < 80 {
				metadata = `{"route.routed_to":"example-org/control-dispatcher"}`
			}
			if i >= count {
				status = "open"
				id = depIssueID(i - count)
			}
			args = append(args, id, fmt.Sprintf("prod timeout issue %d", i), "fixture", "", "", "", status, (i%4)+1, "task", assignee, metadata)
		}
		if _, err := db.ExecContext(ctx, q.String(), args...); err != nil {
			return fmt.Errorf("insert issues %d-%d: %w", start, end, err)
		}
	}
	return nil
}

func insertDepIssues(ctx context.Context, db *sql.DB, depOps, chainDepth int) error {
	const batchSize = 500
	total := depFixtureIssueCount(depOps, chainDepth)
	for start := 0; start < total; start += batchSize {
		end := start + batchSize
		if end > total {
			end = total
		}
		var q strings.Builder
		q.WriteString(`INSERT INTO issues
			(id, title, description, design, acceptance_criteria, notes,
			 status, priority, issue_type, assignee, metadata)
			VALUES `)
		args := make([]any, 0, (end-start)*11)
		for i := start; i < end; i++ {
			if i > start {
				q.WriteByte(',')
			}
			q.WriteString("(?,?,?,?,?,?,?,?,?,?,?)")
			args = append(args, depIssueID(i), fmt.Sprintf("prod copy dep issue %d", i), "fixture", "", "", "", "open", (i%4)+1, "task", "", "{}")
		}
		if _, err := db.ExecContext(ctx, q.String(), args...); err != nil {
			return fmt.Errorf("insert dep issues %d-%d: %w", start, end, err)
		}
	}
	return nil
}

func depFixtureIssueCount(depOps, chainDepth int) int {
	if chainDepth > 0 {
		return depOps*(chainDepth+3) + chainDepth + 2
	}
	return depOps * 2
}

func insertDependencies(ctx context.Context, db *sql.DB, count, issueCount int) error {
	const batchSize = 500
	if count <= 0 {
		return nil
	}
	maxPairs, err := maxDependencyPairs(issueCount)
	if err != nil {
		return err
	}
	if count > maxPairs {
		return fmt.Errorf("dependency count %d exceeds unique dependency pairs for %d issues", count, issueCount)
	}
	for start := 0; start < count; start += batchSize {
		end := start + batchSize
		if end > count {
			end = count
		}
		var q strings.Builder
		q.WriteString(`INSERT INTO dependencies
			(id, issue_id, depends_on_issue_id, type, created_by, metadata)
			VALUES `)
		args := make([]any, 0, (end-start)*6)
		for i := start; i < end; i++ {
			if i > start {
				q.WriteByte(',')
			}
			q.WriteString("(?,?,?,?,?,?)")
			issueID, dependsOnID := dependencyEndpoints(i, issueCount, 1000, 300)
			depType := "parent-child"
			if i < 20 || (i >= 40 && i < 60) {
				issueID, dependsOnID = dependencyEndpoints(i, issueCount, 0, 200)
				depType = "blocks"
			} else if i < 5000 {
				depType = "blocks"
			}
			args = append(args, depid.New(issueID, dependsOnID), issueID, dependsOnID, depType, "bench", "{}")
		}
		if _, err := db.ExecContext(ctx, q.String(), args...); err != nil {
			return fmt.Errorf("insert dependencies %d-%d: %w", start, end, err)
		}
	}
	return nil
}

func maxDependencyPairs(issueCount int) (int, error) {
	if issueCount <= 0 {
		return 0, fmt.Errorf("issue count must be positive, got %d", issueCount)
	}
	if issueCount == 1 {
		return 1, nil
	}
	return issueCount * (issueCount - 1), nil
}

func dependencyEndpoints(i, issueCount, sourceOffset, targetOffset int) (string, string) {
	if issueCount == 1 {
		return perfIssueID(0), perfIssueID(0)
	}
	source := (i + sourceOffset) % issueCount
	round := i / issueCount
	offset := 1 + ((targetOffset - 1 + round) % (issueCount - 1))
	target := (source + offset) % issueCount
	return perfIssueID(source), perfIssueID(target)
}

func perfIssueID(i int) string {
	return fmt.Sprintf("perf-%06d", i)
}

func insertDepAddChains(ctx context.Context, db *sql.DB, ops, depth int) error {
	const batchSize = 500
	if depth <= 0 || ops <= 0 {
		return nil
	}

	total := ops * depth
	for start := 0; start < total; start += batchSize {
		end := start + batchSize
		if end > total {
			end = total
		}
		var q strings.Builder
		q.WriteString(`INSERT INTO dependencies
			(id, issue_id, depends_on_issue_id, type, created_by, metadata)
			VALUES `)
		args := make([]any, 0, (end-start)*6)
		for i := start; i < end; i++ {
			if i > start {
				q.WriteByte(',')
			}
			q.WriteString("(?,?,?,?,?,?)")
			op := i / depth
			step := i % depth
			base := depBase(op, depth)
			issueID := depIssueID(base + 1 + step)
			dependsOnID := depIssueID(base + 2 + step)
			args = append(args, depid.New(issueID, dependsOnID), issueID, dependsOnID, "blocks", "bench", "{}")
		}
		if _, err := db.ExecContext(ctx, q.String(), args...); err != nil {
			return fmt.Errorf("insert dep-add chains %d-%d: %w", start, end, err)
		}
	}
	return nil
}

func runReadyScenario(ctx context.Context, cfg config, ws *workspace) []opResult {
	jobs := make([]job, 0, cfg.Ops)
	for i := 0; i < cfg.Ops; i++ {
		if i%2 == 0 {
			jobs = append(jobs, job{Kind: "ready", Argv: []string{"ready", "--assignee=example-org--control-dispatcher", "--json", "--limit=20"}})
		} else {
			jobs = append(jobs, job{Kind: "ready", Argv: []string{"ready", "--metadata-field", "route.routed_to=example-org/control-dispatcher", "--unassigned", "--json", "--limit=20"}})
		}
	}
	return runJobs(ctx, cfg, ws, jobs)
}

func runDepScenario(ctx context.Context, cfg config, ws *workspace) []opResult {
	jobs := make([]job, 0, cfg.Ops)
	for i := 0; i < cfg.Ops; i++ {
		jobs = append(jobs, depAddJob(i, cfg.ChainDepth))
	}
	return runJobs(ctx, cfg, ws, jobs)
}

func runControlQueryScenario(ctx context.Context, cfg config, ws *workspace) []opResult {
	return runJobs(ctx, cfg, ws, controlQueryJobs(cfg.Ops))
}

func runOutageScenario(ctx context.Context, cfg config, ws *workspace) []opResult {
	jobs := make([]job, 0, cfg.Ops*2)
	jobs = append(jobs, controlQueryJobs(cfg.Ops)...)
	for i := 0; i < cfg.Ops; i++ {
		jobs = append(jobs, depAddJob(i, cfg.ChainDepth))
	}
	return runJobs(ctx, cfg, ws, jobs)
}

type cycleCheckFunc func(context.Context, *sql.DB, string, string, int) error

func runCycleCheckScenario(ctx context.Context, cfg config, ws *workspace, check cycleCheckFunc) []opResult {
	dsn := doltutil.ServerDSN{
		Host:     "127.0.0.1",
		Port:     ws.Port,
		User:     "root",
		Database: ws.Database,
		Timeout:  cfg.Timeout,
	}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return []opResult{{Kind: "cycle-open", Err: err.Error()}}
	}
	defer db.Close()
	db.SetMaxOpenConns(cfg.Concurrency)
	db.SetMaxIdleConns(cfg.Concurrency)

	start := time.Now()
	jobCh := make(chan int)
	resCh := make(chan opResult, cfg.Ops)
	var wg sync.WaitGroup
	for w := 0; w < cfg.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobCh {
				opCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
				base := depBase(i, cfg.ChainDepth)
				issueID := depIssueID(base)
				dependsOnID := depIssueID(base + 1)
				opStart := time.Now()
				err := check(opCtx, db, issueID, dependsOnID, cfg.ChainDepth)
				latency := time.Since(opStart)
				res := opResult{Kind: "cycle", Argv: []string{"cycle-check", issueID, dependsOnID}, Latency: latency}
				if opCtx.Err() == context.DeadlineExceeded {
					res.TimedOut = true
				}
				if err != nil {
					res.Err = err.Error()
					res.StderrTail = tail(err.Error(), 300)
				}
				cancel()
				resCh <- res
			}
		}()
	}
	for i := 0; i < cfg.Ops; i++ {
		jobCh <- i
	}
	close(jobCh)
	wg.Wait()
	close(resCh)

	results := make([]opResult, 0, cfg.Ops)
	for res := range resCh {
		results = append(results, res)
	}
	fmt.Printf("scenario wall=%s\n", time.Since(start).Round(time.Millisecond))
	return results
}

func cycleCheckCurrentSQL(ctx context.Context, db *sql.DB, issueID, dependsOnID string, _ int) error {
	var reachable int
	query := fmt.Sprintf(`
		WITH RECURSIVE reachable AS (
			SELECT ? AS node, 0 AS depth
			UNION ALL
			SELECT d.target_id, r.depth + 1
			FROM reachable r
			JOIN (
				SELECT issue_id, %s AS target_id FROM dependencies WHERE type IN ('blocks', 'conditional-blocks')
				UNION ALL
				SELECT issue_id, %s AS target_id FROM wisp_dependencies WHERE type IN ('blocks', 'conditional-blocks')
			) d ON d.issue_id = r.node
			WHERE r.depth < 100
		)
		SELECT COUNT(*) FROM reachable WHERE node = ?
	`, dependencyTargetExpr, dependencyTargetExpr)
	err := db.QueryRowContext(ctx, query, dependsOnID, issueID).Scan(&reachable)
	if err != nil {
		return err
	}
	if reachable > 0 {
		return fmt.Errorf("cycle detected")
	}
	return nil
}

func cycleCheckDependenciesOnlySQL(ctx context.Context, db *sql.DB, issueID, dependsOnID string, _ int) error {
	return cycleCheckOneTableSQL(ctx, db, "dependencies", issueID, dependsOnID)
}

func cycleCheckWispsOnlySQL(ctx context.Context, db *sql.DB, issueID, dependsOnID string, _ int) error {
	return cycleCheckOneTableSQL(ctx, db, "wisp_dependencies", issueID, dependsOnID)
}

func cycleCheckOneTableSQL(ctx context.Context, db *sql.DB, table, issueID, dependsOnID string) error {
	var reachable int
	//nolint:gosec // G201: table is selected by fixed scenario wrappers.
	query := fmt.Sprintf(`
		WITH RECURSIVE reachable AS (
			SELECT ? AS node, 0 AS depth
			UNION ALL
			SELECT %s, r.depth + 1
			FROM reachable r
			JOIN %s d ON d.issue_id = r.node
			WHERE d.type IN ('blocks', 'conditional-blocks') AND r.depth < 100
		)
		SELECT COUNT(*) FROM reachable WHERE node = ?
	`, dependencyTargetExpr, table)
	if err := db.QueryRowContext(ctx, query, dependsOnID, issueID).Scan(&reachable); err != nil {
		return err
	}
	if reachable > 0 {
		return fmt.Errorf("cycle detected")
	}
	return nil
}

func cycleCheckBatchedBFS(ctx context.Context, db *sql.DB, issueID, dependsOnID string, maxDepth int) error {
	if maxDepth <= 0 || maxDepth > 100 {
		maxDepth = 100
	}
	seen := map[string]struct{}{dependsOnID: {}}
	frontier := []string{dependsOnID}
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		next, err := fetchBlockingTargets(ctx, db, frontier)
		if err != nil {
			return err
		}
		frontier = frontier[:0]
		for _, id := range next {
			if id == issueID {
				return fmt.Errorf("cycle detected")
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			frontier = append(frontier, id)
		}
	}
	return nil
}

func fetchBlockingTargets(ctx context.Context, db *sql.DB, issueIDs []string) ([]string, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(issueIDs)*2)
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(issueIDs)), ",")
	//nolint:gosec // G201: placeholders are generated from ? markers only.
	query := fmt.Sprintf(`
		SELECT %s FROM dependencies
		WHERE issue_id IN (%s) AND type IN ('blocks', 'conditional-blocks')
		UNION ALL
		SELECT %s FROM wisp_dependencies
		WHERE issue_id IN (%s) AND type IN ('blocks', 'conditional-blocks')
	`, dependencyTargetExpr, placeholders, dependencyTargetExpr, placeholders)
	for _, id := range issueIDs {
		args = append(args, id)
	}
	for _, id := range issueIDs {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func runMixedCityLoadScenario(ctx context.Context, cfg config, ws *workspace) []opResult {
	start := time.Now()
	depCount := cfg.Ops
	backgroundCount := cfg.Ops * cfg.Concurrency

	results := make([]opResult, 0, depCount+backgroundCount)
	resultCh := make(chan opResult, depCount+backgroundCount)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < depCount; i++ {
			job := depAddJob(i, cfg.ChainDepth)
			job.Kind = "dispatcher-dep"
			resultCh <- runJob(ctx, cfg, ws, job)
		}
	}()

	backgroundJobs := mixedBackgroundJobs(backgroundCount)
	jobCh := make(chan job)
	for w := 0; w < cfg.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				resultCh <- runJob(ctx, cfg, ws, job)
			}
		}()
	}
	for _, job := range backgroundJobs {
		jobCh <- job
	}
	close(jobCh)

	wg.Wait()
	close(resultCh)
	for res := range resultCh {
		results = append(results, res)
	}
	fmt.Printf("scenario wall=%s\n", time.Since(start).Round(time.Millisecond))
	return results
}

func mixedBackgroundJobs(count int) []job {
	jobs := make([]job, 0, count)
	for i := 0; i < count; i++ {
		switch i % 6 {
		case 0:
			jobs = append(jobs, job{Kind: "session-ready", Argv: []string{"ready", "--include-ephemeral", "--assignee=" + sessionAssignee(i), "--json", "--limit=1"}})
		case 1:
			jobs = append(jobs, job{Kind: "control-ready", Argv: []string{"--readonly", "--sandbox", "ready", "--include-ephemeral", "--assignee=example-org--control-dispatcher", "--json", "--limit=20"}})
		case 2:
			jobs = append(jobs, job{Kind: "route-ready", Argv: []string{"--readonly", "--sandbox", "ready", "--include-ephemeral", "--metadata-field", "route.routed_to=example-org/control-dispatcher", "--unassigned", "--json", "--limit=20"}})
		case 3:
			jobs = append(jobs, job{Kind: "show", Argv: []string{"show", fmt.Sprintf("perf-%06d", i%350), "--json"}})
		case 4:
			jobs = append(jobs, job{Kind: "list", Argv: []string{"list", "--json", "--status", "in_progress", "--assignee=" + sessionAssignee(i), "--limit=1"}})
		case 5:
			jobs = append(jobs, job{Kind: "claim", Argv: []string{"update", fmt.Sprintf("perf-%06d", i%40), "--claim", "--json"}})
		}
	}
	return jobs
}

func sessionAssignee(i int) string {
	return fmt.Sprintf("mc-%07d", i%64)
}

func depAddJob(i, chainDepth int) job {
	base := depBase(i, chainDepth)
	return job{
		Kind: "dep",
		Argv: []string{"dep", "add", depIssueID(base), depIssueID(base + 1), "--type", "blocks", "--json"},
	}
}

func depBase(i, chainDepth int) int {
	if chainDepth <= 0 {
		return i * 2
	}
	return i * (chainDepth + 3)
}

func controlQueryJobs(count int) []job {
	targets := []struct {
		target  string
		session string
		legacy  string
	}{
		{target: "control-dispatcher", session: "control-dispatcher", legacy: "workflow-control"},
		{target: "example-org/control-dispatcher", session: "example-org--control-dispatcher", legacy: "example-org/workflow-control"},
		{target: "example-gui/control-dispatcher", session: "example-gui--control-dispatcher", legacy: "example-gui/workflow-control"},
		{target: "gtest-rig/control-dispatcher", session: "gtest-rig--control-dispatcher", legacy: "gtest-rig/workflow-control"},
	}

	jobs := make([]job, 0, count)
	script := controlQueryScript()
	for i := 0; i < count; i++ {
		t := targets[i%len(targets)]
		jobs = append(jobs, job{
			Kind: "control",
			Sh:   script,
			Env: []string{
				"BD_EXPORT_AUTO=false",
				"GC_CONTROL_TARGET=" + t.target,
				"GC_CONTROL_SESSION_NAME=" + t.session,
				"GC_CONTROL_LEGACY_TARGET=" + t.legacy,
				"GC_SESSION_NAME=" + t.session,
				"GC_ALIAS=" + t.target,
				"GC_SESSION_ID=" + t.session,
			},
		})
	}
	return jobs
}

func controlQueryScript() string {
	return `BD_EXPORT_AUTO=false
tmp=$(mktemp)
err=$(mktemp)
trap "rm -f \"$tmp\" \"$err\"" EXIT
emit_ready() {
  r=$("$@" 2>"$err")
  status=$?
  if [ "$status" -ne 0 ]; then
    printf "ready probe failed (%s):\n" "$*" >&2
    cat "$err" >&2
    return "$status"
  fi
  [ -n "$r" ] && [ "$r" != "[]" ] && printf "%s\n" "$r" >> "$tmp"
}
for id in "$GC_CONTROL_SESSION_NAME" "$GC_SESSION_NAME" "$GC_ALIAS" "$GC_CONTROL_TARGET" "$GC_SESSION_ID"; do
  [ -z "$id" ] && continue
  legacy=""
  case "$id" in *control-dispatcher) legacy="${id%control-dispatcher}workflow-control";; esac
  for cand in "$id" "$legacy"; do
    [ -z "$cand" ] && continue
    emit_ready "$BD_BIN" --readonly --sandbox ready --assignee="$cand" --json --limit=20 || exit $?
  done
done
emit_ready "$BD_BIN" --readonly --sandbox ready --metadata-field "route.routed_to=$GC_CONTROL_TARGET" --unassigned --json --limit=20 || exit $?
emit_ready "$BD_BIN" --readonly --sandbox ready --metadata-field "route.routed_to=$GC_CONTROL_LEGACY_TARGET" --unassigned --json --limit=20 || exit $?
[ -s "$tmp" ] && jq -s 'reduce add[] as $item ([]; if any(.[]; .id == $item.id) then . else . + [$item] end)' "$tmp" || printf "[]"
`
}

func depIssueID(i int) string {
	return fmt.Sprintf("perf-dep-%06d", i)
}

func runJobs(ctx context.Context, cfg config, ws *workspace, jobs []job) []opResult {
	start := time.Now()
	jobCh := make(chan job)
	resCh := make(chan opResult, len(jobs))
	var wg sync.WaitGroup
	for w := 0; w < cfg.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				resCh <- runJob(ctx, cfg, ws, job)
			}
		}()
	}
	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)
	wg.Wait()
	close(resCh)

	results := make([]opResult, 0, len(jobs))
	for res := range resCh {
		results = append(results, res)
	}
	fmt.Printf("scenario wall=%s\n", time.Since(start).Round(time.Millisecond))
	return results
}

func runJob(ctx context.Context, cfg config, ws *workspace, j job) opResult {
	if j.Sh != "" {
		return runShell(ctx, cfg, ws, j)
	}
	return runBD(ctx, cfg, ws, j)
}

func runBD(ctx context.Context, cfg config, ws *workspace, j job) opResult {
	opCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(opCtx, cfg.BDPath, j.Argv...)
	cmd.Dir = ws.Dir
	cmd.Env = subprocessEnv(append([]string{"BD_NON_INTERACTIVE=1"}, j.Env...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	latency := time.Since(start)
	res := opResult{Kind: j.Kind, Argv: j.Argv, Latency: latency}
	if len(res.Argv) == 0 {
		res.Argv = []string{"sh", "-c", compactShell(j.Sh)}
	}
	if opCtx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
	}
	if err != nil {
		res.Err = err.Error()
	}
	res.StderrTail = tail(stderr.String(), 300)
	return res
}

func runShell(ctx context.Context, cfg config, ws *workspace, j job) opResult {
	opCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(opCtx, "sh", "-c", j.Sh)
	cmd.Dir = ws.Dir
	cmd.Env = subprocessEnv(append([]string{"BD_NON_INTERACTIVE=1", "BD_BIN=" + cfg.BDPath}, j.Env...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	latency := time.Since(start)
	res := opResult{Kind: j.Kind, Argv: []string{"sh", "-c", compactShell(j.Sh)}, Latency: latency}
	if opCtx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
	}
	if err != nil {
		res.Err = err.Error()
	}
	res.StderrTail = tail(stderr.String(), 300)
	return res
}

func compactShell(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func report(name string, results []opResult) {
	sort.Slice(results, func(i, j int) bool { return results[i].Latency < results[j].Latency })
	var failures, timeouts int
	var driverReadTimeouts int
	for _, r := range results {
		if r.Err != "" {
			failures++
		}
		if r.TimedOut {
			timeouts++
		}
		if isDriverReadTimeout(r) {
			driverReadTimeouts++
		}
	}
	fmt.Printf("\n[%s] ops=%d failures=%d harness_timeouts=%d driver_read_timeouts=%d p50=%s p95=%s max=%s\n",
		name, len(results), failures, timeouts, driverReadTimeouts,
		percentile(results, 50).Round(time.Millisecond),
		percentile(results, 95).Round(time.Millisecond),
		percentile(results, 100).Round(time.Millisecond),
	)
	for i := len(results) - 1; i >= 0 && i >= len(results)-5; i-- {
		r := results[i]
		fmt.Printf("  slow kind=%s latency=%s timeout=%t err=%q stderr=%q argv=%s\n",
			r.Kind, r.Latency.Round(time.Millisecond), r.TimedOut, r.Err, r.StderrTail, strings.Join(r.Argv, " "))
	}
}

func isDriverReadTimeout(r opResult) bool {
	return strings.Contains(r.StderrTail, "packets.go:58 read tcp") &&
		strings.Contains(r.StderrTail, "i/o timeout")
}

func percentile(results []opResult, p int) time.Duration {
	if len(results) == 0 {
		return 0
	}
	if p >= 100 {
		return results[len(results)-1].Latency
	}
	idx := (len(results)*p + 99) / 100
	if idx <= 0 {
		idx = 1
	}
	return results[idx-1].Latency
}

func readInt(path string) (int, error) {
	//nolint:gosec // G304: benchmark harness reads control files from the selected workspace.
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func subprocessEnv(extra ...string) []string {
	env := cleanEnv(os.Environ(), subprocessEnvDenylist...)
	return append(env, extra...)
}

func cleanEnv(env []string, keys ...string) []string {
	drop := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		drop[key] = struct{}{}
	}
	out := env[:0]
	for _, e := range env {
		key, _, ok := strings.Cut(e, "=")
		if ok {
			if _, skip := drop[key]; skip {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

func tail(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
