package ownershiphandoffv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql" // also registers the "mysql" driver

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// probeTimeout bounds every network observation. A gate that cannot answer
// inside it is an unreachable endpoint, not a slow one: the caller can retry a
// verb for free, and a verb that blocks is worse than one that refuses.
const probeTimeout = 5 * time.Second

// livenessSamples and livenessInterval implement gate (a): a single refused
// connect is not a stopped server, because a server restarting, a listen
// backlog draining, or a scheduler hiccup all produce one. Three samples over
// at least two seconds is the contract's floor.
const (
	livenessSamples  = 3
	livenessInterval = 750 * time.Millisecond
)

// captureBirth and verifyBirth are procid behind a seam. Production always uses
// procid; a test substitutes a platform that has no process-birth identity, so
// the "unavailable" path is exercised on a box that does have one. procid's own
// ErrUnsupported is build-tagged out of the platforms that support it, so there
// is no portable sentinel to compare against: anything other than a definite
// "that process is gone" is treated as an observation failure, which is the
// conservative reading either way.
var (
	captureBirth = procid.Capture
	verifyBirth  = procid.Verify
	// resolvePortHolder is the port-holder lookup behind a seam, so a test can
	// present the undetermined outcome on a box whose /proc works fine.
	resolvePortHolder = doltserver.ResolvePortHolderInDir
)

// reasonPortHolderUndetermined is the recorded reason for an instance bd could
// not resolve because the lookup itself did not run. It is deliberately a
// distinct string from "nothing was listening": one downgrades a gate to
// unavailable, the other is a fact about the world.
const reasonPortHolderUndetermined = "port_holder_undetermined"

// openScope dials a Dolt server and selects database. The caller's server and
// bd's own are both reached the same way; a Dolt sql-server started by either
// accepts the root user with no password on loopback.
func openScope(host string, port int, database string) (*sql.DB, error) {
	dsn := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     "root",
		Database: database,
		Timeout:  probeTimeout,
	}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// handshakes reports whether something at host:port speaks the MySQL protocol.
// It is deliberately weaker than openScope: gate (a) asks only whether a server
// is there, and a server that greets but rejects credentials is still there.
func handshakes(host string, port int) bool {
	greeted, err := doltserver.ProbeSQLServer("tcp", net.JoinHostPort(host, strconv.Itoa(port)), probeTimeout)
	return err == nil && greeted
}

// readSentinels captures the cheap facts that prove two servers are serving the
// same scope: the lowest issue id, the lowest dependency edge, and the commit
// the branch head points at. Absence is itself a sentinel — an empty database
// must compare equal to an empty database — so a missing table and an empty one
// are recorded as states rather than raised as errors.
func readSentinels(ctx context.Context, db *sql.DB, database string) (Sentinels, error) {
	var s Sentinels
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&s.DatabaseSelection); err != nil {
		return s, fmt.Errorf("select database(): %w", err)
	}
	if !strings.EqualFold(s.DatabaseSelection, database) {
		return s, fmt.Errorf("server selected database %q, not %q", s.DatabaseSelection, database)
	}

	var issueID sql.NullString
	var err error
	s.IssuesState, err = scanSentinel(ctx, db, "SELECT id FROM issues ORDER BY id LIMIT 1", &issueID)
	if err != nil {
		return s, fmt.Errorf("read issue sentinel: %w", err)
	}
	s.FirstIssue = issueID.String

	// `dependencies` is issue_id plus a split target: depends_on_issue_id,
	// depends_on_wisp_id or depends_on_external, exactly one of which is set
	// (migration 0002, split by 0041; the old depends_on_id went in 0044/0045).
	// COALESCE over the three is the edge, whichever kind it is.
	var edge sql.NullString
	s.DependenciesState, err = scanSentinel(ctx, db,
		"SELECT CONCAT(issue_id, '->', "+
			"COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external, '')) "+
			"FROM dependencies "+
			"ORDER BY issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external LIMIT 1", &edge)
	if err != nil {
		return s, fmt.Errorf("read dependency sentinel: %w", err)
	}
	s.FirstDependency = edge.String

	if err := db.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&s.HeadHash); err != nil {
		return s, fmt.Errorf("read head hash: %w", err)
	}
	return s, nil
}

// scanSentinel runs a one-row query and classifies the outcome. A table that is
// not there is a fact about the database, not a failure to read it: an empty
// workspace and one whose schema was never created must not look alike.
func scanSentinel(ctx context.Context, db *sql.DB, query string, dest *sql.NullString) (string, error) {
	err := db.QueryRowContext(ctx, query).Scan(dest)
	switch {
	case err == nil:
		return TablePresent, nil
	case errors.Is(err, sql.ErrNoRows):
		return TableEmpty, nil
	case isMissingTable(err):
		return TableMissing, nil
	case isMissingColumn(err):
		// The table is there but not in the shape this sentinel reads — an
		// older workspace, or a schema this bd is newer than. That is a fact
		// about the database, and both ends of the transfer read the SAME
		// database, so both record it identically and the comparison still
		// works. Killing the transfer over it would refuse to hand off a
		// workspace whose data is fine; the head hash still carries the proof.
		return TableUnreadable, nil
	default:
		return "", err
	}
}

// isMissingTable recognizes MySQL error 1146 (ER_NO_SUCH_TABLE), which Dolt
// returns for a database that has no beads schema.
func isMissingTable(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1146
	}
	return false
}

// isMissingColumn recognizes a column this sentinel names that the table does
// not have. MySQL says 1054 (ER_BAD_FIELD_ERROR); Dolt reports the same
// condition as a generic 1105 whose text names the column, so both are matched.
func isMissingColumn(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	if mysqlErr.Number == 1054 {
		return true
	}
	return mysqlErr.Number == 1105 && strings.Contains(mysqlErr.Message, "could not be found")
}

// sentinelsEqual compares two captures. The database selection is excluded on
// purpose: it is checked against the request separately at each end, and
// comparing it here would only restate that.
func sentinelsEqual(a, b Sentinels) (string, bool) {
	switch {
	case a.IssuesState != b.IssuesState || a.FirstIssue != b.FirstIssue:
		return fmt.Sprintf("issues %s/%q vs %s/%q", a.IssuesState, a.FirstIssue, b.IssuesState, b.FirstIssue), false
	case a.DependenciesState != b.DependenciesState || a.FirstDependency != b.FirstDependency:
		return fmt.Sprintf("dependencies %s/%q vs %s/%q",
			a.DependenciesState, a.FirstDependency, b.DependenciesState, b.FirstDependency), false
	case a.HeadHash != b.HeadHash:
		return fmt.Sprintf("head hash %q vs %q", a.HeadHash, b.HeadHash), false
	}
	return "", true
}

// captureInstance resolves the process behind an endpoint and binds it to this
// workspace, then takes its birth identity. This is §3: an observation, not a
// hint. Every failure is recorded as a reason rather than raised, because an
// unresolved instance is a legitimate state — it downgrades a later gate to
// unavailable instead of failing the transfer.
func captureInstance(endpoint Endpoint, dataDir string) Instance {
	now := time.Now().UTC()
	if doltserver.PortHolderSource() == "" {
		return Instance{Resolved: false, Reason: reasonPortHolderUndetermined, ObservedAt: now}
	}
	pid, boundBy, outcome := resolvePortHolder(endpoint.Port, dataDir)
	switch outcome {
	case doltserver.PortHolderUndetermined:
		// Distinct from a lookup that ran and found nothing: bd did not get to
		// look, so the disproof gate that would have rested on this identity is
		// recorded unavailable rather than evaluated.
		return Instance{
			Resolved:   false,
			Source:     doltserver.PortHolderSource(),
			Reason:     reasonPortHolderUndetermined,
			ObservedAt: now,
		}
	case doltserver.PortHolderNoHolder:
		return Instance{
			Resolved:   false,
			Source:     doltserver.PortHolderSource(),
			Reason:     fmt.Sprintf("no process holding port %d could be bound to %s", endpoint.Port, dataDir),
			ObservedAt: now,
		}
	}
	birth, err := captureBirth(pid)
	if err != nil {
		return Instance{
			Resolved:   false,
			PID:        pid,
			BoundBy:    boundBy,
			Source:     doltserver.PortHolderSource(),
			Reason:     fmt.Sprintf("capture birth identity of pid %d: %v", pid, err),
			ObservedAt: now,
		}
	}
	return Instance{
		Resolved:   true,
		PID:        pid,
		Birth:      string(birth),
		BoundBy:    boundBy,
		Source:     doltserver.PortHolderSource(),
		ObservedAt: now,
	}
}

// instanceGone reports whether a captured instance is positively disproved:
// the process is dead, or the pid now belongs to something else. ok false means
// the question could not be answered here, which the caller records as
// unavailable — never as a disproof.
func instanceGone(inst Instance) (gone bool, ok bool, detail string) {
	if !inst.Resolved || inst.PID <= 0 || inst.Birth == "" {
		return false, false, "no legacy instance was resolved at prepare"
	}
	match, err := verifyBirth(inst.PID, procid.Token(inst.Birth))
	if err != nil {
		if procid.IsProcessGone(err) {
			return true, true, fmt.Sprintf("pid %d is gone", inst.PID)
		}
		return false, false, fmt.Sprintf("verify pid %d: %v", inst.PID, err)
	}
	if match {
		return false, true, fmt.Sprintf("pid %d is still the process captured at prepare", inst.PID)
	}
	return true, true, fmt.Sprintf("pid %d no longer matches its captured birth", inst.PID)
}

// endpointQuiet samples the endpoint repeatedly and reports whether every
// sample refused or failed to greet. One live sample is enough to say alive.
func endpointQuiet(endpoint Endpoint) (quiet bool, detail string) {
	for i := 0; i < livenessSamples; i++ {
		if i > 0 {
			time.Sleep(livenessInterval)
		}
		if handshakes(endpoint.Host, endpoint.Port) {
			return false, fmt.Sprintf("sample %d of %d got a MySQL handshake", i+1, livenessSamples)
		}
	}
	return true, fmt.Sprintf("%d samples over %s, none greeted", livenessSamples,
		time.Duration(livenessSamples-1)*livenessInterval)
}

// dataDirLocked reports whether any Dolt storage under dir is still held. It
// test-acquires each noms LOCK non-blocking and releases it immediately: a lock
// bd can take is a lock nobody else holds, which is the only way to ask this
// without trusting a process list.
//
// ok false means the question could not be answered — an unreadable tree, not
// an unlocked one.
func dataDirLocked(dir string) (locked bool, ok bool, detail string) {
	var held []string
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that vanished mid-walk cannot be holding a lock.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() != "LOCK" || filepath.Base(filepath.Dir(path)) != "noms" {
			return nil
		}
		f, openErr := os.OpenFile(path, os.O_RDWR, 0o600) //nolint:gosec // walked path under the workspace data dir
		if openErr != nil {
			return fmt.Errorf("open %s: %w", path, openErr)
		}
		lockErr := lockfile.FlockExclusiveNonBlocking(f)
		if lockErr == nil {
			_ = lockfile.FlockUnlock(f)
			_ = f.Close()
			return nil
		}
		_ = f.Close()
		if lockfile.IsLocked(lockErr) {
			held = append(held, path)
			return nil
		}
		return fmt.Errorf("probe %s: %w", path, lockErr)
	})
	if walkErr != nil {
		return false, false, walkErr.Error()
	}
	if len(held) > 0 {
		return true, true, "held: " + strings.Join(held, ", ")
	}
	return false, true, "no noms LOCK under the data dir is held"
}

// bdServerPresent reports whether bd already manages a live server for this
// workspace. It is the one question ManagesLiveServerOnPort is used for: is
// that server ours? Both the recorded port and a caller-named port are checked,
// because a stale record pointing elsewhere still means bd holds this root.
// It deliberately does not use doltserver.IsRunning, which shells out to `ps`
// to confirm the process is a dolt binary. An ownership handoff spawns nothing
// but dolt, and ManagesLiveServerOnPort answers the same question from the
// recorded port and pid alone.
func bdServerPresent(beadsDir string, ports ...int) (present bool, detail string) {
	// The recorded port first: that is the server bd believes it owns here.
	if recorded := recordedPort(beadsDir); recorded > 0 {
		ports = append([]int{recorded}, ports...)
	}
	for _, port := range ports {
		if port > 0 && doltserver.ManagesLiveServerOnPort(beadsDir, port) {
			return true, fmt.Sprintf("bd manages a live server on port %d for this root", port)
		}
	}
	return false, "no live bd-managed server for this root"
}

// recordedPort reads the port bd last recorded for this workspace, or 0.
func recordedPort(beadsDir string) int {
	data, err := os.ReadFile(filepath.Join(beadsDir, doltserver.PortFileName)) //nolint:gosec // workspace lifecycle file
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return port
}

// isDoltRoot is beads' own predicate for "this directory is a Dolt database
// root": the .dolt subtree is present.
func isDoltRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".dolt"))
	return err == nil && info.IsDir()
}

// freeLoopbackPort asks the OS for an unused port on host and gives it straight
// back. There is an unavoidable race between closing this listener and the
// replacement binding it, which is why the strict launch refuses rather than
// adopts when it finds the port already held.
func freeLoopbackPort(host string) (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		return 0, err
	}
	return port, nil
}
