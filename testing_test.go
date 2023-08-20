package otelkit_test

import (
	"context"
	"github.com/adamluzsi/otelkit"
	"github.com/adamluzsi/testcase"
	"github.com/adamluzsi/testcase/assert"
	"github.com/adamluzsi/testcase/random"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	traceSDK "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"testing"
	"time"
)

func TestDebugSpanExporter_smokeTest(t *testing.T) {
	stub := &testcase.StubTB{}
	exp := otelkit.DebugSpanExporter(stub)

	const (
		tracerName = "tracerName"
		spanName   = "spanName"
		eventName  = "eventName"
	)
	_, span := NewTracerProvider(exp).Tracer(tracerName).Start(context.Background(), spanName)
	span.AddEvent(eventName)
	span.End()

	stub.Finish()
	logs := stub.Logs.String()
	assert.NotEmpty(t, logs)
	assert.Contain(t, logs, span.SpanContext().TraceID().String())
	assert.Contain(t, logs, span.SpanContext().SpanID().String())
	assert.Contain(t, logs, tracerName)
	assert.Contain(t, logs, spanName)
	assert.Contain(t, logs, eventName)
}

func TestNewTraceProvider_smoke(t *testing.T) {
	fake := &otelkit.FakeSpanExporter{}
	tp := NewTracerProvider(fake)

	ctx, span := tp.Tracer("trace").Start(context.Background(), "span")

	spancContext := trace.SpanContextFromContext(ctx)
	assert.Equal(t, span.SpanContext(), spancContext)
	assert.True(t, spancContext.TraceID().IsValid())
	assert.True(t, spancContext.SpanID().IsValid())
}

func TestMakeSpanContext(t *testing.T) {
	s := testcase.NewSpec(t)
	s.NoSideEffect()

	parentContext := testcase.Let(s, func(t *testcase.T) context.Context {
		return context.Background()
	})
	subject := func(t *testcase.T) (context.Context, trace.SpanContext) {
		return MakeTestSpanContext(parentContext.Get(t))
	}

	thenItCreatesValidResults := func(s *testcase.Spec) {
		s.Then(`the returned context is not nil`, func(t *testcase.T) {
			ctx, _ := subject(t)
			t.Must.NotNil(ctx)
		})

		s.Then(`it yields a valid trace and span id`, func(t *testcase.T) {
			_, sc := subject(t)
			t.Must.True(sc.IsValid())
		})

		s.Then(`returned context can be used to inject with propagator`, func(t *testcase.T) {
			ctx, _ := subject(t)

			tc := propagation.TraceContext{}
			h := propagation.HeaderCarrier{}
			tc.Inject(ctx, h)

			t.Must.NotEmpty(h.Get("traceparent"))
		})

		s.Then(`the generated trace id's string encoded form matches the openTelemetry hex format`, func(t *testcase.T) {
			_, sp := subject(t)
			parsedTraceID, err := trace.TraceIDFromHex(sp.TraceID().String())
			t.Must.Nil(err)
			t.Must.Equal(sp.TraceID(), parsedTraceID)
		})

		s.Then(`the generated span id's string encoded form matches the openTelemetry hex format`, func(t *testcase.T) {
			_, sp := subject(t)
			parsedSpanID, err := trace.SpanIDFromHex(sp.SpanID().String())
			t.Must.Nil(err)
			t.Must.Equal(sp.SpanID(), parsedSpanID)
		})
	}

	s.When(`context value is nil`, func(s *testcase.Spec) {
		parentContext.Let(s, func(t *testcase.T) context.Context {
			return nil
		})

		thenItCreatesValidResults(s)
	})

	s.When(`parent context is provided`, func(s *testcase.Spec) {
		type ctxKey struct{}
		value := testcase.Let(s, func(t *testcase.T) string {
			return t.Random.String()
		})
		parentContext.Let(s, func(t *testcase.T) context.Context {
			return context.WithValue(context.Background(), ctxKey{}, value.Get(t))
		})

		thenItCreatesValidResults(s)

		s.Then(`the returned context is linked to the parent context`, func(t *testcase.T) {
			ctx, _ := subject(t)

			v, ok := ctx.Value(ctxKey{}).(string)
			t.Must.True(ok)
			t.Must.Equal(value.Get(t), v)
		})
	})
}

var _ traceSDK.SpanExporter = &otelkit.FakeSpanExporter{}

func TestFakeSpanExporter_ExportSpans(t *testing.T) {
	exp := &otelkit.FakeSpanExporter{}

	tp := NewTracerProvider(exp)
	_, span := tp.Tracer("TracerName").Start(context.Background(), "SpanName")
	span.AddEvent("EventName")
	span.End()

	assert.NotEmpty(t, exp.ExportedSpans)
	assert.NotEmpty(t, exp.Pretty(t))
	assert.Contain(t, exp.Pretty(t), "TracerName")
	assert.Contain(t, exp.Pretty(t), "SpanName")
	assert.Contain(t, exp.Pretty(t), "EventName")
}

func TestFakeSpanExporter_ExportedSpans_race(t *testing.T) {
	var (
		exp = &otelkit.FakeSpanExporter{}
		tp  = NewTracerProvider(exp)
	)
	testcase.Race(func() {
		_, span := tp.Tracer("TracerName").Start(context.Background(), "SpanName")
		span.AddEvent("EventName")
		span.End()
	}, func() {
		assert.EventuallyWithin(time.Second).Assert(t, func(it assert.It) {
			it.Must.NotEmpty(exp.ExportedSpans())
		})
	})
}

func TestStub(t *testing.T) {
	stub := otelkit.Stub(t)

	tp := otel.GetTracerProvider()
	_, span := tp.Tracer("TracerName").Start(context.Background(), "SpanName")
	span.AddEvent("EventName")
	span.End()

	exp := stub.SpanExporter
	assert.NotEmpty(t, exp.ExportedSpans)
	assert.NotEmpty(t, exp.Pretty(t))
	assert.Contain(t, exp.Pretty(t), "TracerName")
	assert.Contain(t, exp.Pretty(t), "SpanName")
	assert.Contain(t, exp.Pretty(t), "EventName")
}

func TestFakeSpanExporter_race(t *testing.T) {
	rnd := random.New(random.CryptoSeed{})
	exp := &otelkit.FakeSpanExporter{}

	tp := NewTracerProvider(exp)
	oneTPWithManyUse := func() {
		_, span := tp.Tracer("TracerName").Start(context.Background(), "SpanName")
		span.AddEvent("EventName")
		span.End()
	}

	manyTPWithSingleUse := func() {
		tp := NewTracerProvider(exp)
		tracerName := "TracerName" + rnd.StringNC(2, random.CharsetDigit())
		_, span := tp.Tracer(tracerName).Start(context.Background(), "SpanName")
		eventName := "EventName" + rnd.StringNC(2, random.CharsetDigit())
		span.AddEvent(eventName)
		span.End()
	}

	testcase.Race(
		oneTPWithManyUse, oneTPWithManyUse,
		manyTPWithSingleUse, manyTPWithSingleUse,
		func() { exp.Pretty(&testcase.StubTB{}) },
	)
}
