package versioncontrolops

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// TransferTimeoutSetting names the configuration that bounds an embedded
// remote transfer statement, for error messages: the config key and the
// environment variable that overrides it.
const TransferTimeoutSetting = "dolt.remote-timeout / BD_DOLT_REMOTE_TIMEOUT"

type transferTimeoutKey struct{}

// WithTransferTimeout opts ctx into a per-statement deadline of d for the
// network statements this package issues (DOLT_PUSH and DOLT_FETCH, including
// the fetch half of a pull). The deadline starts when the statement starts,
// after the remote environment guards are entered, and covers only that
// statement: a pull's local merge and settle run without it (see
// PullWithStrategy). A caller deadline that ends sooner still wins. d <= 0
// leaves ctx unbounded.
//
// Callers that do not opt in keep their existing behavior; clone and backup
// never read the value.
func WithTransferTimeout(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, transferTimeoutKey{}, d)
}

func transferTimeout(ctx context.Context) time.Duration {
	d, _ := ctx.Value(transferTimeoutKey{}).(time.Duration)
	return d
}

// TransferTimeoutError reports a remote transfer statement that its
// configured transfer deadline (not the caller's) stopped. errors.Is matches
// both context.DeadlineExceeded and the statement's own error. The message
// names the operation, the budget and the setting that controls it; it never
// claims the remote was left unchanged, since a timed-out push may have
// written part of its data.
type TransferTimeoutError struct {
	Op      string
	Timeout time.Duration
	Err     error
}

func (e *TransferTimeoutError) Error() string {
	return fmt.Sprintf("%s timed out after %s (set %s to change the limit): %v",
		e.Op, e.Timeout, TransferTimeoutSetting, e.Err)
}

func (e *TransferTimeoutError) Unwrap() []error {
	return []error{context.DeadlineExceeded, e.Err}
}

// runTransfer runs one network statement under ctx's opted-in transfer
// deadline. It must be called inside withRemoteEnvGuards so the budget starts
// once the guards are held. A caller deadline at or before the transfer
// deadline is left in charge, and an error after the caller's own
// cancellation keeps that identity rather than blaming the transfer setting.
func runTransfer(ctx context.Context, op string, stmt func(ctx context.Context) error) error {
	d := transferTimeout(ctx)
	if d <= 0 {
		return stmt(ctx)
	}
	if deadline, ok := ctx.Deadline(); ok && !deadline.After(time.Now().Add(d)) {
		return stmt(ctx)
	}
	tctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	err := stmt(tctx)
	if err != nil && ctx.Err() == nil && errors.Is(tctx.Err(), context.DeadlineExceeded) {
		return &TransferTimeoutError{Op: op, Timeout: d, Err: err}
	}
	return err
}
