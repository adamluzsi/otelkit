package otelkit_test

import (
	"context"
	"fmt"
	"github.com/adamluzsi/otelkit"
	"github.com/adamluzsi/testcase"
	"github.com/adamluzsi/testcase/random"
	"go.opentelemetry.io/otel/propagation"
	traceSDK "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
)

const traceParentHeaderKey = `traceparent`

func getLastReceivedRequest(t *testcase.T, requests []*http.Request) *http.Request {
	t.Must.True(0 < len(requests), "it should have received at least one request")
	lastReceivedRequest := requests[len(requests)-1]
	return lastReceivedRequest
}

func getSpanContextFromRequest(t *testcase.T, receivedRequest *http.Request) trace.SpanContext {
	t.Helper()
	t.Log("extract the tracing from the received request")
	outCtx := propagator.Get(t).Extract(context.Background(), propagation.HeaderCarrier(receivedRequest.Header))
	sp := trace.SpanContextFromContext(outCtx)
	t.Must.True(sp.IsValid())
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("%#v", sp)
		}
	})
	return sp
}

var (
	propagator = testcase.Var[propagation.TextMapPropagator]{
		ID: "propagator",
		Init: func(t *testcase.T) propagation.TextMapPropagator {
			return propagation.TraceContext{}
		},
	}
	stubSpanExporter = testcase.Var[*otelkit.FakeSpanExporter]{
		ID: "FakeSpanExporter",
		Init: func(t *testcase.T) *otelkit.FakeSpanExporter {
			return &otelkit.FakeSpanExporter{}
		},
	}
	tracerProvider = testcase.Var[trace.TracerProvider]{
		ID: "trace.TracerProvider",
		Init: func(t *testcase.T) trace.TracerProvider {
			return NewTracerProvider(stubSpanExporter.Get(t))
		},
	}
	exampleInstrumentationName = testcase.Var[string]{
		ID: "Example instrumentation name",
		Init: func(t *testcase.T) string {
			return t.Random.String()
		},
	}
	tracer = testcase.Var[trace.Tracer]{
		ID: "trace.Tracer",
		Init: func(t *testcase.T) trace.Tracer {
			return tracerProvider.Get(t).Tracer(exampleInstrumentationName.Get(t))
		},
	}
	exampleSpanName = testcase.Var[string]{
		ID: "Example span context name",
		Init: func(t *testcase.T) string {
			return t.Random.String()
		},
	}
	spanContextConfig = testcase.Var[trace.SpanContextConfig]{
		ID: "trace.SpanContextConfig",
		Init: func(t *testcase.T) trace.SpanContextConfig {
			_, sc := MakeTestSpanContext(nil)
			return trace.SpanContextConfig{
				TraceID: sc.TraceID(),
				SpanID:  sc.SpanID(),
			}
		},
	}
)

var (
	requestBodyContent = testcase.Var[string]{
		ID: "request body Content",
		Init: func(t *testcase.T) string {
			return t.Random.String()
		},
	}
	request = testcase.Var[*http.Request]{
		ID: "*http.Request",
		Init: func(t *testcase.T) *http.Request {
			var charset = random.CharsetAlpha()
			rawPath := fmt.Sprintf("/%s", t.Random.StringNWithCharset(5, charset))
			q := url.Values{}
			q.Set(t.Random.StringNWithCharset(5, charset), t.Random.StringNWithCharset(5, charset))
			u := &url.URL{
				Scheme:      t.Random.SliceElement([]string{"http", "https"}).(string),
				Host:        fmt.Sprintf("example%d.saltpay.co", t.Random.Int()),
				Path:        rawPath,
				RawPath:     rawPath,
				RawQuery:    q.Encode(),
				Fragment:    "foo",
				RawFragment: "foo",
			}
			body := strings.NewReader(requestBodyContent.Get(t))
			return httptest.NewRequest(http.MethodGet, u.String(), body)
		},
	}
	responseRecorder = testcase.Var[*httptest.ResponseRecorder]{
		ID: "*httptest.ResponseRecorder",
		Init: func(t *testcase.T) *httptest.ResponseRecorder {
			return httptest.NewRecorder()
		},
	}
)

func MakeTestSpanContext(parent context.Context) (context.Context, trace.SpanContext) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, span := NewTracerProvider(&otelkit.FakeSpanExporter{}).
		Tracer("otelkit").
		Start(parent, "TestSpan")
	defer span.End()
	return ctx, span.SpanContext()
}

func NewTracerProvider(spanExporter traceSDK.SpanExporter) *traceSDK.TracerProvider {
	return traceSDK.NewTracerProvider(
		traceSDK.WithSpanProcessor(
			traceSDK.NewSimpleSpanProcessor(
				spanExporter)))
}
