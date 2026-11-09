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

	// openWaitBudgetDefault bounds an embedded open's retry loop when the
	// caller's context carries no deadline of its own. 30s is a policy
	// choice, not a measured optimum: long enough for ordinary
	// multi-process lock contention to clear, short enough that an
	// interactive command fails with a diagnosis instead of hanging
	// forever (PR #4284). BEADS_EMBEDDED_OPEN_TIMEOUT overrides it.
	openWaitBudgetDefault = 30 * time.Second

	// openWaitBudgetEnv names the environment variable that overrides
	// openWaitBudgetDefault (see openWaitBudget).
	openWaitBudgetEnv = "BEADS_EMBEDDED_OPEN_TIMEOUT"
)

// OpenSQL opens an embedded Dolt database at dir. The returned cleanup
// function closes both the *sql.DB and the underlying connector.
//
// The driver retries lock and connect waits on the caller's context. When
// that context has no deadline, the retries are bounded by the
// BEADS_EMBEDDED_OPEN_TIMEOUT budget (30s by default) and an open that
// gives up after the budget reports ErrOpenTimeout; with a deadline, the
// caller's existing policy stays authoritative and exhaustion is never
// diagnosed. The budget ends the retry loop: it never interrupts an
// attempt that is already running, so a single slow attempt can outlast
// it, and the full budget need not be waited before the policy stops.
func OpenSQL(ctx context.Context, dir, database, branch string) (*sql.DB, func() error, error) {
	dsn := buildDSN(dir, database)

	cfg, err := doltembed.ParseDSN(dsn)
	if err != nil {
		return nil, nil, err
	}

	budget := openWaitBudget()
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = 5 * time.Second
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		// The caller's deadline already bounds the retry loop; keep the
		// driver's wait-until-ctx-cancellation behavior for it.
		bo.MaxElapsedTime = 0
	} else {
		// No caller deadline: without a budget the driver waits forever on
		// lock contention. Bound the retries so a held database fails with
		// ErrOpenTimeout instead of hanging the command.
		bo.MaxElapsedTime = budget
	}
	policy := &exhaustedBackOff{BackOff: bo}
	cfg.BackOff = policy

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
		// The retry policy reported backoff.Stop on its own and the caller
		// did not cancel: diagnose the budget, not the driver's last
		// lock error. Everything else keeps the underlying error.
		if policy.gaveUpWithoutCancellation(ctx) {
			err = errors.Join(openBudgetExhaustedError(budget, dir), err)
		}
		return nil, nil, errors.Join(err, cleanup())
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

// exhaustedBackOff wraps the retry policy the driver consumes and records
// the one public result that matters here: NextBackOff reporting
// backoff.Stop, the policy giving up on its own. The driver calls Reset
// before use and NextBackOff between attempts, both on the open's
// goroutine; the flag is atomic because nothing in the driver's contract
// guarantees that. The driver owns retryability; this only observes the
// policy we supplied, never engine internals.
type exhaustedBackOff struct {
	backoff.BackOff
	exhausted atomic.Bool
}

// NextBackOff delegates to the wrapped policy and records its Stop.
func (b *exhaustedBackOff) NextBackOff() time.Duration {
	d := b.BackOff.NextBackOff()
	if d == backoff.Stop {
		b.exhausted.Store(true)
	}
	return d
}

// Reset clears the exhaustion record and delegates to the wrapped policy.
func (b *exhaustedBackOff) Reset() {
	b.exhausted.Store(false)
	b.BackOff.Reset()
}

// gaveUpWithoutCancellation reports whether a failed open should be
// diagnosed as ErrOpenTimeout: the policy gave up on its own and the
// caller did not cancel. Observed caller cancellation takes precedence
// over exhaustion — the context error is the truer cause.
func (b *exhaustedBackOff) gaveUpWithoutCancellation(ctx context.Context) bool {
	return b.exhausted.Load() && ctx.Err() == nil
}

// openBudgetExhaustedError is the ErrOpenTimeout diagnostic for an open
// that ran its retry budget dry without the caller canceling. It does
// not claim the budget was fully waited (the policy stops before an
// interval that would overshoot it) or that any specific process holds
// the database — only that the budget ended while the database could not
// be opened, and which knob raises it.
func openBudgetExhaustedError(budget time.Duration, dir string) error {
	return fmt.Errorf("%w (budget %s) at %s: another process or open handle may hold the database; retry or increase %s",
		ErrOpenTimeout, budget, dir, openWaitBudgetEnv)
}

// openWaitBudget resolves the embedded open retry budget from the
// environment on every open: valid values change per command, so nothing
// is cached. See parseOpenWaitBudget for the accepted forms.
func openWaitBudget() time.Duration {
	return parseOpenWaitBudget(os.Getenv(openWaitBudgetEnv))
}

// parseOpenWaitBudget parses the BEADS_EMBEDDED_OPEN_TIMEOUT setting,
// falling back to openWaitBudgetDefault when raw is unset, empty,
// unparsable, overflowing or non-positive. Valid time.ParseDuration
// strings (e.g. "2m", "500ms") or bare numbers treated as seconds
// (e.g. "90") are accepted — the same convention as internal/storage/
// dolt/store.go's parseTimeout.
func parseOpenWaitBudget(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return openWaitBudgetDefault
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d > 0 {
			return d
		}
		return openWaitBudgetDefault
	}
	if d, err := time.ParseDuration(raw + "s"); err == nil {
		if d > 0 {
			return d
		}
		return openWaitBudgetDefault
	}
	return openWaitBudgetDefault
}
