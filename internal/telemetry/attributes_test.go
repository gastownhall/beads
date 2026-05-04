package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

// captureBaseAttrs is the package-private hook that Init uses to populate the
// process-wide measurement attributes. Tests drive it directly so they don't
// need the full Init machinery (env vars, exporters) just to assert merging.

// resetBaseAttrs clears the package-level baseAttrs slice and restores it on
// test exit, so per-test captures don't leak into sibling tests.
func resetBaseAttrs(t *testing.T) {
	t.Helper()
	prev := baseAttrs
	baseAttrs = nil
	t.Cleanup(func() { baseAttrs = prev })
}

func TestBaseAttrs_DefaultIsEmpty(t *testing.T) {
	resetBaseAttrs(t)
	if got := BaseAttrs(); len(got) != 0 {
		t.Errorf("BaseAttrs() before capture = %v, want empty", got)
	}
}

func TestBaseAttrs_AfterCaptureWithPrefix(t *testing.T) {
	resetBaseAttrs(t)
	captureBaseAttrs("ass")
	got := BaseAttrs()
	v, ok := lookupAttr(got, "bd.prefix")
	if !ok {
		t.Fatalf("BaseAttrs() = %v; missing bd.prefix", got)
	}
	if v.AsString() != "ass" {
		t.Errorf("bd.prefix = %q, want ass", v.AsString())
	}
}

func TestBaseAttrs_AfterCaptureEmptyPrefixIsEmpty(t *testing.T) {
	resetBaseAttrs(t)
	captureBaseAttrs("")
	if got := BaseAttrs(); len(got) != 0 {
		t.Errorf("BaseAttrs() after empty-prefix capture = %v, want empty", got)
	}
}

func TestBaseAttrs_RecaptureReplaces(t *testing.T) {
	resetBaseAttrs(t)
	captureBaseAttrs("ass")
	captureBaseAttrs("bd")
	got := BaseAttrs()
	if len(got) != 1 {
		t.Fatalf("BaseAttrs() = %v, want exactly one attribute after recapture", got)
	}
	v, _ := lookupAttr(got, "bd.prefix")
	if v.AsString() != "bd" {
		t.Errorf("bd.prefix = %q, want bd (recapture should replace)", v.AsString())
	}
}

func TestBaseAttrs_ReturnsCopy(t *testing.T) {
	resetBaseAttrs(t)
	captureBaseAttrs("ass")
	got := BaseAttrs()
	if len(got) > 0 {
		got[0] = attribute.String("bd.prefix", "mutated-by-caller")
	}
	regot := BaseAttrs()
	v, _ := lookupAttr(regot, "bd.prefix")
	if v.AsString() != "ass" {
		t.Errorf("BaseAttrs() returned a mutable view of internal state; got %q want ass", v.AsString())
	}
}

func TestInit_CapturesBaseAttrsFromPrefix(t *testing.T) {
	clearAllEnv(t)
	resetTelemetryState(t)
	resetBaseAttrs(t)
	if err := Init(context.Background(), "bd", "v0.0.0", "ass"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	v, ok := lookupAttr(BaseAttrs(), "bd.prefix")
	if !ok {
		t.Fatalf("Init did not capture bd.prefix into BaseAttrs(); got %v", BaseAttrs())
	}
	if v.AsString() != "ass" {
		t.Errorf("BaseAttrs bd.prefix = %q, want ass", v.AsString())
	}
}

func TestInit_EmptyPrefixLeavesBaseAttrsEmpty(t *testing.T) {
	clearAllEnv(t)
	resetTelemetryState(t)
	resetBaseAttrs(t)
	if err := Init(context.Background(), "bd", "v0.0.0", ""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := BaseAttrs(); len(got) != 0 {
		t.Errorf("BaseAttrs() after empty-prefix Init = %v, want empty", got)
	}
}
