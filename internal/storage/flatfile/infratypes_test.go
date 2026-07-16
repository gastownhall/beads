package flatfile

import (
	"context"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// Oracle: issueops.ResolveInfraTypesInTx — the 'types.infra' config key wins,
// then config.yaml, then domain.DefaultInfraTypes() = {agent, role, message}.
// The old flatfile implementation returned a hardcoded
// {gate, molecule, merge-request}, which matches none of those layers.

func TestGetInfraTypesDefaults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	got := s.GetInfraTypes(ctx)
	want := make(map[string]bool)
	for _, typ := range domain.DefaultInfraTypes() {
		want[typ] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetInfraTypes() = %v, want defaults %v", got, want)
	}

	for _, typ := range domain.DefaultInfraTypes() {
		if !s.IsInfraTypeCtx(ctx, types.IssueType(typ)) {
			t.Errorf("IsInfraTypeCtx(%q) = false, want true (default infra type)", typ)
		}
	}
	// The old hardcoded set must NOT be infra under default config.
	for _, typ := range []string{"gate", "molecule", "merge-request"} {
		if s.IsInfraTypeCtx(ctx, types.IssueType(typ)) {
			t.Errorf("IsInfraTypeCtx(%q) = true, want false under default config", typ)
		}
	}
}

func TestGetInfraTypesFromConfig(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetConfig(ctx, "types.infra", "gate, custom-infra"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	got := s.GetInfraTypes(ctx)
	want := map[string]bool{"gate": true, "custom-infra": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetInfraTypes() = %v, want configured set %v", got, want)
	}
	// Config REPLACES the defaults entirely (reference resolution order).
	if s.IsInfraTypeCtx(ctx, "agent") {
		t.Error("IsInfraTypeCtx(agent) = true, want false when types.infra overrides defaults")
	}

	if err := s.DeleteConfig(ctx, "types.infra"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if !s.IsInfraTypeCtx(ctx, "agent") {
		t.Error("IsInfraTypeCtx(agent) = false after DeleteConfig, want defaults restored")
	}
}

func TestGetInfraTypesFreshMapPerCall(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m := s.GetInfraTypes(ctx)
	m["injected"] = true
	if s.GetInfraTypes(ctx)["injected"] {
		t.Error("caller mutation of GetInfraTypes result leaked into a later call")
	}
}
