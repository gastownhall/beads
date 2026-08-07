package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// doltSQLRunner is the public-SQL boundary for Dolt observations. Its caller
// is responsible for running `bd sql --csv <query>` in the target workspace;
// this collector deliberately has no access to a Dolt directory or driver.
type doltSQLRunner interface {
	SQLCSV(context.Context, string) ([]byte, error)
}

// pinnedDoltRunner observes historical repositories through the stable public
// Dolt CLI SQL surface. Historical bd creates the repository; one pinned Dolt
// version canonicalizes every legacy/server schema without version adapters.
type pinnedDoltRunner struct {
	binary      string
	workspace   string
	dataDir     string
	host        string
	port        int
	database    string
	environment []string
}

func pinnedDoltServerRunner(binary, workspace, database string, environment []string, port int) pinnedDoltRunner {
	return pinnedDoltRunner{
		binary: binary, workspace: workspace,
		host: "127.0.0.1", port: port, database: database, environment: environment,
	}
}

func (r pinnedDoltRunner) command(ctx context.Context, query string) *exec.Cmd {
	args := make([]string, 0, 10)
	if r.dataDir != "" {
		args = append(args, "--data-dir="+r.dataDir)
	} else {
		args = append(args,
			"--host="+r.host,
			"--port="+strconv.Itoa(r.port),
			"--no-tls",
		)
	}
	if r.database != "" {
		query = "USE " + doltIdentifier(r.database) + "; " + query
	}
	args = append(args, "sql", "-r", "csv", "-q", query)
	command := exec.CommandContext(ctx, r.binary, args...)
	command.Dir = r.workspace
	environment := r.environment
	if len(environment) == 0 {
		environment = os.Environ()
	}
	command.Env = appendWithoutKey(environment, "DOLT_DISABLE_EVENT_FLUSH", "DOLT_DISABLE_EVENT_FLUSH=1")
	return command
}

func (r pinnedDoltRunner) SQLCSV(ctx context.Context, query string) ([]byte, error) {
	local := r.dataDir != "" && r.host == "" && r.port == 0
	remote := r.dataDir == "" && r.host == "127.0.0.1" && r.port > 0 && r.port <= 65535
	if r.binary == "" || r.workspace == "" || (!local && !remote) {
		return nil, errors.New("invalid pinned Dolt SQL runner")
	}
	output, err := commandCSVOutput(r.command(ctx, query))
	if err != nil {
		return nil, fmt.Errorf("pinned Dolt SQL: %w", err)
	}
	return output, nil
}

// discoverDoltServerDatabase identifies the sole application schema exposed
// by an active historical server. Server metadata is intentionally not used:
// early releases did not record the database name there.
func discoverDoltServerDatabase(ctx context.Context, runner doltSQLRunner) (string, error) {
	userDatabases, err := listDoltUserDatabases(ctx, runner)
	if err != nil {
		return "", err
	}
	if len(userDatabases) != 1 {
		return "", fmt.Errorf("SHOW DATABASES found %d user databases; require exactly one user database", len(userDatabases))
	}
	return userDatabases[0], nil
}

// listDoltUserDatabases observes every public user schema without relying on
// Dolt's on-disk layout. It retains names in canonical order so callers can
// fingerprint every candidate before deciding whether a topology is allowed.
func listDoltUserDatabases(ctx context.Context, runner doltSQLRunner) ([]string, error) {
	records, err := runDoltCSV(ctx, runner, "SHOW DATABASES")
	if err != nil {
		return nil, fmt.Errorf("SHOW DATABASES: %w", err)
	}
	if len(records.columns) != 1 || records.columns[0] != "database" {
		return nil, errors.New("SHOW DATABASES returned an unexpected schema")
	}
	userDatabases := make([]string, 0, len(records.rows))
	seen := make(map[string]bool, len(records.rows))
	for _, row := range records.rows {
		database := strings.TrimSpace(row[0])
		if database == "" {
			return nil, errors.New("SHOW DATABASES returned an empty database name")
		}
		if seen[database] {
			return nil, fmt.Errorf("SHOW DATABASES returned duplicate database %q", database)
		}
		seen[database] = true
		if isDoltSystemDatabase(database) {
			continue
		}
		userDatabases = append(userDatabases, database)
	}
	sort.Strings(userDatabases)
	return userDatabases, nil
}

func isDoltSystemDatabase(database string) bool {
	switch strings.ToLower(database) {
	case "information_schema", "mysql", "dolt":
		return true
	default:
		return false
	}
}

func discoverActiveDoltServerDatabase(
	ctx context.Context,
	binary, workspace string,
	environment []string,
	host string,
	port int,
) (string, error) {
	runner := pinnedDoltServerRunner(binary, workspace, "", environment, port)
	runner.host = host
	return discoverDoltServerDatabase(ctx, runner)
}

func discoverMixedDoltDatabases(ctx context.Context, legacy, embedded doltSQLRunner) (string, string, error) {
	legacyDatabase, err := discoverDoltServerDatabase(ctx, legacy)
	if err != nil {
		return "", "", fmt.Errorf("discover legacy Dolt database: %w", err)
	}
	embeddedDatabase, err := discoverDoltServerDatabase(ctx, embedded)
	if err != nil {
		return "", "", fmt.Errorf("discover embedded Dolt database: %w", err)
	}
	return legacyDatabase, embeddedDatabase, nil
}

func appendWithoutKey(environment []string, key, value string) []string {
	result := make([]string, 0, len(environment)+1)
	prefix := key + "="
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, value)
}

const (
	pinnedDoltRuntimeVersion = "2.1.8"
	pinnedDoltRuntimeSHA256  = "853c8c0a798cd6f47d0be8071cc32e071054a7d35ff4b0f0812330d35212bdce"
)

var pinnedDoltRuntimePattern = regexp.MustCompile(`(^|[^0-9])2\.1\.8([^0-9]|$)`)

// doltServerFallback owns the short-lived external server used when a
// historical bd can only expose its Dolt database through server mode.
// It only starts the process in .beads/dolt; it never reads Dolt files.
type doltServerFallback struct {
	workspace   string
	doltBin     string
	environment []string
	port        int
	command     *exec.Cmd
	done        chan error
}

// startDoltServerFallback starts the pinned server on an ephemeral loopback
// port and must be closed before removing the isolated workspace.
func startDoltServerFallback(
	ctx context.Context,
	workspace, doltBin string,
	environment []string,
	requestedPort int,
) (*doltServerFallback, error) {
	if workspace == "" {
		return nil, errors.New("Dolt fallback workspace is empty")
	}
	dataDir := filepath.Join(workspace, ".beads", "dolt")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Dolt fallback data directory: %w", err)
	}
	resolved, err := resolveDoltRuntime(ctx, doltBin)
	if err != nil {
		return nil, err
	}
	attempts := 3
	if requestedPort != 0 {
		if requestedPort < 1 || requestedPort > 65535 {
			return nil, fmt.Errorf("invalid requested Dolt fallback port %d", requestedPort)
		}
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		port := requestedPort
		if port == 0 {
			port, err = allocateDoltFallbackPort()
			if err != nil {
				return nil, err
			}
		}
		fallback := &doltServerFallback{
			workspace: workspace, doltBin: resolved,
			environment: append([]string(nil), environment...), port: port,
		}
		command, err := fallback.serverCommand()
		if err != nil {
			return nil, err
		}
		if err := command.Start(); err != nil {
			continue // The listener can win the ephemeral-port race; retry it.
		}
		fallback.command = command
		fallback.done = make(chan error, 1)
		go func() { fallback.done <- command.Wait() }()
		if err := fallback.waitReady(ctx); err == nil {
			return fallback, nil
		}
		_ = fallback.Close()
	}
	return nil, errors.New("start pinned external Dolt server on loopback")
}

func resolveDoltRuntime(ctx context.Context, binary string) (string, error) {
	if binary == "" {
		binary = os.Getenv("DOLT_BIN")
	}
	if binary == "" {
		binary = "dolt"
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("locate pinned Dolt %s: %w", pinnedDoltRuntimeVersion, err)
	}
	if err := verifyDoltRuntimeExecutable(resolved); err != nil {
		return "", fmt.Errorf("Dolt runtime %q: %w", resolved, err)
	}
	output, err := exec.CommandContext(ctx, resolved, "version").CombinedOutput() //nolint:gosec // resolved passed the pinned Dolt executable digest check above.
	if err != nil {
		return "", fmt.Errorf("run Dolt runtime %q: %w", resolved, err)
	}
	if err := verifyDoltRuntimeVersion(string(output)); err != nil {
		return "", fmt.Errorf("Dolt runtime %q: %w", resolved, err)
	}
	return resolved, nil
}

func verifyDoltRuntimeVersion(output string) error {
	if !pinnedDoltRuntimePattern.MatchString(output) {
		return fmt.Errorf("require pinned Dolt %s, got %q", pinnedDoltRuntimeVersion, strings.TrimSpace(output))
	}
	return nil
}

func verifyDoltRuntimeExecutable(path string) error {
	if err := verifyPinnedDoltRuntimePlatform(runtime.GOOS, runtime.GOARCH); err != nil {
		return err
	}
	file, err := os.Open(path) //nolint:gosec // path is explicit runtime configuration verified by its pinned SHA-256 below.
	if err != nil {
		return fmt.Errorf("open executable: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash executable: %w", err)
	}
	return verifyPinnedDoltRuntimeDigest(fmt.Sprintf("%x", hash.Sum(nil)))
}

func verifyPinnedDoltRuntimePlatform(goos, goarch string) error {
	if goos != "linux" || goarch != "amd64" {
		return fmt.Errorf("require pinned Dolt %s for linux/amd64, got %s/%s", pinnedDoltRuntimeVersion, goos, goarch)
	}
	return nil
}

func verifyPinnedDoltRuntimeDigest(actual string) error {
	if actual != pinnedDoltRuntimeSHA256 {
		return fmt.Errorf("require pinned Dolt %s linux/amd64 SHA-256 %s, got %s", pinnedDoltRuntimeVersion, pinnedDoltRuntimeSHA256, actual)
	}
	return nil
}

func allocateDoltFallbackPort() (port int, err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate Dolt fallback port: %w", err)
	}
	defer func() {
		if closeErr := listener.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close Dolt fallback port listener: %w", closeErr))
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// requireHistoricalDoltAutoDetectPortsFree prevents v0.49-era init commands
// from silently attaching the census workspace to an unrelated local server.
func requireHistoricalDoltAutoDetectPortsFree(ports ...int) error {
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("invalid historical Dolt auto-detect port %d", port)
		}
		connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 100*time.Millisecond)
		if err != nil {
			continue
		}
		_ = connection.Close()
		return fmt.Errorf(
			"loopback port %d is occupied; run the census in an isolated environment to prevent historical Dolt auto-detection",
			port,
		)
	}
	return nil
}

func (f *doltServerFallback) serverCommand() (*exec.Cmd, error) {
	if f.workspace == "" || f.doltBin == "" || f.port < 1 || f.port > 65535 {
		return nil, errors.New("invalid Dolt fallback server configuration")
	}
	command := exec.Command(f.doltBin, "sql-server", "-H", "127.0.0.1", "-P", strconv.Itoa(f.port), "--loglevel=warning")
	command.Dir = filepath.Join(f.workspace, ".beads", "dolt")
	command.Env = doltFallbackEnv(f.baseEnvironment(), f.port)
	return command, nil
}

func (f *doltServerFallback) waitReady(ctx context.Context) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var lastProbeErr error
	for {
		if err := f.exitedBeforeReady(); err != nil {
			return err
		}
		probeContext, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		lastProbeErr = f.probeReady(probeContext)
		cancel()
		if lastProbeErr == nil {
			if err := f.exitedBeforeReady(); err != nil {
				return err
			}
			return nil
		}
		select {
		case serverErr := <-f.done:
			return f.recordExitBeforeReady(serverErr)
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for pinned external Dolt server SQL identity: %w", lastProbeErr)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (f *doltServerFallback) probeReady(ctx context.Context) error {
	runner := pinnedDoltServerRunner(f.doltBin, f.workspace, "", f.baseEnvironment(), f.port)
	records, err := runDoltCSV(ctx, runner, "SELECT dolt_version() AS dolt_version")
	if err != nil {
		return err
	}
	if len(records.columns) != 1 || records.columns[0] != "dolt_version" ||
		len(records.rows) != 1 || len(records.rows[0]) != 1 {
		return errors.New("pinned external Dolt server returned an unexpected identity result")
	}
	if got := records.rows[0][0]; got != pinnedDoltRuntimeVersion {
		return fmt.Errorf("pinned external Dolt server version = %q, want %q", got, pinnedDoltRuntimeVersion)
	}
	return nil
}

func (f *doltServerFallback) exitedBeforeReady() error {
	if f.done == nil {
		return errors.New("pinned external Dolt server has no active child process")
	}
	select {
	case serverErr := <-f.done:
		return f.recordExitBeforeReady(serverErr)
	default:
		return nil
	}
}

func (f *doltServerFallback) recordExitBeforeReady(serverErr error) error {
	f.done = nil // Wait has already reaped the process; Close is now a no-op.
	if serverErr == nil {
		return errors.New("pinned external Dolt server exited before ready")
	}
	return fmt.Errorf("pinned external Dolt server exited before ready: %w", serverErr)
}

func (f *doltServerFallback) baseEnvironment() []string {
	if len(f.environment) > 0 {
		return append([]string(nil), f.environment...)
	}
	return os.Environ()
}

func (f *doltServerFallback) Close() error {
	if f == nil || f.command == nil || f.command.Process == nil || f.done == nil {
		return nil
	}
	_ = f.command.Process.Signal(os.Interrupt)
	select {
	case err := <-f.done:
		f.done = nil
		return err
	case <-time.After(5 * time.Second):
		if err := f.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		err := <-f.done
		f.done = nil
		return err
	}
}

func commandCSVOutput(command *exec.Cmd) ([]byte, error) {
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func doltFallbackEnv(environment []string, port int) []string {
	result := make([]string, 0, len(environment)+4)
	for _, value := range environment {
		if strings.HasPrefix(value, "BEADS_DOLT_SERVER_HOST=") ||
			strings.HasPrefix(value, "BEADS_DOLT_SERVER_PORT=") ||
			strings.HasPrefix(value, "BEADS_NO_DAEMON=") ||
			strings.HasPrefix(value, "BEADS_DOLT_AUTO_START=") {
			continue
		}
		result = append(result, value)
	}
	return append(result,
		"BEADS_DOLT_SERVER_HOST=127.0.0.1",
		"BEADS_DOLT_SERVER_PORT="+strconv.Itoa(port),
		"BEADS_NO_DAEMON=1",
		"BEADS_DOLT_AUTO_START=0")
}

type doltFingerprint struct {
	Objects          []doltObject          `json:"objects"`
	Catalog          []doltCatalogSnapshot `json:"catalog"`
	MigrationLedgers []doltMigrationLedger `json:"migration_ledgers"`
	Capabilities     []doltCapability      `json:"capabilities"`
}

type doltObject struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Create string `json:"create"`
}

// doltCatalogSnapshot preserves the named information_schema relation rather
// than depending on the server's presentation of its table output.
type doltCatalogSnapshot struct {
	Name    string        `json:"name"`
	Columns []string      `json:"columns"`
	Rows    [][]doltValue `json:"rows"`
}

// doltValue keeps an empty SQL value distinct from NULL.
type doltValue struct {
	Null  bool   `json:"null"`
	Value string `json:"value"`
}

type doltMigrationLedger struct {
	Table   string        `json:"table"`
	Columns []string      `json:"columns"`
	Rows    [][]doltValue `json:"rows"`
}

// doltCapability makes a server-version limitation part of the fingerprint,
// instead of silently treating an unavailable catalog surface as empty.
type doltCapability struct {
	Name      string `json:"name"`
	Supported bool   `json:"supported"`
}

type doltCatalogQuery struct {
	name      string
	relation  string
	columns   []string
	predicate string
}

var doltCatalogQueries = []doltCatalogQuery{
	{"information_schema.columns", "information_schema.columns", []string{"table_name", "column_name", "ordinal_position", "column_default", "is_nullable", "data_type", "column_type", "character_maximum_length", "numeric_precision", "numeric_scale", "datetime_precision", "character_set_name", "collation_name", "column_key", "extra", "generation_expression"}, "table_schema = DATABASE()"},
	// Database/schema names, cardinality, timestamps, and definers are omitted:
	// they are runtime identity/statistics, not schema semantics, and often
	// contain the isolated workspace's generated database name.
	{"information_schema.statistics", "information_schema.statistics", []string{"table_name", "non_unique", "index_name", "seq_in_index", "column_name", "collation", "sub_part", "nullable", "index_type", "comment", "index_comment", "is_visible", "expression"}, "table_schema = DATABASE()"},
	{"information_schema.table_constraints", "information_schema.table_constraints", []string{"constraint_name", "table_name", "constraint_type", "enforced"}, "constraint_schema = DATABASE()"},
	{"information_schema.key_column_usage", "information_schema.key_column_usage", []string{"constraint_name", "table_name", "column_name", "ordinal_position", "position_in_unique_constraint", "referenced_table_name", "referenced_column_name"}, "constraint_schema = DATABASE()"},
	{"information_schema.referential_constraints", "information_schema.referential_constraints", []string{"constraint_name", "unique_constraint_name", "match_option", "update_rule", "delete_rule", "table_name", "referenced_table_name"}, "constraint_schema = DATABASE()"},
	{"information_schema.triggers", "information_schema.triggers", []string{"trigger_name", "event_manipulation", "event_object_table", "action_statement", "action_timing", "action_orientation", "action_condition", "action_reference_old_table", "action_reference_new_table", "action_reference_old_row", "action_reference_new_row", "sql_mode"}, "trigger_schema = DATABASE()"},
}

func (query doltCatalogQuery) sql() string {
	selects := make([]string, 0, len(query.columns)*2)
	for i, column := range query.columns {
		quoted := doltIdentifier(column)
		selects = append(selects,
			"CASE WHEN "+quoted+" IS NULL THEN 1 ELSE 0 END AS "+doltIdentifier(fmt.Sprintf("c%03d_null", i)),
			quoted)
	}
	return "SELECT " + strings.Join(selects, ", ") + " FROM " + query.relation + " WHERE " + query.predicate
}

func collectDolt(ctx context.Context, runner doltSQLRunner) (doltFingerprint, error) {
	if runner == nil {
		return doltFingerprint{}, errors.New("Dolt SQL runner is nil")
	}
	objects, err := collectDoltObjects(ctx, runner)
	if err != nil {
		return doltFingerprint{}, err
	}
	catalog := make([]doltCatalogSnapshot, 0, len(doltCatalogQueries))
	capabilities := make([]doltCapability, 0, len(doltCatalogQueries))
	for _, source := range doltCatalogQueries {
		snapshot, err := collectDoltCatalog(ctx, runner, source.name, source.sql())
		if err != nil {
			if isMissingDoltCatalogCapability(err, source) {
				// Preserve only demonstrated missing catalog surfaces as a version
				// capability. Transport, permission, and CSV failures are evidence
				// that the observation is incomplete and must fail closed.
				capabilities = append(capabilities, doltCapability{Name: source.name})
				continue
			}
			return doltFingerprint{}, fmt.Errorf("collect catalog %q: %w", source.name, err)
		}
		catalog = append(catalog, snapshot)
		capabilities = append(capabilities, doltCapability{Name: source.name, Supported: true})
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })
	ledgers, err := collectDoltMigrationLedgers(ctx, runner, objects, catalog)
	if err != nil {
		return doltFingerprint{}, err
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })
	return doltFingerprint{Objects: objects, Catalog: catalog, MigrationLedgers: ledgers, Capabilities: capabilities}, nil
}

func isMissingDoltCatalogCapability(err error, source doltCatalogQuery) bool {
	message := strings.ToLower(err.Error())
	if hasExactDoltCapabilityError(message, "table not found: "+strings.ToLower(source.relation)) {
		return true
	}
	for _, column := range source.columns {
		if hasExactDoltCapabilityError(message, `column "`+strings.ToLower(column)+`" could not be found in any table in scope`) {
			return true
		}
	}
	return false
}

func hasExactDoltCapabilityError(message, expected string) bool {
	for start := 0; ; {
		index := strings.Index(message[start:], expected)
		if index < 0 {
			return false
		}
		index += start
		end := index + len(expected)
		if (index == 0 || !isDoltCapabilityIdentifierByte(message[index-1])) &&
			(end == len(message) || !isDoltCapabilityIdentifierByte(message[end])) {
			return true
		}
		start = index + 1
	}
}

func isDoltCapabilityIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_' || value == '.'
}

func collectDoltObjects(ctx context.Context, runner doltSQLRunner) ([]doltObject, error) {
	records, err := runDoltCSV(ctx, runner, "SHOW FULL TABLES")
	if err != nil {
		return nil, fmt.Errorf("SHOW FULL TABLES: %w", err)
	}
	if len(records.rows) == 0 {
		return []doltObject{}, nil
	}
	if len(records.columns) < 2 {
		return nil, errors.New("SHOW FULL TABLES returned fewer than two columns")
	}
	objects := make([]doltObject, 0, len(records.rows))
	for _, row := range records.rows {
		name, kind := row[0], strings.ToUpper(row[1])
		var statement string
		switch kind {
		case "BASE TABLE":
			statement, err = showDoltCreate(ctx, runner, "TABLE", name)
		case "VIEW":
			statement, err = showDoltCreate(ctx, runner, "VIEW", name)
		default:
			return nil, fmt.Errorf("SHOW FULL TABLES returned unsupported type %q for %q", row[1], name)
		}
		if err != nil {
			return nil, err
		}
		objects = append(objects, doltObject{Name: name, Type: kind, Create: statement})
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].Name != objects[j].Name {
			return objects[i].Name < objects[j].Name
		}
		return objects[i].Type < objects[j].Type
	})
	return objects, nil
}

func showDoltCreate(ctx context.Context, runner doltSQLRunner, kind, name string) (string, error) {
	records, err := runDoltCSV(ctx, runner, "SHOW CREATE "+kind+" "+doltIdentifier(name))
	if err != nil {
		return "", fmt.Errorf("SHOW CREATE %s %q: %w", strings.ToLower(kind), name, err)
	}
	if len(records.rows) != 1 || len(records.rows[0]) < 2 {
		return "", fmt.Errorf("SHOW CREATE %s %q returned an unexpected result", strings.ToLower(kind), name)
	}
	return records.rows[0][1], nil
}

func collectDoltCatalog(ctx context.Context, runner doltSQLRunner, name, query string) (doltCatalogSnapshot, error) {
	records, err := runDoltCSV(ctx, runner, query)
	if err != nil {
		return doltCatalogSnapshot{}, err
	}
	if len(records.columns)%2 != 0 {
		return doltCatalogSnapshot{}, fmt.Errorf("catalog %q returned unexpected columns", name)
	}
	columns := make([]string, len(records.columns)/2)
	for i := range columns {
		if got, want := records.columns[i*2], fmt.Sprintf("c%03d_null", i); got != want {
			return doltCatalogSnapshot{}, fmt.Errorf("catalog %q returned unexpected null marker %q", name, got)
		}
		columns[i] = records.columns[i*2+1]
	}
	rows := make([][]doltValue, len(records.rows))
	for i, record := range records.rows {
		row := make([]doltValue, len(columns))
		for j := range columns {
			if record[j*2] != "0" && record[j*2] != "1" {
				return doltCatalogSnapshot{}, fmt.Errorf("catalog %q has invalid null marker %q", name, record[j*2])
			}
			row[j] = doltValue{Null: record[j*2] == "1", Value: record[j*2+1]}
		}
		rows[i] = row
	}
	sort.SliceStable(rows, func(i, j int) bool { return compareDoltRows(rows[i], rows[j]) < 0 })
	return doltCatalogSnapshot{Name: name, Columns: columns, Rows: rows}, nil
}

func collectDoltMigrationLedgers(ctx context.Context, runner doltSQLRunner, objects []doltObject, catalog []doltCatalogSnapshot) ([]doltMigrationLedger, error) {
	columnsByTable := doltColumnsByTable(catalog)
	ledgers := make([]doltMigrationLedger, 0)
	for _, object := range objects {
		if object.Type != "BASE TABLE" || !isMigrationLedger(object.Name) {
			continue
		}
		columns := columnsByTable[object.Name]
		if len(columns) == 0 {
			return nil, fmt.Errorf("migration ledger %q cannot be read: information_schema.columns is unavailable", object.Name)
		}
		ledger, err := collectDoltMigrationLedger(ctx, runner, object.Name, columns)
		if err != nil {
			return nil, err
		}
		ledgers = append(ledgers, ledger)
	}
	sort.Slice(ledgers, func(i, j int) bool { return ledgers[i].Table < ledgers[j].Table })
	return ledgers, nil
}

func doltColumnsByTable(catalog []doltCatalogSnapshot) map[string][]string {
	result := map[string][]string{}
	var columns *doltCatalogSnapshot
	for i := range catalog {
		if catalog[i].Name == "information_schema.columns" {
			columns = &catalog[i]
			break
		}
	}
	if columns == nil {
		return result
	}
	positions := columnPositions(columns.Columns)
	tableName, tableOK := positions["table_name"]
	columnName, columnOK := positions["column_name"]
	ordinal, ordinalOK := positions["ordinal_position"]
	if !tableOK || !columnOK || !ordinalOK {
		return result
	}
	type entry struct {
		name    string
		ordinal int
	}
	byTable := map[string][]entry{}
	for _, row := range columns.Rows {
		if len(row) <= max(tableName, max(columnName, ordinal)) || row[tableName].Null || row[columnName].Null {
			continue
		}
		position, err := strconv.Atoi(row[ordinal].Value)
		if err != nil {
			continue
		}
		byTable[row[tableName].Value] = append(byTable[row[tableName].Value], entry{name: row[columnName].Value, ordinal: position})
	}
	for table, entries := range byTable {
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].ordinal != entries[j].ordinal {
				return entries[i].ordinal < entries[j].ordinal
			}
			return entries[i].name < entries[j].name
		})
		for _, entry := range entries {
			result[table] = append(result[table], entry.name)
		}
	}
	return result
}

func collectDoltMigrationLedger(ctx context.Context, runner doltSQLRunner, table string, columns []string) (doltMigrationLedger, error) {
	selects := make([]string, 0, len(columns)*2)
	for i, column := range columns {
		quoted := doltIdentifier(column)
		prefix := fmt.Sprintf("c%03d", i)
		selects = append(selects,
			"CASE WHEN "+quoted+" IS NULL THEN 1 ELSE 0 END AS "+doltIdentifier(prefix+"_null"),
			quoted+" AS "+doltIdentifier(prefix+"_value"))
	}
	records, err := runDoltCSV(ctx, runner, "SELECT "+strings.Join(selects, ", ")+" FROM "+doltIdentifier(table))
	if err != nil {
		return doltMigrationLedger{}, fmt.Errorf("read migration ledger %q: %w", table, err)
	}
	if len(records.columns) != len(columns)*2 {
		return doltMigrationLedger{}, fmt.Errorf("migration ledger %q returned unexpected columns", table)
	}
	rows := make([][]doltValue, len(records.rows))
	for i, record := range records.rows {
		row := make([]doltValue, len(columns))
		for j := range columns {
			if record[j*2] != "0" && record[j*2] != "1" {
				return doltMigrationLedger{}, fmt.Errorf("migration ledger %q has invalid null marker %q", table, record[j*2])
			}
			null := record[j*2] == "1"
			value := record[j*2+1]
			if !null && strings.EqualFold(columns[j], "applied_at") {
				value = "<applied>"
			}
			row[j] = doltValue{Null: null, Value: value}
		}
		rows[i] = row
	}
	sort.SliceStable(rows, func(i, j int) bool { return compareDoltRows(rows[i], rows[j]) < 0 })
	return doltMigrationLedger{Table: table, Columns: append([]string(nil), columns...), Rows: rows}, nil
}

type doltCSVRecords struct {
	columns []string
	rows    [][]string
}

func runDoltCSV(ctx context.Context, runner doltSQLRunner, query string) (doltCSVRecords, error) {
	raw, err := runner.SQLCSV(ctx, query)
	if err != nil {
		return doltCSVRecords{}, err
	}
	reader := csv.NewReader(strings.NewReader(string(raw)))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return doltCSVRecords{}, fmt.Errorf("parse CSV: %w", err)
	}
	if len(records) == 0 {
		return doltCSVRecords{}, errors.New("CSV has no header")
	}
	columns := make([]string, len(records[0]))
	for i, column := range records[0] {
		columns[i] = strings.ToLower(strings.TrimSpace(column))
	}
	if len(columns) == 0 {
		return doltCSVRecords{}, errors.New("CSV has no columns")
	}
	seen := make(map[string]bool, len(columns))
	for _, column := range columns {
		if column == "" || seen[column] {
			return doltCSVRecords{}, fmt.Errorf("CSV has invalid column %q", column)
		}
		seen[column] = true
	}
	rows := make([][]string, len(records)-1)
	for i, record := range records[1:] {
		if len(record) != len(columns) {
			return doltCSVRecords{}, fmt.Errorf("CSV row %d has %d columns, want %d", i+1, len(record), len(columns))
		}
		rows[i] = append([]string(nil), record...)
	}
	return doltCSVRecords{columns: columns, rows: rows}, nil
}

func doltSnapshotSorted(snapshots []doltCatalogSnapshot) bool {
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i-1].Name >= snapshots[i].Name {
			return false
		}
	}
	for _, snapshot := range snapshots {
		for i := 1; i < len(snapshot.Rows); i++ {
			if compareDoltRows(snapshot.Rows[i-1], snapshot.Rows[i]) > 0 {
				return false
			}
		}
	}
	return true
}

func compareDoltRows(left, right []doltValue) int {
	count := min(len(left), len(right))
	for i := 0; i < count; i++ {
		leftValue := strconv.FormatBool(left[i].Null) + "\x00" + left[i].Value
		rightValue := strconv.FormatBool(right[i].Null) + "\x00" + right[i].Value
		if compared := strings.Compare(leftValue, rightValue); compared != 0 {
			return compared
		}
	}
	return len(left) - len(right)
}

func columnPositions(columns []string) map[string]int {
	positions := make(map[string]int, len(columns))
	for i, column := range columns {
		positions[strings.ToLower(column)] = i
	}
	return positions
}

func doltIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}
