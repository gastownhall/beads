package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// buildOTLPMetricExporter creates an HTTP/protobuf OTLP metric exporter.
// url is a full HTTP URL, e.g. http://localhost:8428/opentelemetry/api/v1/push
// (VictoriaMetrics format). Compatible with any OTLP HTTP metric receiver.
func buildOTLPMetricExporter(ctx context.Context, url string) (sdkmetric.Exporter, error) {
	return otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(url))
}
