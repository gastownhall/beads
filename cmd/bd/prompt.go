package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// notifyContext is overridden in tests to avoid sending real signals.
var notifyContext = signal.NotifyContext

const exitCodeCanceled = 130

// readLineWithContext returns a line from reader or ctx.Err() if canceled.
// If closer is provided, it is closed on cancellation to unblock the read.
func readLineWithContext(ctx context.Context, reader *bufio.Reader, closer io.Closer) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	sigCtx, stop := notifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := sigCtx.Err(); err != nil {
		return "", err
	}

	type result struct {
		line string
		err  error
	}

	resultCh := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		resultCh <- result{line: line, err: err}
	}()

	select {
	case <-sigCtx.Done():
		if closer != nil {
			_ = closer.Close() // Best effort cleanup of readline
		}
		return "", sigCtx.Err()
	case res := <-resultCh:
		// When both channels are ready simultaneously (e.g., context canceled
		// at the same time the pipe closes with EOF), prefer the cancellation
		// error. This prevents a race on macOS where EOF wins the select.
		select {
		case <-sigCtx.Done():
			return "", sigCtx.Err()
		default:
		}
		return res.line, res.err
	}
}

func isCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

func exitCanceled() {
	os.Exit(exitCodeCanceled)
}
