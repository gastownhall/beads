package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

func TestFindStateLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		labels    []string
		dimension string
		wantValue string
		wantFound bool
	}{
		{
			name:      "first exact dimension match wins",
			labels:    []string{"mode:normal", "mode:degraded"},
			dimension: "mode",
			wantValue: "normal",
			wantFound: true,
		},
		{
			name:      "retains colons in value",
			labels:    []string{"error:code:500"},
			dimension: "error",
			wantValue: "code:500",
			wantFound: true,
		},
		{
			name:      "empty value is found",
			labels:    []string{"mode:"},
			dimension: "mode",
			wantValue: "",
			wantFound: true,
		},
		{
			name:      "does not match dimension prefix without colon",
			labels:    []string{"mode-extra:degraded"},
			dimension: "mode",
			wantValue: "",
			wantFound: false,
		},
		{
			name:      "missing dimension",
			labels:    []string{"mode:normal"},
			dimension: "health",
			wantValue: "",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, found := findStateLabel(tt.labels, tt.dimension)
			if value != tt.wantValue || found != tt.wantFound {
				t.Fatalf("findStateLabel(%q, %q) = (%q, %t), want (%q, %t)", tt.labels, tt.dimension, value, found, tt.wantValue, tt.wantFound)
			}
		})
	}
}

func TestCollectStateLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		labels []string
		want   map[string]string
	}{
		{
			name: "nil labels returns nonnil empty map",
			want: map[string]string{},
		},
		{
			name:   "empty labels returns nonnil empty map",
			labels: []string{},
			want:   map[string]string{},
		},
		{
			name:   "ignores labels without a nonempty dimension",
			labels: []string{"backend", ":value"},
			want:   map[string]string{},
		},
		{
			name:   "retains empty and colon values",
			labels: []string{"mode:", "error:code:500"},
			want:   map[string]string{"mode": "", "error": "code:500"},
		},
		{
			name:   "last duplicate wins",
			labels: []string{"mode:normal", "mode:degraded"},
			want:   map[string]string{"mode": "degraded"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectStateLabels(tt.labels)
			if got == nil {
				t.Fatal("collectStateLabels returned a nil map")
			}
			if len(got) != len(tt.want) {
				t.Fatalf("collectStateLabels(%q) has %d values, want %d: %v", tt.labels, len(got), len(tt.want), got)
			}
			for dimension, wantValue := range tt.want {
				if gotValue, ok := got[dimension]; !ok || gotValue != wantValue {
					t.Errorf("collectStateLabels(%q)[%q] = (%q, %t), want (%q, true)", tt.labels, dimension, gotValue, ok, wantValue)
				}
			}
		})
	}
}

// labelPrefixProbeStore is a store that answers the label-prefix probe and
// nothing else: setStateLabelPatch must not touch any other method.
type labelPrefixProbeStore struct {
	storage.DoltStorage
	served bool
	err    error
}

func (s labelPrefixProbeStore) ServesLabelPrefixPatch(context.Context) (bool, error) {
	return s.served, s.err
}

// TestSetStateLabelPatch pins the shape `bd set-state` settles BEFORE it
// writes its event (GH#5657). Every route that takes the literal prefix gets
// RemovePrefixes, which the update evaluates in its own transaction. A bd
// serve that does not advertise issues.update.labelPrefixes would refuse that
// member, so set-state names the exact labels its read found instead: every
// value of the dimension except the new one (removal wins in a LabelPatch, so
// removing the new label too would drop it). A probe that fails is an error,
// so nothing is written on a guess.
func TestSetStateLabelPatch(t *testing.T) {
	t.Parallel()

	labels := []string{"mode:slow", "keep", "mode:fast", "mode-x:slow", "mode:degraded", "modes:a"}
	prefixed := issueops.LabelPatch{RemovePrefixes: []string{"mode:"}, Add: []string{"mode:fast"}}
	exact := issueops.LabelPatch{Add: []string{"mode:fast"}, Remove: []string{"mode:slow", "mode:degraded"}}

	for _, tc := range []struct {
		name  string
		store storage.DoltStorage
		want  issueops.LabelPatch
	}{
		{"no store to ask", nil, prefixed},
		{"a store that is not a prober", struct{ storage.DoltStorage }{}, prefixed},
		{"a server advertising the token", labelPrefixProbeStore{served: true}, prefixed},
		{"a server without the token", labelPrefixProbeStore{served: false}, exact},
	} {
		got, err := setStateLabelPatch(context.Background(), tc.store, "mode", "mode:fast", labels)
		if err != nil {
			t.Errorf("%s: setStateLabelPatch: %v", tc.name, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: patch = %+v, want %+v", tc.name, got, tc.want)
		}
	}

	// A dimension the read found unset removes nothing on the exact shape.
	got, err := setStateLabelPatch(context.Background(), labelPrefixProbeStore{}, "health", "health:ok", labels)
	if err != nil || !reflect.DeepEqual(got, issueops.LabelPatch{Add: []string{"health:ok"}}) {
		t.Errorf("unset dimension on an older server: patch = (%+v, %v), want an add only", got, err)
	}

	probeErr := errors.New("handshake unreadable")
	if _, err := setStateLabelPatch(context.Background(), labelPrefixProbeStore{err: probeErr}, "mode", "mode:fast", labels); !errors.Is(err, probeErr) {
		t.Errorf("a failed probe returned %v, want it wrapped", err)
	}
}
