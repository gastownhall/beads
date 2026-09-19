package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// resolveStorageClass: the explicit flag path (config-default resolution
// rides config.GetString and is exercised by the embedded create tests).
func TestResolveStorageClassExplicit(t *testing.T) {
	got, err := resolveStorageClass("unversioned", types.TypeTask)
	if err != nil || got != types.StorageClassUnversioned {
		t.Errorf("explicit unversioned: got %q, %v", got, err)
	}

	// Explicit versioned is returned verbatim; callers normalize it to the unset
	// marker (C2.4 omitted-when-versioned) only after plane-conflict validation,
	// so the durable request survives long enough to be honored or rejected.
	got, err = resolveStorageClass("versioned", types.TypeTask)
	if err != nil || got != types.StorageClassVersioned {
		t.Errorf("explicit versioned should be preserved: got %q, %v", got, err)
	}

	got, err = resolveStorageClass("ephemeral", types.TypeTask)
	if err != nil || got != types.StorageClassEphemeral {
		t.Errorf("explicit ephemeral: got %q, %v", got, err)
	}

	if _, err := resolveStorageClass("bogus", types.TypeTask); err == nil {
		t.Error("invalid explicit value should error")
	}
}

func TestValidateStorageClassConfig(t *testing.T) {
	if err := validateStorageClassConfig("storage-class.event", "unversioned"); err != nil {
		t.Errorf("valid key+value rejected: %v", err)
	}
	if err := validateStorageClassConfig("storage-class.", "unversioned"); err == nil {
		t.Error("empty type suffix should be rejected")
	}
	if err := validateStorageClassConfig("storage-class.event.extra", "unversioned"); err == nil {
		t.Error("nested suffix should be rejected")
	}
	err := validateStorageClassConfig("storage-class.event", "permanent")
	if err == nil || !strings.Contains(err.Error(), "versioned, unversioned, or ephemeral") {
		t.Errorf("bad value should be rejected with the value list, got: %v", err)
	}
	// The suffix is validated too: create-time lookup keys on the Normalize()d
	// type, so an alias or typo would otherwise pass here and silently never
	// match (lion's #5149 should-fix).
	err = validateStorageClassConfig("storage-class.feat", "unversioned")
	if err == nil || !strings.Contains(err.Error(), "storage-class.feature") {
		t.Errorf("alias suffix should be rejected with the canonical key hint, got: %v", err)
	}
	if err := validateStorageClassConfig("storage-class.taks", "unversioned"); err == nil {
		t.Error("unknown (typo) suffix should be rejected")
	}
	if err := validateStorageClassConfig("storage-class.task", "unversioned"); err != nil {
		t.Errorf("canonical built-in suffix rejected: %v", err)
	}
}

// reconcileStorageClassPlane is the flag-over-config decision shared by
// single-issue create and graph-apply (Protocol v0.1 §C1.3). A durable class on
// an effective wisp plane is a contradiction: an explicit class is rejected so
// the durable intent is not silently erased, while a config-derived class yields
// to the explicit plane. versioned normalizes to the unset marker only after the
// check, so on conflict the class is returned verbatim for the caller's message.
func TestReconcileStorageClassPlane(t *testing.T) {
	tests := []struct {
		name      string
		class     types.StorageClass
		explicit  bool
		wispPlane bool
		wantClass types.StorageClass
		wantConf  bool
	}{
		{"explicit unversioned + wisp plane conflicts", types.StorageClassUnversioned, true, true, types.StorageClassUnversioned, true},
		{"explicit versioned + wisp plane conflicts (verbatim)", types.StorageClassVersioned, true, true, types.StorageClassVersioned, true},
		{"config unversioned yields to wisp plane", types.StorageClassUnversioned, false, true, "", false},
		{"config versioned yields to wisp plane", types.StorageClassVersioned, false, true, "", false},
		{"explicit unversioned stays on a durable row", types.StorageClassUnversioned, true, false, types.StorageClassUnversioned, false},
		{"config unversioned stays on a durable row", types.StorageClassUnversioned, false, false, types.StorageClassUnversioned, false},
		{"versioned normalizes to unset on a durable row", types.StorageClassVersioned, true, false, "", false},
		{"unset class with a wisp plane is a no-op", "", true, true, "", false},
		{"unset class without a wisp plane is a no-op", "", false, false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotClass, gotConf := reconcileStorageClassPlane(tt.class, tt.explicit, tt.wispPlane)
			if gotClass != tt.wantClass || gotConf != tt.wantConf {
				t.Errorf("reconcileStorageClassPlane(%q, explicit=%v, wisp=%v) = (%q, %v), want (%q, %v)",
					tt.class, tt.explicit, tt.wispPlane, gotClass, gotConf, tt.wantClass, tt.wantConf)
			}
		})
	}
}

// resolveCreateStorageClass is the ONE create-time decision every CLI door
// makes, so its table is the contract the direct, proxied and --file routes all
// answer from. Each case names the class the caller asked for, the plane flags
// it asked for, and what the record must end up as.
func TestResolveCreateStorageClass(t *testing.T) {
	tests := []struct {
		name      string
		flag      string
		issueType types.IssueType
		wisp      bool
		noHistory bool
		wantClass types.StorageClass
		wantWisp  bool
		wantErr   string
	}{
		{name: "no flag is unset", issueType: types.TypeTask},
		{name: "explicit unversioned persists", flag: "unversioned", issueType: types.TypeTask, wantClass: types.StorageClassUnversioned},
		// C2.4: versioned is the default and serializes as the unset marker, so
		// the durable row carries no cell.
		{name: "explicit versioned normalizes to unset", flag: "versioned", issueType: types.TypeTask},
		// C1.4: the spelled-out ephemeral class IS --ephemeral, and a wisp-plane
		// row derives its class (C1.2) rather than carrying a marker.
		{name: "explicit ephemeral routes to the wisp plane", flag: "ephemeral", issueType: types.TypeTask, wantWisp: true},
		{name: "ephemeral flag alone leaves the class unset", issueType: types.TypeTask, wisp: true, wantWisp: true},
		{name: "no-history alone leaves the class unset", issueType: types.TypeTask, noHistory: true},
		// An explicit durable class on a wisp plane is a contradiction the caller
		// spelled out, so it is refused rather than silently collapsed.
		{name: "explicit versioned with --ephemeral conflicts", flag: "versioned", issueType: types.TypeTask, wisp: true, wantWisp: true, wantErr: "conflicts with --ephemeral/--no-history"},
		{name: "explicit unversioned with --ephemeral conflicts", flag: "unversioned", issueType: types.TypeTask, wisp: true, wantWisp: true, wantErr: "conflicts with --ephemeral/--no-history"},
		{name: "explicit unversioned with --no-history conflicts", flag: "unversioned", issueType: types.TypeTask, noHistory: true, wantErr: "conflicts with --ephemeral/--no-history"},
		{name: "explicit ephemeral with --no-history conflicts", flag: "ephemeral", issueType: types.TypeTask, noHistory: true, wantErr: "--storage-class ephemeral and --no-history are mutually exclusive"},
		{name: "unparseable flag is a usage error", flag: "permanent", issueType: types.TypeTask, wantErr: "versioned, unversioned, or ephemeral"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotClass, gotWisp, err := resolveCreateStorageClass(tt.flag, tt.issueType, tt.wisp, tt.noHistory)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveCreateStorageClass(%q, wisp=%v, noHistory=%v) error = %v, want one containing %q",
						tt.flag, tt.wisp, tt.noHistory, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCreateStorageClass(%q, wisp=%v, noHistory=%v): %v", tt.flag, tt.wisp, tt.noHistory, err)
			}
			if gotClass != tt.wantClass || gotWisp != tt.wantWisp {
				t.Errorf("resolveCreateStorageClass(%q, wisp=%v, noHistory=%v) = (%q, %v), want (%q, %v)",
					tt.flag, tt.wisp, tt.noHistory, gotClass, gotWisp, tt.wantClass, tt.wantWisp)
			}
		})
	}
}

// The per-type storage-class.<type> default is the half of the contract the
// proxied and --file doors used to skip entirely, and it is keyed on the
// NORMALIZED type, so an alias must find the canonical key.
func TestResolveCreateStorageClassConfigDefault(t *testing.T) {
	initConfigForTest(t)
	config.Set("storage-class.task", "unversioned")
	config.Set("storage-class.feature", "unversioned")

	class, wisp, err := resolveCreateStorageClass("", types.TypeTask, false, false)
	if err != nil || class != types.StorageClassUnversioned || wisp {
		t.Errorf("config default: got (%q, %v, %v), want (unversioned, false, nil)", class, wisp, err)
	}
	// Aliases normalize before the lookup, so --type feat finds
	// storage-class.feature (the only spelling `bd config set` accepts).
	class, _, err = resolveCreateStorageClass("", types.IssueType("feat"), false, false)
	if err != nil || class != types.StorageClassUnversioned {
		t.Errorf("alias type: got (%q, %v), want unversioned", class, err)
	}
	// Another type is untouched.
	class, _, err = resolveCreateStorageClass("", types.TypeBug, false, false)
	if err != nil || class != "" {
		t.Errorf("unconfigured type: got (%q, %v), want unset", class, err)
	}
	// Flag over config (C1.3).
	class, _, err = resolveCreateStorageClass("versioned", types.TypeTask, false, false)
	if err != nil || class != "" {
		t.Errorf("explicit versioned over config default: got (%q, %v), want unset", class, err)
	}
	// A config-derived durable class is not a caller contradiction, so it YIELDS
	// to the explicit plane rather than blocking the create.
	class, wisp, err = resolveCreateStorageClass("", types.TypeTask, true, false)
	if err != nil || class != "" || !wisp {
		t.Errorf("config default under --ephemeral: got (%q, %v, %v), want ('', true, nil)", class, wisp, err)
	}
}

// The proxied single create's whole bug was that the class never reached
// createInput, so the two links in that chain get their own pins.
func TestGatherCreateInputCarriesStorageClass(t *testing.T) {
	initConfigForTest(t)

	in, err := gatherCreateInput(newCreateFlagsCommand(t, "--storage-class", "unversioned"), []string{"Unversioned"})
	if err != nil {
		t.Fatalf("gatherCreateInput: %v", err)
	}
	if in.storageClass != types.StorageClassUnversioned || in.storageClassFlag != "unversioned" || in.ephemeral {
		t.Errorf("unversioned: storageClass=%q flag=%q ephemeral=%v", in.storageClass, in.storageClassFlag, in.ephemeral)
	}
	if issue := buildCreateIssueFromInput(in); issue.StorageClass != types.StorageClassUnversioned {
		t.Errorf("buildCreateIssueFromInput dropped the class: got %q", issue.StorageClass)
	}

	// C1.4 on the proxied door: the spelled-out class flips the input to the
	// wisp plane, which is what routes the create away from the durable table.
	in, err = gatherCreateInput(newCreateFlagsCommand(t, "--storage-class", "ephemeral"), []string{"Ephemeral"})
	if err != nil {
		t.Fatalf("gatherCreateInput ephemeral: %v", err)
	}
	if !in.ephemeral || in.storageClass != "" {
		t.Errorf("ephemeral spelling: ephemeral=%v storageClass=%q, want true/unset", in.ephemeral, in.storageClass)
	}
	if issue := buildCreateIssueFromInput(in); !issue.Ephemeral || issue.StorageClass != "" {
		t.Errorf("buildCreateIssueFromInput: ephemeral=%v class=%q", issue.Ephemeral, issue.StorageClass)
	}

	if _, err := gatherCreateInput(newCreateFlagsCommand(t, "--ephemeral", "--storage-class", "versioned"), []string{"Conflict"}); err == nil {
		t.Error("--ephemeral with an explicit durable class should be refused before any transaction")
	}

	// --file keeps the RAW flag for per-template resolution and resolves nothing
	// here: the per-type default needs a type, and only a template has one.
	in, err = gatherCreateInput(newCreateFlagsCommand(t, "--file", "plan.md", "--storage-class", "unversioned"), nil)
	if err != nil {
		t.Fatalf("gatherCreateInput --file: %v", err)
	}
	if in.storageClassFlag != "unversioned" || in.storageClass != "" {
		t.Errorf("--file: flag=%q resolved=%q, want the raw flag and no premature resolution", in.storageClassFlag, in.storageClass)
	}
}

// --graph refuses a plan-wide --storage-class the way it already refuses
// --mol-type: the per-node storage_class field (plus its per-type config
// default) is the mechanism that works, and a second plan-wide one would only
// raise precedence questions. Both transports route through this check.
// (HandleError writes the text to stderr and returns a bare exit code, so the
// wording is pinned by the subprocess suites that capture it.)
func TestRejectSingleIssueFlagsForGraphRefusesStorageClass(t *testing.T) {
	if err := rejectSingleIssueFlagsForGraph(newCreateFlagsCommand(t, "--graph", "plan.json", "--storage-class", "unversioned")); err == nil {
		t.Fatal("--graph with a plan-wide --storage-class should be refused")
	}
	if err := rejectSingleIssueFlagsForGraph(newCreateFlagsCommand(t, "--graph", "plan.json")); err != nil {
		t.Errorf("--graph without the flag should pass: %v", err)
	}
	// --file still HONORS the flag, so it must not be caught by the graph
	// rejection's shared single-issue list.
	if err := rejectSingleIssueFlagsForMarkdown(newCreateFlagsCommand(t, "--file", "plan.md", "--storage-class", "unversioned")); err != nil {
		t.Errorf("--file with --storage-class should be accepted: %v", err)
	}
}
