// Package handoffauth provides authorization checks for cross-project handoff operations.
//
// Policies are stored in DB-backed project config:
//   - handoff.allow_send_to: comma-separated list of project names (or "*")
//   - handoff.accept_from: comma-separated list of project names (or "*")
//
// Sender identity on shared-server transport is derived from the database name
// (transport-authenticated), not from client-supplied fields.
package handoffauth

import (
	"context"
	"fmt"
	"strings"
)

// ConfigGetter abstracts config access for authorization checks.
type ConfigGetter interface {
	GetConfig(ctx context.Context, key string) (string, error)
}

// CheckSendAllowed verifies that the current project is allowed to send to the
// given target project. Returns nil if allowed, error if denied.
// Error messages are intentionally generic to prevent project enumeration.
func CheckSendAllowed(ctx context.Context, cfg ConfigGetter, targetProject string) error {
	policy, err := cfg.GetConfig(ctx, "handoff.allow_send_to")
	if err != nil || policy == "" {
		// No policy configured — deny by default for safety
		return fmt.Errorf("send failed: no outbound handoff policy configured (set handoff.allow_send_to)")
	}
	if !matchesPolicy(policy, targetProject) {
		// Intentionally generic error to prevent project enumeration
		return fmt.Errorf("send failed: target project not permitted by handoff policy")
	}
	return nil
}

// CheckAcceptAllowed verifies that the current project accepts handoff items
// from the given sender project. Returns nil if allowed, error if denied.
func CheckAcceptAllowed(ctx context.Context, cfg ConfigGetter, senderProject string) error {
	policy, err := cfg.GetConfig(ctx, "handoff.accept_from")
	if err != nil || policy == "" {
		// No policy configured — accept all (backward compatible default)
		return nil
	}
	if !matchesPolicy(policy, senderProject) {
		return fmt.Errorf("handoff rejected: sender project %q not in accept policy", senderProject)
	}
	return nil
}

// matchesPolicy checks if a project name matches a comma-separated policy string.
// A policy of "*" matches everything.
func matchesPolicy(policy, project string) bool {
	policy = strings.TrimSpace(policy)
	if policy == "*" {
		return true
	}
	for _, allowed := range strings.Split(policy, ",") {
		if strings.TrimSpace(allowed) == project {
			return true
		}
	}
	return false
}
