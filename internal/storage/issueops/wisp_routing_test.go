package issueops

import (
	"context"
	"reflect"
	"testing"
)

func TestPartitionByWispSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ids       []string
		wispSet   map[string]struct{}
		wantWisps []string
		wantPerms []string
	}{
		{
			name:    "all permanent",
			ids:     []string{"be-1", "be-2", "be-3"},
			wispSet: map[string]struct{}{},
			// wantWisps nil — no entries appended
			wantPerms: []string{"be-1", "be-2", "be-3"},
		},
		{
			name:      "all wisps",
			ids:       []string{"be-wisp-a", "be-wisp-b"},
			wispSet:   map[string]struct{}{"be-wisp-a": {}, "be-wisp-b": {}},
			wantWisps: []string{"be-wisp-a", "be-wisp-b"},
			// wantPerms nil
		},
		{
			name:      "mixed",
			ids:       []string{"be-1", "be-wisp-a", "be-2", "be-wisp-b", "be-3"},
			wispSet:   map[string]struct{}{"be-wisp-a": {}, "be-wisp-b": {}},
			wantWisps: []string{"be-wisp-a", "be-wisp-b"},
			wantPerms: []string{"be-1", "be-2", "be-3"},
		},
		{
			name: "empty input",
			ids:  nil,
			// wantWisps nil, wantPerms nil
			wispSet: map[string]struct{}{"be-wisp-a": {}},
		},
		{
			name:      "nil wisp set treats all as permanent",
			ids:       []string{"be-1", "be-wisp-a"},
			wispSet:   nil,
			wantPerms: []string{"be-1", "be-wisp-a"},
		},
		{
			name:      "explicit-id wisp routes as wisp",
			ids:       []string{"custom-id-42"},
			wispSet:   map[string]struct{}{"custom-id-42": {}},
			wantWisps: []string{"custom-id-42"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotWisps, gotPerms := partitionByWispSet(tc.ids, tc.wispSet)
			if !reflect.DeepEqual(gotWisps, tc.wantWisps) {
				t.Errorf("wispIDs: got %v, want %v", gotWisps, tc.wantWisps)
			}
			if !reflect.DeepEqual(gotPerms, tc.wantPerms) {
				t.Errorf("permIDs: got %v, want %v", gotPerms, tc.wantPerms)
			}
		})
	}
}

func TestWispIDSetInTx_EmptyIDsNoQuery(t *testing.T) {
	t.Parallel()
	set, err := WispIDSetInTx(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("WispIDSetInTx(nil, nil): %v", err)
	}
	if set == nil {
		t.Fatalf("expected non-nil empty map, got nil")
	}
	if len(set) != 0 {
		t.Fatalf("expected empty map, got %v", set)
	}
}
