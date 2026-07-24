package beads_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestReExportCloseBlocked proves the public beads.ErrCloseBlocked alias is the
// same value as the internal sentinel and composes through errors.Is when
// wrapped — the property CloseIssueChecked callers rely on to detect a guard
// refusal without importing internal/storage.
func TestReExportCloseBlocked(t *testing.T) {
	t.Parallel()

	if beads.ErrCloseBlocked != storage.ErrCloseBlocked {
		t.Error("beads.ErrCloseBlocked is not the internal sentinel value (identity broken)")
	}
	wrapped := fmt.Errorf("x: %w", beads.ErrCloseBlocked)
	if !errors.Is(wrapped, beads.ErrCloseBlocked) {
		t.Errorf("errors.Is(wrapped, beads.ErrCloseBlocked) = false; err = %v", wrapped)
	}
}

// TestReExportVersionMismatch proves the public beads.ErrVersionMismatch alias
// is the same value as the internal sentinel and composes through errors.Is when
// wrapped — the property a CloseIssueChecked caller relies on to detect an
// optimistic-concurrency refusal without importing internal/storage.
func TestReExportVersionMismatch(t *testing.T) {
	t.Parallel()

	if beads.ErrVersionMismatch != storage.ErrVersionMismatch {
		t.Error("beads.ErrVersionMismatch is not the internal sentinel value (identity broken)")
	}
	wrapped := fmt.Errorf("x: %w", beads.ErrVersionMismatch)
	if !errors.Is(wrapped, beads.ErrVersionMismatch) {
		t.Errorf("errors.Is(wrapped, beads.ErrVersionMismatch) = false; err = %v", wrapped)
	}
}

// TestUpdateIssueOptionsIsExported proves the public beads.UpdateIssueOptions
// alias is usable from outside the module and its ExpectedVersion compare-and-
// swap field round-trips — the type a caller names to opt a
// Storage.UpdateIssueChecked into optimistic concurrency without importing
// internal/storage. The zero value must leave ExpectedVersion nil (no check).
func TestUpdateIssueOptionsIsExported(t *testing.T) {
	t.Parallel()

	v := int64(7)
	opts := beads.UpdateIssueOptions{ExpectedVersion: &v}
	if opts.ExpectedVersion == nil || *opts.ExpectedVersion != 7 {
		t.Fatalf("ExpectedVersion did not round-trip through the exported alias: %+v", opts)
	}
	if (beads.UpdateIssueOptions{}).ExpectedVersion != nil {
		t.Fatal("zero-value UpdateIssueOptions must have a nil ExpectedVersion (no check)")
	}
}

// TestReExportedSentinelIdentity proves each public sentinel is the SAME value
// as the internal one it aliases, so errors.Is composes across the package
// boundary without any bridging.
func TestReExportedSentinelIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		exported error
		internal error
	}{
		{"ErrNotFound", beads.ErrNotFound, storage.ErrNotFound},
		{"ErrAlreadyClaimed", beads.ErrAlreadyClaimed, storage.ErrAlreadyClaimed},
		{"ErrNotClaimable", beads.ErrNotClaimable, storage.ErrNotClaimable},
		{"ErrVersionMismatch", beads.ErrVersionMismatch, storage.ErrVersionMismatch},
		{"ErrSelfDependency", beads.ErrSelfDependency, domain.ErrSelfDependency},
		{"ErrDependencyCycle", beads.ErrDependencyCycle, domain.ErrDependencyCycle},
		{"ErrFieldTooLong", beads.ErrFieldTooLong, types.ErrFieldTooLong},
	}
	for _, tc := range cases {
		if tc.exported != tc.internal {
			t.Errorf("beads.%s is not the internal sentinel value (identity broken)", tc.name)
		}
	}
}

// stubDepRepo satisfies domain.DependencySQLRepository via the embedded
// interface; only the two methods dependencyUseCaseImpl.add consults before
// returning a self-dep/cycle sentinel are implemented. Any other call would
// nil-panic, which keeps the stub honest about the exact surface these
// branches touch.
type stubDepRepo struct {
	domain.DependencySQLRepository
	hasCycle bool
}

func (s stubDepRepo) ValidateBlockingHierarchy(context.Context, *types.Dependency) error {
	return nil
}

func (s stubDepRepo) HasCycle(context.Context, string, string) (bool, error) {
	return s.hasCycle, nil
}

// TestReExportedSentinelCatchesRealProductionError drives ACTUAL converted
// production returns — the domain dependency use case's self-dep and cycle
// branches — and asserts the PUBLIC aliases match them via errors.Is. This is
// the property the re-export exists to provide, verified end to end through
// real code rather than by wrapping a sentinel with itself.
func TestReExportedSentinelCatchesRealProductionError(t *testing.T) {
	t.Parallel()

	uc := domain.NewDependencyUseCase(stubDepRepo{})
	selfErr := uc.AddDependency(context.Background(),
		&types.Dependency{IssueID: "a", DependsOnID: "a", Type: types.DepBlocks}, "tester")
	if !errors.Is(selfErr, beads.ErrSelfDependency) {
		t.Errorf("errors.Is(real self-dep err, beads.ErrSelfDependency) = false; err = %v", selfErr)
	}

	uc = domain.NewDependencyUseCase(stubDepRepo{hasCycle: true})
	cycleErr := uc.AddDependency(context.Background(),
		&types.Dependency{IssueID: "a", DependsOnID: "b", Type: types.DepBlocks}, "tester")
	if !errors.Is(cycleErr, beads.ErrDependencyCycle) {
		t.Errorf("errors.Is(real cycle err, beads.ErrDependencyCycle) = false; err = %v", cycleErr)
	}
}

// insertErrDepRepo returns preset errors from the two methods the domain
// dependency use-case consults before its typed-conflict passthrough branches:
// ValidateBlockingHierarchy (hierarchy conflict) and Insert (type conflict). Any
// other call nil-panics through the embedded interface, keeping the stub honest
// about the surface these branches touch.
type insertErrDepRepo struct {
	domain.DependencySQLRepository
	hierarchyErr error
	insertErr    error
}

func (r insertErrDepRepo) ValidateBlockingHierarchy(context.Context, *types.Dependency) error {
	return r.hierarchyErr
}

func (r insertErrDepRepo) HasCycle(context.Context, string, string) (bool, error) {
	return false, nil
}

func (r insertErrDepRepo) Insert(context.Context, *types.Dependency, string, domain.DepInsertOpts) error {
	return r.insertErr
}

// TestReExportedDependencyConflictTypes proves the public
// beads.DependencyTypeConflictError and beads.DependencyHierarchyConflictError
// aliases are the SAME struct types the engine returns: driving ACTUAL domain
// use-case passthrough returns (the conflict is passed through unwrapped, the
// property both write stacks now share), errors.As classifies each through the
// public alias and reads its fields — no message parsing.
func TestReExportedDependencyConflictTypes(t *testing.T) {
	t.Parallel()

	// Type conflict: a different-type edge already exists between the pair.
	typeConflict := &domain.DependencyTypeConflictError{
		IssueID: "a", DependsOnID: "b", ExistingType: "blocks", RequestedType: "related",
	}
	uc := domain.NewDependencyUseCase(insertErrDepRepo{insertErr: typeConflict})
	err := uc.AddDependency(context.Background(),
		&types.Dependency{IssueID: "a", DependsOnID: "b", Type: types.DepRelated}, "tester")
	var gotType *beads.DependencyTypeConflictError
	if !errors.As(err, &gotType) {
		t.Fatalf("errors.As(real type-conflict err, *beads.DependencyTypeConflictError) = false; err = %v", err)
	}
	if gotType.IssueID != "a" || gotType.DependsOnID != "b" ||
		gotType.ExistingType != "blocks" || gotType.RequestedType != "related" {
		t.Errorf("extracted type-conflict fields = %+v, want {a b blocks related}", gotType)
	}

	// Hierarchy conflict: a blocking edge would gate an issue on its ancestor.
	hierConflict := &domain.DependencyHierarchyConflictError{
		IssueID: "child", BlockerID: "ancestor", BlockerIsAncestor: true,
	}
	uc = domain.NewDependencyUseCase(insertErrDepRepo{hierarchyErr: hierConflict})
	err = uc.AddDependency(context.Background(),
		&types.Dependency{IssueID: "child", DependsOnID: "ancestor", Type: types.DepBlocks}, "tester")
	var gotHier *beads.DependencyHierarchyConflictError
	if !errors.As(err, &gotHier) {
		t.Fatalf("errors.As(real hierarchy-conflict err, *beads.DependencyHierarchyConflictError) = false; err = %v", err)
	}
	if gotHier.IssueID != "child" || gotHier.BlockerID != "ancestor" || !gotHier.BlockerIsAncestor {
		t.Errorf("extracted hierarchy-conflict fields = %+v, want {child ancestor true}", gotHier)
	}
}

// TestReExportFieldTooLong proves the public beads.ErrFieldTooLong alias is the
// same value as the internal types sentinel and composes through errors.Is when
// wrapped — the property length-validation callers rely on to detect an
// over-length assignee/owner/label without importing internal/types. It also
// checks the MaxFieldLen constant re-export tracks the source of truth.
func TestReExportFieldTooLong(t *testing.T) {
	t.Parallel()

	if beads.ErrFieldTooLong != types.ErrFieldTooLong {
		t.Error("beads.ErrFieldTooLong is not the internal sentinel value (identity broken)")
	}
	wrapped := fmt.Errorf("x: %w", beads.ErrFieldTooLong)
	if !errors.Is(wrapped, beads.ErrFieldTooLong) {
		t.Errorf("errors.Is(wrapped, beads.ErrFieldTooLong) = false; err = %v", wrapped)
	}
	if beads.MaxFieldLen != types.MaxFieldLen {
		t.Errorf("beads.MaxFieldLen = %d, want types.MaxFieldLen = %d", beads.MaxFieldLen, types.MaxFieldLen)
	}
}
