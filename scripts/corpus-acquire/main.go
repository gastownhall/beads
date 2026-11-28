// corpus-acquire takes a filesystem-level, point-in-time copy of a named
// production corpus (my_db | gascity) out of the shared multi-database Dolt
// data_dir, plus depth measurement, for the downstream replay-with-oracle
// harness (be-hs42e.5.2) to consume.
//
// It never invokes git, never opens a write-capable (or any) MySQL-protocol
// connection to the shared Dolt server, even while that server is serving
// the source, and never creates a scratch database on that server — see
// be-hs42e.5.1's exit_contract and corpusacquire_test.go for the full
// acceptance criteria this satisfies.
//
// Usage:
//
//	go run ./scripts/corpus-acquire -data-dir=<dir> -db=my_db -dest=<dir>
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CorpusClone is the CORPUS_CLONE-shaped output record be-hs42e.5.2 (the
// downstream replay-with-oracle harness) consumes.
type CorpusClone struct {
	SourceDB           string    `json:"source_db"`
	SourceServer       string    `json:"source_server"`
	SnapshotCommitHash string    `json:"snapshot_commit_hash"`
	IssueCount         int       `json:"issue_count"`
	DoltLogCommitCount int       `json:"dolt_log_commit_count"`
	OldestCommitAt     time.Time `json:"oldest_commit_at"`
	NewestCommitAt     time.Time `json:"newest_commit_at"`
	Partial            bool      `json:"partial"`
	PartialReason      string    `json:"partial_reason,omitempty"`
}

func main() {
	dataDir := flag.String("data-dir", "", "Dolt shared server data_dir containing the source database (required)")
	db := flag.String("db", "", "source database name, e.g. my_db or gascity (required)")
	dest := flag.String("dest", "", "destination directory for the acquired clone (default: minted under -out-root)")
	outRoot := flag.String("out-root", "", "parent directory to mint a destination name under, when -dest is not given")
	sourceServer := flag.String("source-server", "127.0.0.1:28231", "address of the source Dolt server, recorded for provenance only — never connected to")
	baselineDepth := flag.Int("baseline-depth", 0, "prior dolt_log commit-count baseline; 0 disables the partial-coverage check")
	flag.Parse()

	if *dataDir == "" || *db == "" {
		fmt.Fprintln(os.Stderr, "usage: corpus-acquire -data-dir=<dir> -db=<name> [-dest=<dir>] [-out-root=<dir>] [-source-server=host:port] [-baseline-depth=N]")
		os.Exit(2)
	}

	destDir := *dest
	if destDir == "" {
		if *outRoot == "" {
			fmt.Fprintln(os.Stderr, "corpus-acquire: one of -dest or -out-root is required")
			os.Exit(2)
		}
		destDir = filepath.Join(*outRoot, GenerateCloneID(*db, time.Now()))
	}
	if err := ValidateMintedName(filepath.Base(destDir)); err != nil {
		fmt.Fprintln(os.Stderr, "corpus-acquire:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	if err := AcquireCorpus(ctx, *dataDir, *db, destDir); err != nil {
		fmt.Fprintln(os.Stderr, "corpus-acquire:", err)
		os.Exit(1)
	}

	result, err := MeasureCorpus(ctx, destDir, *db, *sourceServer, *baselineDepth)
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus-acquire:", err)
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "corpus-acquire: encoding result:", err)
		os.Exit(1)
	}
}

// AcquireCorpus copies the database dataDir/dbName into destDir at the
// filesystem level, then verifies the copy opens with a read-only query
// against the copy itself.
//
// It never runs dolt against the source. A dolt sql-server serving dataDir
// records itself in <dataDir>/.dolt/sql-server.info, and the dolt CLI then
// routes every command for a database under that data dir, --data-dir=<source>
// included, through the server over MySQL protocol instead of opening the
// files. That is the shared-server deployment this tool is for, so the only
// way to take a clone without touching the server is to read the files. The
// copy lives outside the served data dir and carries no sql-server.info, so
// the verification query opens it in-process; a destination under a served
// data dir is refused for the same reason.
//
// The server may be writing while the copy runs. The manifest is copied
// first and the chunk journal last (see doltCopyRank), so the journal copied
// covers everything the manifest and journal index copied before it refer
// to. A source garbage-collected mid-copy can still leave an unusable copy;
// the verification query reports that, and a re-run takes a fresh copy.
func AcquireCorpus(ctx context.Context, dataDir, dbName, destDir string) error {
	sourceDir := filepath.Join(dataDir, dbName)
	if !isDoltRepo(sourceDir) {
		return fmt.Errorf("corpus-acquire: no dolt database %q found under %s", dbName, dataDir)
	}
	if _, err := os.Lstat(destDir); err == nil {
		return fmt.Errorf("corpus-acquire: destination %s already exists", destDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("corpus-acquire: checking destination %s: %w", destDir, err)
	}
	destParent := filepath.Dir(destDir)
	if served := servedDataDir(destParent); served != "" {
		return fmt.Errorf("corpus-acquire: destination %s is under %s, which a dolt sql-server is serving; dolt would route every query on the copy through that server, so choose a destination outside it", destDir, served)
	}
	if err := os.MkdirAll(destParent, 0o755); err != nil {
		return fmt.Errorf("corpus-acquire: creating destination parent: %w", err)
	}

	stagingDir, err := os.MkdirTemp(destParent, "."+filepath.Base(destDir)+".partial-")
	if err != nil {
		return fmt.Errorf("corpus-acquire: creating staging dir: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	if err := copyDoltTree(filepath.Join(sourceDir, ".dolt"), filepath.Join(stagingDir, ".dolt")); err != nil {
		return fmt.Errorf("corpus-acquire: copying %s: %w", sourceDir, err)
	}
	if _, err := doltSQLRow(ctx, stagingDir, "SELECT COUNT(*) FROM dolt_log"); err != nil {
		return fmt.Errorf("corpus-acquire: the copy of %s does not open (if the source was garbage-collected mid-copy, re-run): %w", sourceDir, err)
	}
	if err := os.Rename(stagingDir, destDir); err != nil {
		return fmt.Errorf("corpus-acquire: moving the copy into place: %w", err)
	}
	renamed = true
	return nil
}

// servedDataDir returns dir or its nearest ancestor that a dolt sql-server is
// serving, identified by the <dir>/.dolt/sql-server.info record the server
// writes at startup, or "" when there is none. The dolt CLI routes commands
// for any database under such a directory through that server.
func servedDataDir(dir string) string {
	dir = filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".dolt", "sql-server.info")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// chunkJournalName is the fixed file name of Dolt's chunk journal under
// .dolt/noms.
const chunkJournalName = "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv"

// doltCopyRank orders the files of a live copy: the manifest first, the
// journal index next to last, and the append-only chunk journal last, so the
// journal copied is at least as long as anything copied before it refers to.
func doltCopyRank(name string) int {
	switch name {
	case "manifest":
		return 0
	case "journal.idx":
		return 2
	case chunkJournalName:
		return 3
	default:
		return 1
	}
}

// skipDoltFile reports whether a file under .dolt stays behind in a copy:
// lock files belong to whichever process holds the source open, and a
// sql-server.info record in the copy would make dolt route queries on the
// copy to the server that wrote it.
func skipDoltFile(name string) bool {
	return name == "LOCK" || name == "sql-server.info" || name == "sql-server.lock"
}

// copyDoltTree copies the regular files under src, a database's .dolt
// directory, to dst in doltCopyRank order, recreating the directory layout
// and leaving out the files skipDoltFile names. It only reads src.
func copyDoltTree(src, dst string) error {
	type file struct {
		rel  string
		rank int
	}
	var files []file
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if skipDoltFile(d.Name()) {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unexpected non-regular file %s", path)
		}
		files = append(files, file{rel: rel, rank: doltCopyRank(d.Name())})
		return nil
	})
	if err != nil {
		return err
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].rank < files[j].rank })
	for _, f := range files {
		if err := copyFile(filepath.Join(src, f.rel), filepath.Join(dst, f.rel)); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 -- a file found walking the operator-named source .dolt tree
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- inside the staging dir this tool created
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copying %s: %w", src, err)
	}
	return out.Close()
}

// MeasureCorpus re-measures the local clone at cloneDir on every call (never
// cached): its HEAD commit hash, issue count, and dolt_log depth and date
// range, all via read-only SQL queries against the local clone only — never
// the sourceServer, which is recorded on the result purely for provenance
// and is never connected to. If priorBaselineDepth is positive and the
// clone's actual depth falls below it, the result is flagged Partial rather
// than silently reporting narrower coverage as if it were complete. A clone
// under a data dir a dolt sql-server is serving is refused: dolt would route
// these queries through that server.
func MeasureCorpus(ctx context.Context, cloneDir, sourceDB, sourceServer string, priorBaselineDepth int) (CorpusClone, error) {
	cc := CorpusClone{
		SourceDB:     sourceDB,
		SourceServer: sourceServer,
	}
	if served := servedDataDir(cloneDir); served != "" {
		return cc, fmt.Errorf("corpus-acquire: clone %s is under %s, which a dolt sql-server is serving; measuring it would query that server", cloneDir, served)
	}

	headRow, err := doltSQLRow(ctx, cloneDir, "SELECT commit_hash FROM dolt_log LIMIT 1")
	if err != nil {
		return cc, fmt.Errorf("corpus-acquire: measuring snapshot commit hash: %w", err)
	}
	cc.SnapshotCommitHash = strings.TrimSpace(headRow[0])

	issueRow, err := doltSQLRow(ctx, cloneDir, "SELECT COUNT(*) FROM issues")
	if err != nil {
		return cc, fmt.Errorf("corpus-acquire: measuring issue count: %w", err)
	}
	issueCount, err := strconv.Atoi(strings.TrimSpace(issueRow[0]))
	if err != nil {
		return cc, fmt.Errorf("corpus-acquire: parsing issue count %q: %w", issueRow[0], err)
	}
	cc.IssueCount = issueCount

	depthRow, err := doltSQLRow(ctx, cloneDir, "SELECT COUNT(*), MIN(UNIX_TIMESTAMP(date)), MAX(UNIX_TIMESTAMP(date)) FROM dolt_log")
	if err != nil {
		return cc, fmt.Errorf("corpus-acquire: measuring commit depth: %w", err)
	}
	if len(depthRow) < 3 {
		return cc, fmt.Errorf("corpus-acquire: unexpected dolt_log depth query result: %v", depthRow)
	}
	commitCount, err := strconv.Atoi(strings.TrimSpace(depthRow[0]))
	if err != nil {
		return cc, fmt.Errorf("corpus-acquire: parsing dolt_log commit count %q: %w", depthRow[0], err)
	}
	cc.DoltLogCommitCount = commitCount

	oldestSec, err := strconv.ParseFloat(strings.TrimSpace(depthRow[1]), 64)
	if err != nil {
		return cc, fmt.Errorf("corpus-acquire: parsing oldest commit timestamp %q: %w", depthRow[1], err)
	}
	newestSec, err := strconv.ParseFloat(strings.TrimSpace(depthRow[2]), 64)
	if err != nil {
		return cc, fmt.Errorf("corpus-acquire: parsing newest commit timestamp %q: %w", depthRow[2], err)
	}
	cc.OldestCommitAt = time.Unix(int64(oldestSec), 0).UTC()
	cc.NewestCommitAt = time.Unix(int64(newestSec), 0).UTC()

	if priorBaselineDepth > 0 && commitCount < priorBaselineDepth {
		cc.Partial = true
		cc.PartialReason = fmt.Sprintf("dolt_log_commit_count=%d is below the supplied baseline depth=%d", commitCount, priorBaselineDepth)
	}

	return cc, nil
}

var mintedNameDangerRE = regexp.MustCompile(`^[0-9a-v]{16,31}$`)

// ValidateMintedName rejects any name this tool would use for a directory
// or branch that matches the shape of a truncated Dolt/Noms content hash:
// 16-31 characters entirely within the base32-style [0-9a-v] alphabet. Real
// Dolt commit hashes are always exactly 32 characters in this alphabet, so
// a 32-character match is deliberately allowed — the danger zone is names
// that merely *look* hash-shaped by coincidence at a shorter length, which
// can confuse downstream ref resolution (NFR5/R2, the gap in upstream
// issueops.ValidateRef tracked on PR #6730). Ordinary English compound
// words can innocently fall in this trap: "productionmirror",
// "releasecandidate", and "integrationtests" are all real 16-character
// examples entirely within [0-9a-v].
func ValidateMintedName(name string) error {
	if mintedNameDangerRE.MatchString(name) {
		return fmt.Errorf("corpus-acquire: name %q matches the dangerous truncated-hash shape ^[0-9a-v]{16,31}$ (NFR5/R2) — choose a name with a hyphen, underscore, uppercase letter, or a length outside 16-31", name)
	}
	return nil
}

// GenerateCloneID mints a destination identifier for dbName that is safe
// under ValidateMintedName by construction: the literal hyphens in
// "corpus-<db>-<timestamp>" fall outside the [0-9a-v] character class, so
// the full string can never match the dangerous shape regardless of dbName
// or when it is called.
func GenerateCloneID(dbName string, now time.Time) string {
	return fmt.Sprintf("corpus-%s-%s", dbName, now.UTC().Format("20060102-150405"))
}

// sanitizedEnv returns base with every environment variable this tool must
// never blindly trust stripped out (mirrors scripts/ci/pr-core.sh's
// pattern): the ambient Dolt server address/port, actor identity, and city
// root, none of which this tool's own filesystem-level operations need, and
// all of which have caused a real incident when leaked into a child
// process (cairn beads-testdb-production-leak: ambient
// BEADS_DOLT_SERVER_PORT fail-open created a scratch database directly on
// the shared production server).
func sanitizedEnv(base []string) []string {
	strip := map[string]bool{
		"BEADS_DOLT_SERVER_PORT": true,
		"BEADS_DOLT_PORT":        true,
		"BEADS_ACTOR":            true,
		"BD_ACTOR":               true,
		"GT_ROOT":                true,
	}
	out := make([]string, 0, len(base))
	for _, kv := range base {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if strip[name] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// isDoltRepo reports whether dir looks like a dolt database directory.
// AcquireCorpus uses this to fail cleanly on a missing source without ever
// creating one — this tool only ever reads.
func isDoltRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".dolt"))
	return err == nil && info.IsDir()
}

// doltRun runs the dolt CLI (resolved via PATH at call time, like any other
// exec.Command — this is also what lets a test's PATH-prepended shim
// intercept it) with dir as its working directory and a sanitized child
// environment. It never invokes git and never receives a non-file:// URL or
// server address from any caller in this package.
func doltRun(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "dolt", args...)
	cmd.Dir = dir
	cmd.Env = sanitizedEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("dolt %s (dir=%s): %w\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out, nil
}

// doltSQLRow runs a single-row, read-only SQL query against the dolt
// database at dir — the local clone only, never a network or server
// connection — and returns its columns as strings.
func doltSQLRow(ctx context.Context, dir, query string) ([]string, error) {
	out, err := doltRun(ctx, dir, "sql", "-q", query, "-r", "csv")
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(bytes.NewReader(out))
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("corpus-acquire: parsing CSV from query %q: %w", query, err)
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("corpus-acquire: query %q returned no data row", query)
	}
	return rows[1], nil
}
