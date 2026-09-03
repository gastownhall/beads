package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The instrument wrappers below exist so an unstamped measurement is
// unwritable rather than merely discouraged.
//
// WithMergedAttrs is opt-in per call site: Add(ctx, 1) compiles just as
// happily as Add(ctx, 1, WithMergedAttrs()), and the compiler cannot tell the
// two apart. A single missed call site drops bd.prefix from that series and
// silently re-collides it across projects. Holding the instruments as Counter,
// Histogram and Gauge instead of their metric.* equivalents removes the choice
// — there is no Add or Record that skips the merge, and a raw
// metric.WithAttributes argument no longer compiles.
//
// The constructors swallow instrument-creation errors the way the raw OTel
// calls did before them, leaving a no-op instrument. Telemetry is opt-in and
// must never block the user, so a broken instrument stays silent.

// Counter is an Int64Counter that stamps the process-wide base attributes
// (see BaseAttrs) on every measurement.
type Counter struct{ inner metric.Int64Counter }

// NewCounter creates a Counter on m.
func NewCounter(m metric.Meter, name string, opts ...metric.Int64CounterOption) Counter {
	c, _ := m.Int64Counter(name, opts...)
	return Counter{inner: c}
}

// Add records an increment with extras merged onto the base attributes.
func (c Counter) Add(ctx context.Context, incr int64, extras ...attribute.KeyValue) {
	if c.inner == nil {
		return
	}
	c.inner.Add(ctx, incr, WithMergedAttrs(extras...))
}

// Histogram is a Float64Histogram that stamps the process-wide base
// attributes on every measurement.
type Histogram struct{ inner metric.Float64Histogram }

// NewHistogram creates a Histogram on m.
func NewHistogram(m metric.Meter, name string, opts ...metric.Float64HistogramOption) Histogram {
	h, _ := m.Float64Histogram(name, opts...)
	return Histogram{inner: h}
}

// Record records a value with extras merged onto the base attributes.
func (h Histogram) Record(ctx context.Context, value float64, extras ...attribute.KeyValue) {
	if h.inner == nil {
		return
	}
	h.inner.Record(ctx, value, WithMergedAttrs(extras...))
}

// Gauge is an Int64Gauge that stamps the process-wide base attributes on
// every measurement.
type Gauge struct{ inner metric.Int64Gauge }

// NewGauge creates a Gauge on m.
func NewGauge(m metric.Meter, name string, opts ...metric.Int64GaugeOption) Gauge {
	g, _ := m.Int64Gauge(name, opts...)
	return Gauge{inner: g}
}

// Record records a value with extras merged onto the base attributes.
func (g Gauge) Record(ctx context.Context, value int64, extras ...attribute.KeyValue) {
	if g.inner == nil {
		return
	}
	g.inner.Record(ctx, value, WithMergedAttrs(extras...))
}
