//go:build !wasm

package otel

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func broflakeTracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer("broflake")
}

// SendNATBehaviorTelemetry sends a single span annotated with the NAT summary inferred from ICE
// gathering.
func SendNATBehaviorTelemetry(summary NATSummary, spanName string) {
	_, span := broflakeTracer().Start(
		context.Background(),
		spanName,
		trace.WithAttributes(
			attribute.Bool("nat_is_natted", summary.IsNatted),
			attribute.String("nat_mapping_behavior", summary.MappingBehavior),
			attribute.String("nat_filtering_behavior", summary.FilteringBehavior),
			attribute.Bool("nat_port_preservation", summary.PortPreservation),
			attribute.String("nat_type", summary.NATType),
			attribute.String("nat_external_ip", summary.ExternalIP),
		),
	)
	span.End()
}
