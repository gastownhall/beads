package utils

import (
	"context"
	"strings"
	"testing"
)

// A nil store makes the ordering observable: reaching the "storage is nil"
// error proves the sentinel guard did NOT fire for that input.
func TestResolvePartialIDRefusesSentinelTokens(t *testing.T) {
	ctx := context.Background()

	refused := []string{
		"null", "NULL", "Null", " null ",
		"undefined", "UNDEFINED",
		"", "   ",
	}
	for _, input := range refused {
		got, err := ResolvePartialID(ctx, nil, input)
		if err == nil {
			t.Errorf("ResolvePartialID(%q) = %q, nil; want a refusal", input, got)
			continue
		}
		if strings.Contains(err.Error(), "storage is nil") {
			t.Errorf("ResolvePartialID(%q) reached the store; the sentinel guard did not fire: %v", input, err)
		}
	}
}

func TestResolvePartialIDAcceptsIDsContainingSentinelTokens(t *testing.T) {
	ctx := context.Background()

	accepted := []string{
		"bd-a3f8e9",
		"a3f8",
		"nullos",
		"null3t0",
		"bd-null",
		"bd-null.1",
		"undefined-behavior",
	}
	for _, input := range accepted {
		_, err := ResolvePartialID(ctx, nil, input)
		if err == nil || !strings.Contains(err.Error(), "storage is nil") {
			t.Errorf("ResolvePartialID(%q) = %v; want it to pass the sentinel guard and reach the store", input, err)
		}
	}
}
