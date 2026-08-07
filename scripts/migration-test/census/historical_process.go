package main

import (
	"context"
	"errors"
	"fmt"
)

type historicalBinaryBindingContextKey struct{}

// historicalProcessContainmentError is infrastructure failure. It deliberately
// does not unwrap the historical command error: callers must never classify an
// execution whose cleanup could not be proved as an ordinary historical exit.
type historicalProcessContainmentError struct {
	actionErr            error
	cleanupErr           error
	unexpectedDescendant bool
}

func (err *historicalProcessContainmentError) Error() string {
	if err.cleanupErr != nil && err.actionErr != nil {
		return fmt.Sprintf("contain historical process descendants: %v; historical action before containment: %v", err.cleanupErr, err.actionErr)
	}
	if err.cleanupErr != nil {
		return fmt.Sprintf("contain historical process descendants: %v", err.cleanupErr)
	}
	if err.actionErr != nil {
		return fmt.Sprintf("contain historical process descendants: unexpected descendant; historical action before containment: %v", err.actionErr)
	}
	return "contain historical process descendants: unexpected descendant"
}

// withHistoricalBinaryBinding associates an in-generation binary binding with
// a context that is allowed to execute that binary. The binding is intentionally
// process-local evidence: source builds do not serialize an output digest.
func withHistoricalBinaryBinding(ctx context.Context, binding freshBinary) context.Context {
	return withHistoricalBinaryBindings(ctx, []freshBinary{binding})
}

func withHistoricalBinaryBindings(ctx context.Context, bindings []freshBinary) context.Context {
	current, _ := ctx.Value(historicalBinaryBindingContextKey{}).(map[string]freshBinary)
	copy := make(map[string]freshBinary, len(current)+len(bindings))
	for path, binding := range current {
		copy[path] = binding
	}
	for _, binding := range bindings {
		copy[binding.path] = binding
	}
	return context.WithValue(ctx, historicalBinaryBindingContextKey{}, copy)
}

func historicalBinaryBinding(ctx context.Context, binary string) (freshBinary, error) {
	bindings, ok := ctx.Value(historicalBinaryBindingContextKey{}).(map[string]freshBinary)
	if !ok {
		return freshBinary{}, errors.New("historical binary has no in-generation executable binding")
	}
	binding, ok := bindings[binary]
	if !ok || binding.path != binary {
		return freshBinary{}, errors.New("historical binary is not bound in this execution context")
	}
	if !validSHA256(binding.executableSHA256) {
		return freshBinary{}, errors.New("historical binary binding has an invalid executable digest")
	}
	switch binding.acquisition.Kind {
	case "source-build":
		if !validAcquisition(binding.acquisition) {
			return freshBinary{}, errors.New("historical source build binding has an invalid acquisition")
		}
	case "release-asset":
		if !validAcquisition(binding.acquisition) || binding.acquisition.ExecutableSHA256 != binding.executableSHA256 {
			return freshBinary{}, errors.New("historical release asset binding does not match its recorded digest")
		}
	default:
		return freshBinary{}, errors.New("historical binary binding has an unknown acquisition")
	}
	return binding, nil
}
