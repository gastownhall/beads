package dolt

import "context"

type implicitVersionCommitModeKey struct{}

const (
	implicitVersionCommitOn    = "on"
	implicitVersionCommitOff   = "off"
	implicitVersionCommitBatch = "batch"
)

// WithImplicitVersionCommitMode threads the CLI auto-commit policy down into
// storage so ordinary write paths can decide whether to create a Dolt version
// commit immediately or leave changes in the working set.
func WithImplicitVersionCommitMode(ctx context.Context, mode string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	switch mode {
	case implicitVersionCommitOn, implicitVersionCommitOff, implicitVersionCommitBatch:
		return context.WithValue(ctx, implicitVersionCommitModeKey{}, mode)
	default:
		return ctx
	}
}

func shouldImplicitlyVersionCommit(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	mode, _ := ctx.Value(implicitVersionCommitModeKey{}).(string)
	switch mode {
	case implicitVersionCommitOff, implicitVersionCommitBatch:
		return false
	default:
		return true
	}
}
