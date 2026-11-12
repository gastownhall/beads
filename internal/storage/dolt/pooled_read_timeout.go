package dolt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
)

// poolReadTimeoutFromDSN returns the per-I/O read deadline a pool opened on dsn
// uses, or zero when the DSN sets none or does not parse. buildServerDSN owns
// the precedence (built-in default, command fallback, explicit setting); this
// reads its result instead of repeating it.
func poolReadTimeoutFromDSN(dsn string) time.Duration {
	if dsn == "" {
		return 0
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		return 0
	}
	return parsed.ReadTimeout
}

// classifyPooledRead classifies one failed attempt of a pooled read helper
// (withReadTx, queryContext, queryRowContext) against the store's pool read
// deadline. start is when the attempt began: after the store lock and after
// any earlier attempt's backoff, so neither counts toward the attempt's time.
// op, when set, names the operation for an error whose own text does not.
func (s *DoltStore) classifyPooledRead(ctx context.Context, err error, start time.Time, op string) error {
	return classifyPooledReadFailure(ctx, err, time.Since(start), s.poolReadTimeout, op)
}

// classifyPooledReadFailure decides whether a failed pooled read may be sent
// again. It returns err unchanged unless the attempt looks like it hit the
// pool's per-I/O read deadline, and then a *storage.ReadTimeoutError, which
// the managed retry never replays (GH#5258: each replay of a read the server
// is still running parks another session there).
//
//   - Confirmed: the error still carries a network read timeout.
//   - Suspected: the error is the driver's "invalid connection" and the
//     attempt ran at least the deadline. The driver reports a read deadline
//     this way, discarding the timeout, so elapsed time is the only signal
//     left. It is a conservative replay policy, not a timeout detector: a
//     pool wait, earlier statements or a long healthy stream can make a reset
//     slow too. Those also stop, worded "possible".
//
// A fast "invalid connection" (a stale pooled socket) keeps its retry. A pool
// with no read deadline (deadline <= 0), a canceled or expired context, an
// already-permanent error and a decoded server error are never classified,
// whatever their text says.
func classifyPooledReadFailure(ctx context.Context, err error, elapsed, deadline time.Duration, op string) error {
	if err == nil || deadline <= 0 {
		return err
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var already *storage.ReadTimeoutError
	if errors.As(err, &already) {
		return err
	}
	var permanent *backoff.PermanentError
	if errors.As(err, &permanent) {
		return err
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return err
	}
	confirmed := isNetworkReadTimeout(err)
	if !confirmed && (!isInvalidConnection(err) || elapsed < deadline) {
		return err
	}
	if op != "" {
		err = fmt.Errorf("%s: %w", op, err)
	}
	return &storage.ReadTimeoutError{Timeout: deadline, Confirmed: confirmed, Err: err}
}

// isNetworkReadTimeout reports whether err retains a network read that timed
// out. Dial and write timeouts are not read deadlines and do not count.
func isNetworkReadTimeout(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "read" && opErr.Timeout()
}

// isInvalidConnection reports whether err is the MySQL driver's
// mysql.ErrInvalidConn, or its exact rendering at the end of an error chain
// that lost the sentinel's identity (a %v wrap). Other text that merely
// mentions a connection is not matched.
func isInvalidConnection(err error) bool {
	if errors.Is(err, mysql.ErrInvalidConn) {
		return true
	}
	msg := err.Error()
	text := mysql.ErrInvalidConn.Error()
	return msg == text || strings.HasSuffix(msg, ": "+text)
}
