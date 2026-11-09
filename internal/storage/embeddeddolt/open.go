//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	doltembed "github.com/dolthub/driver/v2"
)

// validIdentifier matches safe SQL identifiers (letters, digits, underscores).
// Hyphens are excluded because database names are interpolated into system
// variable identifiers (@@<db>_head_ref) where hyphens are invalid.
var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

const (
	commitName  = "beads"
	commitEmail = "beads@local"
)

// openWaitBudgetEnv names the environment variable that bounds how long
// OpenSQL keeps retrying an open the driver reports as retryable (another
// process or handle holding the database) when the caller set no deadline.
const openWaitBudgetEnv = "BEADS_EMBEDDED_OPEN_TIMEOUT"

// defaultOpenWaitBudget is the retry budget when openWaitBudgetEnv is unset
// or invalid.
const defaultOpenWaitBudget = 30 * time.Second

// openWaitBudget returns the retry budget configured in openWaitBudgetEnv,
// read on every call.
func openWaitBudget() time.Duration {
	d, err := time.ParseDuration(os.Getenv(openWaitBudgetEnv))
	if err != nil || d <= 0 {
		return defaultOpenWaitBudget
	}
	return d
}

// openWaitBackOff records whether the policy it wraps gave up (NextBackOff
// returned Stop) since its last Reset. The driver owns the retries and
// decides which errors are retryable; this only observes the policy's own
// answer, so OpenSQL can tell an exhausted budget from any other failure.
type openWaitBackOff struct {
	backoff.BackOff
	exhausted atomic.Bool
}

func (b *openWaitBackOff) NextBackOff() time.Duration {
	next := b.BackOff.NextBackOff()
	if next == backoff.Stop {
		b.exhausted.Store(true)
	}
	return next
}

func (b *openWaitBackOff) Reset() {
	b.exhausted.Store(false)
	b.BackOff.Reset()
}

// classifyOpenErr marks err as ErrOpenTimeout when the retry policy ran out
// of budget and the caller had not canceled ctx; caller cancellation takes
// precedence, and every other error is returned unchanged.
func classifyOpenErr(ctx context.Context, wait *openWaitBackOff, dir string, budget time.Duration, err error) error {
	if ctx.Err() != nil || !wait.exhausted.Load() {
		return err
	}
	return fmt.Errorf("%w (budget %s) at %s: another process or open handle may hold the database; retry or increase %s: %w",
		ErrOpenTimeout, budget, dir, openWaitBudgetEnv, err)
}

// OpenSQL opens an embedded Dolt database at dir. The returned cleanup
// function closes both the *sql.DB and the underlying connector.
//
// The driver retries an open it reports as retryable, such as a database
// another process or handle holds, with an exponential backoff. The retries
// stop after the BEADS_EMBEDDED_OPEN_TIMEOUT budget (default 30s), and the
// open then fails with ErrOpenTimeout. The budget stops further retries; it
// never interrupts a running attempt.
func OpenSQL(ctx context.Context, dir, database, branch string) (*sql.DB, func() error, error) {
	dsn := buildDSN(dir, database)

	cfg, err := doltembed.ParseDSN(dsn)
	if err != nil {
		return nil, nil, err
	}

	budget := openWaitBudget()
	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = budget
	bo.MaxInterval = 5 * time.Second
	wait := &openWaitBackOff{BackOff: bo}
	cfg.BackOff = wait

	connector, err := doltembed.NewConnector(cfg)
	if err != nil {
		return nil, nil, err
	}

	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)

	cleanup := func() error {
		dbErr := db.Close()
		connErr := connector.Close()
		// connector.Close → engine.Close → BackgroundThreads.Shutdown
		// always returns context.Canceled because Shutdown cancels its
		// own parent context then returns parentCtx.Err().  This is
		// a spurious error from a clean shutdown; filter it from each
		// result individually so real close errors are still surfaced.
		if errors.Is(dbErr, context.Canceled) {
			dbErr = nil
		}
		if errors.Is(connErr, context.Canceled) {
			connErr = nil
		}
		return errors.Join(dbErr, connErr)
	}

	if err := db.PingContext(ctx); err != nil {
		return nil, nil, errors.Join(classifyOpenErr(ctx, wait, dir, budget, err), cleanup())
	}

	if strings.TrimSpace(database) != "" {
		if !validIdentifier.MatchString(database) {
			msg := fmt.Sprintf("invalid database name: %q", database)
			if strings.ContainsRune(database, '-') {
				msg += "; hyphens are not allowed in embedded mode — replace with underscores in .beads/metadata.json dolt_database field, or run 'bd doctor'"
			}
			return nil, nil, errors.Join(errors.New(msg), cleanup())
		}
		if _, err := db.ExecContext(ctx, "USE `"+database+"`"); err != nil {
			return nil, nil, errors.Join(err, cleanup())
		}
		if strings.TrimSpace(branch) != "" {
			if _, err := db.ExecContext(ctx, fmt.Sprintf("SET @@%s_head_ref = %s", database, sqlStringLiteral(branch))); err != nil {
				return nil, nil, errors.Join(err, cleanup())
			}
		}
	}

	return db, cleanup, nil
}

func buildDSN(dir, database string) string {
	v := url.Values{}
	v.Set(doltembed.CommitNameParam, commitName)
	v.Set(doltembed.CommitEmailParam, commitEmail)
	v.Set(doltembed.MultiStatementsParam, "true")
	if strings.TrimSpace(database) != "" {
		v.Set(doltembed.DatabaseParam, database)
	}
	// Build the DSN string manually instead of using url.URL.String(),
	// which percent-encodes the path (spaces → %20). The embedded Dolt
	// driver's ParseDataSource strips the "file://" prefix and uses the
	// remainder as a literal filesystem path, so encoding breaks paths
	// that contain spaces. See #2920.
	path := dir
	if os.PathSeparator == '\\' {
		path = strings.ReplaceAll(path, `\`, `/`)
	}
	return "file://" + path + "?" + v.Encode()
}

func sqlStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(strings.TrimSpace(s), "'", "''") + "'"
}
