package domain

import (
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestDefaultInfraTypesExcludeRig(t *testing.T) {
	got := DefaultInfraTypes()
	want := []string{"agent", "role", "message"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultInfraTypes() = %v, want %v", got, want)
	}
	if IsInfraType(types.IssueType("rig")) {
		t.Fatal("rig must be durable by default, not an infra/wisp-routed type")
	}
}

func TestDefaultInfraTypesReturnsCopy(t *testing.T) {
	got := DefaultInfraTypes()
	got[0] = "mutated"

	if IsInfraType(types.IssueType("mutated")) {
		t.Fatal("mutating DefaultInfraTypes result changed infra classification")
	}
	if !IsInfraType(types.IssueType("agent")) {
		t.Fatal("agent should remain an infra type")
	}
}
