package telemetry

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// baseAttrs holds the process-wide measurement attributes captured at
// telemetry.Init. These are stamped on every emitted metric so that an
// OTel→Prometheus pipeline can split bd.* series per beads project — the
// equivalent resource attributes only land on target_info, which Prometheus
// can't join without an instance label that the SDK doesn't always emit.
var baseAttrs []attribute.KeyValue

// captureBaseAttrs records the process-wide measurement attributes derived
// from prefix. Called by Init unconditionally so BaseAttrs() reports the
// configured prefix even when telemetry is disabled.
func captureBaseAttrs(prefix string) {
	if prefix == "" {
		baseAttrs = nil
		return
	}
	baseAttrs = []attribute.KeyValue{attribute.String("bd.prefix", prefix)}
}

// BaseAttrs returns a defensive copy of the process-wide measurement
// attributes set at Init. Returns nil when no prefix is configured.
func BaseAttrs() []attribute.KeyValue {
	if len(baseAttrs) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, len(baseAttrs))
	copy(out, baseAttrs)
	return out
}

// WithMergedAttrs returns a metric.MeasurementOption that combines the
// process-wide base attributes (BaseAttrs) with extras. Use it at every
// metric record/add call site so bd.prefix lands on the emitted datapoint.
func WithMergedAttrs(extras ...attribute.KeyValue) metric.MeasurementOption {
	if len(baseAttrs) == 0 {
		return metric.WithAttributes(extras...)
	}
	merged := make([]attribute.KeyValue, 0, len(baseAttrs)+len(extras))
	merged = append(merged, baseAttrs...)
	merged = append(merged, extras...)
	return metric.WithAttributes(merged...)
}
