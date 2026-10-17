package types

import (
	"reflect"
	"testing"
)

func TestNormalizeCtxKeepsNilAndEmptyApart(t *testing.T) {
	t.Parallel()
	if got := NormalizeCtx(nil); got != nil {
		t.Fatalf("nil must stay nil (undeclared), got %#v", got)
	}
	for _, in := range [][]string{{}, {""}, {" ", ""}} {
		got := NormalizeCtx(in)
		if got == nil || len(got) != 0 {
			t.Fatalf("NormalizeCtx(%q) must be a NON-nil empty slice (declared empty), got %#v", in, got)
		}
	}
	got := NormalizeCtx([]string{" host=studio ", "gpu", "host=studio", "", "gpu"})
	if want := []string{"host=studio", "gpu"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeCtx = %q, want %q", got, want)
	}
}

func TestRequiresSatisfiedLabels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ctx  []string
		want []string
	}{
		{"nil ctx satisfies nothing", nil, nil},
		{"empty ctx satisfies nothing", []string{}, nil},
		{"bare token", []string{"gpu"}, []string{"requires:gpu"}},
		{"keyed token also satisfies the bare key", []string{"host=studio"}, []string{"requires:host", "requires:host=studio"}},
		{"dedupe across tokens", []string{"host=studio", "host=mini", "host"}, []string{"requires:host", "requires:host=mini", "requires:host=studio"}},
		{"a leading = is not a key", []string{"=v"}, []string{"requires:=v"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := RequiresSatisfiedLabels(tc.ctx); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("RequiresSatisfiedLabels(%q) = %q, want %q", tc.ctx, got, tc.want)
			}
		})
	}
}

func TestRequiresLabelSatisfied(t *testing.T) {
	t.Parallel()
	ctx := []string{"host=studio", "gpu"}
	for label, want := range map[string]bool{
		"lane:bee":             true, // not a requirement
		"requires:gpu":         true,
		"requires:host":        true, // bare key met by host=studio
		"requires:host=studio": true,
		"requires:host=mini":   false,
		"requires:prod-creds":  false,
		"requires:":            false, // an empty token is never satisfied
	} {
		if got := RequiresLabelSatisfied(label, ctx); got != want {
			t.Errorf("RequiresLabelSatisfied(%q, %q) = %v, want %v", label, ctx, got, want)
		}
	}
	if RequiresLabelSatisfied("requires:gpu", nil) {
		t.Errorf("an undeclared context must not satisfy a requirement in the in-memory twin")
	}
}
