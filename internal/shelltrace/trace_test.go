// SPDX-License-Identifier: AGPL-3.0-only

package shelltrace

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func TestRecordConnectionPhases(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})

	created := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	delivered := created.Add(200 * time.Millisecond)
	claimed := created.Add(1200 * time.Millisecond)
	connected := created.Add(3200 * time.Millisecond)
	session := &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{
			UID: types.UID("session-1"), CreationTimestamp: metav1.NewTime(created),
			Annotations: map[string]string{
				computev1alpha.InstanceConsoleSessionDeliveredAtAnnotation: delivered.Format(time.RFC3339Nano),
				computev1alpha.InstanceConsoleSessionCellAnnotation:        "cell-1",
				computev1alpha.InstanceConsoleSessionLocationAnnotation:    "us-east-1",
			},
		},
		Status: computev1alpha.InstanceConsoleSessionStatus{
			ClaimedAt: &metav1.Time{Time: claimed},
			StartedAt: &metav1.Time{Time: connected},
		},
	}
	RecordConnection(session, "")
	spans := exporter.GetSpans()
	if len(spans) != 4 {
		t.Fatalf("got %d spans, want 4", len(spans))
	}
	want := map[string][2]time.Time{
		"shell.session.connect":        {created, connected},
		"shell.session.deliver":        {created, delivered},
		"shell.session.claim":          {delivered, claimed},
		"shell.session.client_connect": {claimed, connected},
	}
	for _, span := range spans {
		times, ok := want[span.Name]
		if !ok || !span.StartTime.Equal(times[0]) || !span.EndTime.Equal(times[1]) {
			t.Errorf("unexpected span %q: %s to %s", span.Name, span.StartTime, span.EndTime)
		}
	}
}

func TestRecordConnectionFailureWithoutClaim(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})
	created := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	ended := created.Add(30 * time.Second)
	session := &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)},
		Status:     computev1alpha.InstanceConsoleSessionStatus{EndedAt: &metav1.Time{Time: ended}},
	}
	RecordConnection(session, computev1alpha.InstanceConsoleSessionReasonUnavailable)
	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	if spans[0].Name != "shell.session.unattributed" {
		t.Errorf("missing timestamps should produce an unattributed phase, got %q", spans[0].Name)
	}
	for _, span := range spans {
		if span.Name == "shell.session.connect" {
			if span.Status.Code != codes.Error || !span.EndTime.Equal(ended) {
				t.Errorf("failure span has status %v and end %s", span.Status, span.EndTime)
			}
		}
	}
}

func TestTraceContextCrossesSessionCopy(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})
	created := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	project := &computev1alpha.InstanceConsoleSession{ObjectMeta: metav1.ObjectMeta{
		UID: types.UID("session-1"), CreationTimestamp: metav1.NewTime(created),
		Annotations: map[string]string{},
	}}
	_, delivery, traceparent := StartDelivery(t.Context(), project, true)
	if traceparent == "" {
		t.Fatal("delivery did not inject traceparent")
	}
	project.Annotations[computev1alpha.InstanceConsoleSessionTraceParentAnnotation] = traceparent
	delivery.End()
	cell := project.DeepCopy()
	cell.Labels = map[string]string{computev1alpha.InstanceConsoleSessionUIDLabel: string(project.UID)}
	_, agent := StartAgentSpan(t.Context(), cell, "shell.session.agent.claim")
	agent.End()
	connected := created.Add(3 * time.Second)
	project.Status.StartedAt = &metav1.Time{Time: connected}
	RecordConnection(project, "")

	spans := exporter.GetSpans()
	var deliverySpan, agentSpan, connectSpan tracetest.SpanStub
	for _, span := range spans {
		switch span.Name {
		case "shell.session.delivery":
			deliverySpan = span
		case "shell.session.agent.claim":
			agentSpan = span
		case "shell.session.connect":
			connectSpan = span
		}
	}
	if !deliverySpan.SpanContext.IsValid() ||
		agentSpan.Parent.SpanID() != deliverySpan.SpanContext.SpanID() ||
		connectSpan.Parent.SpanID() != deliverySpan.SpanContext.SpanID() ||
		agentSpan.SpanContext.TraceID() != deliverySpan.SpanContext.TraceID() ||
		connectSpan.SpanContext.TraceID() != deliverySpan.SpanContext.TraceID() {
		t.Fatalf("session trace context did not link delivery, agent, and connection spans")
	}
}
