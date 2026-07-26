// routing_notice_test.go - Tests for the routing-swap stderr notice printed
// by list/ready when reads are auto-routed to a different store.
//
// Deliberately untagged (no `cgo` build tag): printContributorRoutingNotice
// and routingNoticeText don't need a real store or the embedded Dolt server,
// so these tests exercise them with a nil storage.DoltStorage and must stay
// compilable under CGO_ENABLED=0 with the gms_pure_go build tag alongside
// the rest of the pure-Go test suite.

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/routing"
)

// TestRoutingNoticeText_VariesByRule verifies the notice's reason and fix
// command are branched on the matched routing rule instead of always
// attributing the swap to beads.role=contributor (gastownhall/beads#4866
// review, 2026-07-23).
func TestRoutingNoticeText_VariesByRule(t *testing.T) {
	tests := []struct {
		name       string
		rule       routing.RoutingRule
		wantReason string
		wantFix    string
	}{
		{
			name:       "contributor rule",
			rule:       routing.RuleContributor,
			wantReason: "beads.role=contributor",
			wantFix:    "git config beads.role maintainer",
		},
		{
			name:       "maintainer rule",
			rule:       routing.RuleMaintainer,
			wantReason: "routing.maintainer",
			wantFix:    "bd config unset routing.maintainer",
		},
		{
			name:       "default rule",
			rule:       routing.RuleDefault,
			wantReason: "routing.default",
			wantFix:    "bd config unset routing.default",
		},
		{
			name:       "no rule (fallback wording)",
			rule:       routing.RuleNone,
			wantReason: "auto-routing rule",
			wantFix:    "bd config get",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, fix := routingNoticeText(tt.rule)
			if !strings.Contains(reason, tt.wantReason) {
				t.Errorf("routingNoticeText(%v) reason = %q, want substring %q", tt.rule, reason, tt.wantReason)
			}
			if !strings.Contains(fix, tt.wantFix) {
				t.Errorf("routingNoticeText(%v) fix = %q, want substring %q", tt.rule, fix, tt.wantFix)
			}
		})
	}

	// The contributor-rule wording must not leak into the other rules'
	// notices: that mislabeling (fixed `git config beads.role maintainer`
	// advice for a maintainer- or default-routed swap) was the review's
	// blocking finding.
	for _, rule := range []routing.RoutingRule{routing.RuleMaintainer, routing.RuleDefault, routing.RuleNone} {
		reason, fix := routingNoticeText(rule)
		if strings.Contains(reason, "beads.role") || strings.Contains(fix, "beads.role") {
			t.Errorf("routingNoticeText(%v) incorrectly mentions beads.role: reason=%q fix=%q", rule, reason, fix)
		}
	}
}

// TestPrintContributorRoutingNotice_TextMatchesRule verifies the emitted
// stderr notice reflects the rule passed in, for each rule variant.
func TestPrintContributorRoutingNotice_TextMatchesRule(t *testing.T) {
	origQuiet := quietFlag
	quietFlag = false
	t.Cleanup(func() { quietFlag = origQuiet })

	tests := []struct {
		name string
		rule routing.RoutingRule
		want string
	}{
		{"contributor", routing.RuleContributor, "beads.role=contributor"},
		{"maintainer", routing.RuleMaintainer, "routing.maintainer"},
		{"default", routing.RuleDefault, "routing.default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				printContributorRoutingNotice(context.Background(), nil, tt.rule)
			})
			if !strings.Contains(out, tt.want) {
				t.Errorf("printContributorRoutingNotice(rule=%v) stderr = %q, want substring %q", tt.rule, out, tt.want)
			}
		})
	}
}

// TestPrintContributorRoutingNotice_QuietSuppresses verifies --quiet
// suppresses the routing notice, matching the other non-error stderr
// notices in this package (tips.go, metrics.go) that respect quietFlag.
func TestPrintContributorRoutingNotice_QuietSuppresses(t *testing.T) {
	origQuiet := quietFlag
	t.Cleanup(func() { quietFlag = origQuiet })

	quietFlag = true
	out := captureStderr(t, func() {
		printContributorRoutingNotice(context.Background(), nil, routing.RuleContributor)
	})
	if out != "" {
		t.Errorf("printContributorRoutingNotice() with quietFlag=true wrote stderr = %q, want empty", out)
	}

	quietFlag = false
	out = captureStderr(t, func() {
		printContributorRoutingNotice(context.Background(), nil, routing.RuleContributor)
	})
	if out == "" {
		t.Error("printContributorRoutingNotice() with quietFlag=false wrote no stderr, want non-empty")
	}
}
