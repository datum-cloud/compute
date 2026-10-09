// SPDX-License-Identifier: AGPL-3.0-only

// Package shelltrace reconstructs the asynchronous connection path from the
// timestamps written by the project controller and cell agent. Only the
// management-plane controller exports spans; cells need no telemetry egress.
package shelltrace

import (
	"context"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	tracerName           = "go.datum.net/compute/shell-session"
	connectSpanName      = "shell.session.connect"
	unattributedSpanName = "shell.session.unattributed"
)

// StartDelivery starts the trace at the project controller's delivery attempt.
// Its traceparent travels on the session object to the hub and cell.
func StartDelivery(ctx context.Context, session *computev1alpha.InstanceConsoleSession, first bool) (context.Context, trace.Span, string) {
	options := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithAttributes(attribute.String("session.uid", string(session.UID)))}
	if first && !session.CreationTimestamp.IsZero() {
		options = append(options, trace.WithTimestamp(session.CreationTimestamp.Time))
	}
	ctx, span := otel.Tracer(tracerName).Start(ctx, "shell.session.delivery", options...)
	carrier := propagation.MapCarrier{}
	(propagation.TraceContext{}).Inject(ctx, carrier)
	return ctx, span, carrier.Get("traceparent")
}

// ParentContext extracts the controller's span context from a session copy.
// Karmada preserves this annotation while propagating the object to a cell.
func ParentContext(ctx context.Context, session *computev1alpha.InstanceConsoleSession) context.Context {
	if session.Annotations == nil {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": session.Annotations[computev1alpha.InstanceConsoleSessionTraceParentAnnotation]}
	return (propagation.TraceContext{}).Extract(ctx, carrier)
}

// StartAgentSpan records work performed by a cell agent as a child of the
// controller span, even though reconciliation runs in another process.
func StartAgentSpan(ctx context.Context, session *computev1alpha.InstanceConsoleSession, name string) (context.Context, trace.Span) {
	uid := session.Labels[computev1alpha.InstanceConsoleSessionUIDLabel]
	return otel.Tracer(tracerName).Start(ParentContext(ctx, session), name,
		trace.WithAttributes(attribute.String("session.uid", uid)))
}

// Init configures OTLP only when its endpoint is set. Processes without an
// endpoint keep the default no-op provider.
func Init(ctx context.Context, service string) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", service))),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

// RecordConnection emits one trace when the activity event for a connection
// (or an attempt that ended before connection) is recorded. These timestamps
// cross Kubernetes watches and Karmada, where an in-memory parent span cannot.
// Missing or out-of-order phase timestamps become unattributed time rather
// than invented phase durations.
func RecordConnection(session *computev1alpha.InstanceConsoleSession, endReason string) {
	start := session.CreationTimestamp.Time
	if start.IsZero() {
		return
	}
	var end time.Time
	if session.Status.StartedAt != nil {
		end = session.Status.StartedAt.Time
	} else if session.Status.EndedAt != nil {
		end = session.Status.EndedAt.Time
	} else {
		end = time.Now()
	}
	if end.Before(start) {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("session.uid", string(session.UID)),
		attribute.String("cell", session.Annotations[computev1alpha.InstanceConsoleSessionCellAnnotation]),
		attribute.String("location", session.Annotations[computev1alpha.InstanceConsoleSessionLocationAnnotation]),
	}
	ctx, root := otel.Tracer(tracerName).Start(ParentContext(context.Background(), session), connectSpanName,
		trace.WithTimestamp(start), trace.WithAttributes(attrs...))
	defer root.End(trace.WithTimestamp(end))
	if endReason != "" {
		root.SetAttributes(attribute.String("end.reason", endReason))
		if session.Status.StartedAt == nil {
			root.SetStatus(codes.Error, endReason)
		}
	}

	last := start
	hasDelivery := false
	delivered, err := time.Parse(time.RFC3339Nano,
		session.Annotations[computev1alpha.InstanceConsoleSessionDeliveredAtAnnotation])
	if err == nil && !delivered.Before(last) && !delivered.After(end) {
		phase(ctx, "shell.session.deliver", last, delivered)
		last = delivered
		hasDelivery = true
	}
	hasClaim := false
	if session.Status.ClaimedAt != nil {
		claimed := session.Status.ClaimedAt.Time
		if !claimed.Before(last) && !claimed.After(end) {
			name := unattributedSpanName
			if hasDelivery {
				name = "shell.session.claim"
			}
			phase(ctx, name, last, claimed)
			last = claimed
			hasClaim = true
		}
	}
	name := unattributedSpanName
	if hasClaim {
		name = "shell.session.client_connect"
	}
	phase(ctx, name, last, end)
}

func phase(ctx context.Context, name string, start, end time.Time) {
	_, span := otel.Tracer(tracerName).Start(ctx, name, trace.WithTimestamp(start))
	span.End(trace.WithTimestamp(end))
}
