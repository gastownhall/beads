package domain

import (
	"context"
	"slices"
	"testing"
)

// stubPrefixLabelUseCase answers only the label reads labelPrefixRemovals
// makes. The embedded interface is nil on purpose: a call this test did not
// intend panics rather than passing quietly.
type stubPrefixLabelUseCase struct {
	LabelUseCase
	durable []string
	wisp    []string
}

func (s *stubPrefixLabelUseCase) GetLabels(context.Context, string) ([]string, error) {
	return s.durable, nil
}

func (s *stubPrefixLabelUseCase) GetWispLabels(context.Context, string) ([]string, error) {
	return s.wisp, nil
}

// TestLabelPrefixRemovalsKeepTheEditAddRemove pins the uow route's half of
// LabelPatch.RemovePrefixes (GH#5657) without a replacement: the prefixes
// resolve, against the labels the transaction reads, into the exact labels
// the Remove path drops, so ApplyUpdate keeps the edit on its Add/Remove
// paths as the direct route keeps it. A legacy empty-string label row matches
// no prefix and stays (converting the patch to SetLabels used to delete it, or
// fail on it); a label the adds put back is not removed, so re-adding the
// dimension's one value writes nothing; an explicit Remove still wins over an
// add; and an empty prefix matches nothing.
//
// changed is what decides whether ApplyUpdate rewrites the label coordination
// cell: the edit leaves a different set than the one it read. Setting a
// dimension that holds no value yet changes the set without removing anything,
// which is the overlap two first-time `bd set-state` calls race on; an add the
// patch's own Remove takes back changes nothing.
func TestLabelPrefixRemovalsKeepTheEditAddRemove(t *testing.T) {
	held := []string{"", "state:a", "state:b", "stateful", "keep"}
	for _, tc := range []struct {
		name    string
		spec    UpdateSpec
		durable []string
		wisp    bool
		want    []string
		changed bool
	}{
		{
			name:    "drops the dimension but the re-added value, keeps the legacy empty row",
			spec:    UpdateSpec{RemoveLabelPrefixes: []string{"state:"}, AddLabels: []string{"state:b"}},
			want:    []string{"state:a"},
			changed: true,
		},
		{
			name:    "an explicit remove wins over an add",
			spec:    UpdateSpec{RemoveLabelPrefixes: []string{"state:"}, AddLabels: []string{"state:b"}, RemoveLabels: []string{"state:b"}},
			want:    []string{"state:b", "state:a"},
			changed: true,
		},
		{
			name:    "an empty prefix matches nothing",
			spec:    UpdateSpec{RemoveLabelPrefixes: []string{""}, AddLabels: []string{"state:c"}},
			want:    nil,
			changed: true,
		},
		{
			name:    "the wisp plane reads its own labels",
			spec:    UpdateSpec{RemoveLabelPrefixes: []string{"state:"}, AddLabels: []string{"state:c"}},
			wisp:    true,
			want:    []string{"state:x"},
			changed: true,
		},
		{
			name:    "a first value removes nothing and still changes the set",
			spec:    UpdateSpec{RemoveLabelPrefixes: []string{"state:"}, AddLabels: []string{"state:a"}},
			durable: []string{"keep"},
			want:    nil,
			changed: true,
		},
		{
			name:    "an add the patch removes again changes nothing",
			spec:    UpdateSpec{RemoveLabelPrefixes: []string{"state:"}, AddLabels: []string{"state:z"}, RemoveLabels: []string{"state:z"}},
			durable: []string{"keep"},
			want:    []string{"state:z"},
			changed: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			durable := held
			if tc.durable != nil {
				durable = tc.durable
			}
			u := &issueUseCaseImpl{labelUC: &stubPrefixLabelUseCase{durable: durable, wisp: []string{"", "state:x", "keep"}}}
			got, changed, err := u.labelPrefixRemovals(t.Context(), "bd-1", tc.spec, tc.wisp)
			if err != nil {
				t.Fatalf("labelPrefixRemovals: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("removals = %q, want %q", got, tc.want)
			}
			if changed != tc.changed {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
		})
	}

	// The dimension already holds exactly the value the patch adds: nothing to
	// remove and nothing changes, so the Add/Remove paths write no row or event
	// and ApplyUpdate leaves the cell alone.
	u := &issueUseCaseImpl{labelUC: &stubPrefixLabelUseCase{durable: []string{"state:b", "keep"}}}
	got, changed, err := u.labelPrefixRemovals(t.Context(), "bd-1",
		UpdateSpec{RemoveLabelPrefixes: []string{"state:"}, AddLabels: []string{"state:b"}}, false)
	if err != nil || len(got) != 0 || changed {
		t.Errorf("re-adding the one held value: removals = (%q, changed %v, %v), want none, unchanged", got, changed, err)
	}
}

// TestLabelPrefixPatchTargetWithAReplacement pins the other half: beside a
// replacement the prefixes apply to the replaced set, in LabelPatch order, and
// an empty entry is dropped, so the diffing SetLabels path gets the final set.
func TestLabelPrefixPatchTargetWithAReplacement(t *testing.T) {
	got := labelPrefixPatchTarget([]string{"state:a", "", "x", "keep"}, UpdateSpec{
		RemoveLabelPrefixes: []string{"state:"},
		AddLabels:           []string{"state:c", ""},
		RemoveLabels:        []string{"x"},
	})
	if want := []string{"keep", "state:c"}; !slices.Equal(got, want) {
		t.Errorf("target = %q, want %q", got, want)
	}
}

// coordinationLabelRepo holds one issue's labels in memory and logs, in order,
// every row write and every coordination touch, so a test sees whether a label
// edit touched the cell and whether it did so before its first row write. The
// embedded interface is nil on purpose: any other call panics.
type coordinationLabelRepo struct {
	LabelSQLRepository
	labels []string
	log    []string
}

func (r *coordinationLabelRepo) List(context.Context, string, LabelOpts) ([]string, error) {
	return slices.Clone(r.labels), nil
}

func (r *coordinationLabelRepo) Insert(_ context.Context, _, label, _ string, _ LabelOpts) error {
	r.log = append(r.log, "insert "+label)
	if !slices.Contains(r.labels, label) {
		r.labels = append(r.labels, label)
	}
	return nil
}

func (r *coordinationLabelRepo) Delete(_ context.Context, _, label, _ string, _ LabelOpts) error {
	r.log = append(r.log, "delete "+label)
	r.labels = slices.DeleteFunc(r.labels, func(held string) bool { return held == label })
	return nil
}

func (r *coordinationLabelRepo) TouchCoordination(_ context.Context, _ string, opts LabelOpts) error {
	if opts.UseWispsTable {
		r.log = append(r.log, "touch wisp_labels")
	} else {
		r.log = append(r.log, "touch labels")
	}
	return nil
}

// TestOnlySetLabelsTouchesTheCoordinationCell pins the label use case's half
// of the coordination rule (GH#5657, issueops.TouchLabelCoordinationInTx):
// SetLabels deletes whatever the set it read holds beyond the request, so it
// touches the cell, in its own tier, once and before its first row write when
// it changes the set, and not at all when it does not. The named AddLabels and
// RemoveLabels read nothing they act on and never touch it.
func TestOnlySetLabelsTouchesTheCoordinationCell(t *testing.T) {
	ctx := t.Context()
	repo := &coordinationLabelRepo{labels: []string{"state:a", "keep"}}
	uc := NewLabelUseCase(repo)

	if err := uc.SetLabels(ctx, "bd-1", []string{"state:b", "keep"}, "tester"); err != nil {
		t.Fatalf("SetLabels: %v", err)
	}
	if want := []string{"touch labels", "delete state:a", "insert state:b"}; !slices.Equal(repo.log, want) {
		t.Errorf("SetLabels that changes the set wrote %q, want %q", repo.log, want)
	}

	repo.log = nil
	if err := uc.SetLabels(ctx, "bd-1", []string{"keep", "state:b", ""}, "tester"); err != nil {
		t.Fatalf("SetLabels (same set): %v", err)
	}
	if len(repo.log) != 0 {
		t.Errorf("SetLabels with the held set wrote %q, want nothing: a no-op must not touch the cell", repo.log)
	}

	repo.log = nil
	if err := uc.SetWispLabels(ctx, "bd-w", []string{"state:b", "keep", "wisp:x"}, "tester"); err != nil {
		t.Fatalf("SetWispLabels: %v", err)
	}
	if want := []string{"touch wisp_labels", "insert wisp:x"}; !slices.Equal(repo.log, want) {
		t.Errorf("SetWispLabels that changes the set wrote %q, want %q", repo.log, want)
	}

	repo.log = nil
	if err := uc.AddLabels(ctx, "bd-1", []string{"area:y"}, "tester"); err != nil {
		t.Fatalf("AddLabels: %v", err)
	}
	if err := uc.RemoveLabels(ctx, "bd-1", []string{"keep"}, "tester"); err != nil {
		t.Fatalf("RemoveLabels: %v", err)
	}
	if want := []string{"insert area:y", "delete keep"}; !slices.Equal(repo.log, want) {
		t.Errorf("named adds and removes wrote %q, want %q and no touch", repo.log, want)
	}
}
