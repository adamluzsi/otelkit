package otelkit

import (
	"bytes"
	"context"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	traceSDK "go.opentelemetry.io/otel/sdk/trace"
	"io"
	"sync"
)

type testingTB interface {
	Helper()
	Cleanup(func())
	Logf(format string, args ...any)
	Fatalf(format string, args ...any)
	Errorf(format string, args ...any)
}

type Stubs struct {
	SpanExporter   *FakeSpanExporter
	TracerProvider *traceSDK.TracerProvider
}

func Stub(tb testingTB) *Stubs {
	tb.Helper()

	ogTP := otel.GetTracerProvider()
	tb.Cleanup(func() { otel.SetTracerProvider(ogTP) }) // restore OG TraceProvider

	spanExporter := &FakeSpanExporter{}

	tracerProvider := traceSDK.NewTracerProvider(
		traceSDK.WithSpanProcessor(
			traceSDK.NewSimpleSpanProcessor(
				spanExporter)))

	otel.SetTracerProvider(tracerProvider)

	return &Stubs{
		SpanExporter:   spanExporter,
		TracerProvider: tracerProvider,
	}

}

func DebugSpanExporter(tb testingTB) traceSDK.SpanExporter {
	tb.Helper()
	buf := &bytes.Buffer{}
	tb.Cleanup(func() {
		tb.Helper()
		tb.Logf("\n%s", buf.String())
	})
	return prettyPrintExporter(tb, buf)
}

func prettyPrintExporter(tb testingTB, writer io.Writer) traceSDK.SpanExporter {
	tb.Helper()
	se, err := stdouttrace.New(
		stdouttrace.WithWriter(writer),
		// Use human-readable output.
		stdouttrace.WithPrettyPrint(),
		// Do not print timestamps for the demo.
		stdouttrace.WithoutTimestamps(),
	)
	if err != nil {
		tb.Fatalf("expected no error but got: %v", err)
	}
	tb.Cleanup(func() {
		if err := se.Shutdown(context.Background()); err != nil {
			tb.Errorf("%v", err)
		}
	})
	return se
}

type FakeSpanExporter struct {
	spans []traceSDK.ReadOnlySpan

	m sync.Mutex
}

func (exp *FakeSpanExporter) ExportSpans(ctx context.Context, spans []traceSDK.ReadOnlySpan) error {
	exp.m.Lock()
	defer exp.m.Unlock()
	exp.spans = append(exp.spans, spans...)
	return nil
}

func (exp *FakeSpanExporter) ExportedSpans() []traceSDK.ReadOnlySpan {
	exp.m.Lock()
	defer exp.m.Unlock()
	return append([]traceSDK.ReadOnlySpan{}, exp.spans...)
}

func (exp *FakeSpanExporter) Shutdown(ctx context.Context) error { return nil }

func (exp *FakeSpanExporter) Pretty(tb testingTB) string {
	exp.m.Lock()
	defer exp.m.Unlock()
	tb.Helper()
	buf := &bytes.Buffer{}
	if err := prettyPrintExporter(tb, buf).ExportSpans(context.Background(), exp.spans); err != nil {
		tb.Fatalf("%s", err.Error())
	}
	return buf.String()
}
