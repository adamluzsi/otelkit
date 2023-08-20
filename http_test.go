package otelkit_test

import (
	"context"
	"errors"
	"github.com/adamluzsi/otelkit"
	"github.com/adamluzsi/testcase"
	"github.com/adamluzsi/testcase/assert"
	"github.com/adamluzsi/testcase/random"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	traceSDK "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithContextMiddleware_ServeHTTP(t *testing.T) {
	s := testcase.NewSpec(t)
	s.NoSideEffect()

	withCtxFn := testcase.Var[func(context.Context, trace.SpanContext) context.Context]{ID: "WithContextFn"}

	makeSubject := func(t *testcase.T, next http.Handler) http.Handler {
		return &otelkit.HTTPMiddlewareWithContext{
			Next:          next,
			WithContextFn: withCtxFn.Get(t),
		}
	}

	act := func(t *testcase.T) {
		makeSubject(t, stubHandler.Get(t)).ServeHTTP(responseRecorder.Get(t), request.Get(t))
	}

	s.When("function is nil", func(s *testcase.Spec) {
		withCtxFn.Let(s, func(t *testcase.T) func(context.Context, trace.SpanContext) context.Context {
			return nil // nil value
		})

		ItBehavesLikeAMiddleware(s, makeSubject)
	})

	s.When("function is provided", func(s *testcase.Spec) {
		type ctxKey struct{}
		withCtxFn.Let(s, func(t *testcase.T) func(context.Context, trace.SpanContext) context.Context {
			return func(ctx context.Context, spanContext trace.SpanContext) context.Context {
				return context.WithValue(ctx, ctxKey{}, spanContext.TraceID())
			}
		})

		ItBehavesLikeAMiddleware(s, makeSubject)

		s.And("the received request doesn't have tracing", func(s *testcase.Spec) {
			s.Before(func(t *testcase.T) {
				sc := trace.SpanContextFromContext(request.Get(t).Context())
				t.Must.False(sc.TraceID().IsValid())
				t.Must.False(sc.SpanID().IsValid())
				t.Must.False(sc.IsValid())
			})

			ItBehavesLikeAMiddleware(s, makeSubject)

			s.Then("context function is not used with the invalid tracing span context", func(t *testcase.T) {
				act(t)

				lastReceivedRequest := getLastReceivedRequest(t, stubHandler.Get(t).Requests)

				t.Must.Nil(lastReceivedRequest.Context().Value(ctxKey{}))
			})
		})

		s.And("the received request has trace", func(s *testcase.Spec) {
			GivenRequestHeaderHasTracing(s)
			ItBehavesLikeAMiddleware(s, makeSubject)

			s.Then("the populated context is forwarded to the next handler", func(t *testcase.T) {
				act(t)

			})

			s.Then("the request that the next http.Handler's receives has tracing", func(t *testcase.T) {
				act(t)

				receivedRequest := getLastReceivedRequest(t, stubHandler.Get(t).Requests)

				sp := getSpanContextFromRequest(t, receivedRequest)
				t.Must.True(sp.IsValid())
				t.Must.Equal(spanContextConfig.Get(t).TraceID, sp.TraceID(),
					"the parent tracing id should be extractable from the request headers")
			})
		})
	})
}

func TestNoTracingWarningMiddleware_ServeHTTP(t *testing.T) {
	s := testcase.NewSpec(t)
	s.NoSideEffect()

	notifyFn := testcase.Var[func(otelkit.NoTracingWarningEvent)]{ID: "notify that can be used for logging"}

	makeSubject := func(t *testcase.T, next http.Handler) http.Handler {
		return &otelkit.HTTPMiddlewareNoTracingWarning{
			Next:       next,
			Propagator: propagator.Get(t),
			NotifyFn:   notifyFn.Get(t),
		}
	}
	act := func(t *testcase.T) {
		makeSubject(t, stubHandler.Get(t)).ServeHTTP(responseRecorder.Get(t), request.Get(t))
	}

	s.When("logging function is nil", func(s *testcase.Spec) {
		notifyFn.Let(s, func(t *testcase.T) func(otelkit.NoTracingWarningEvent) {
			return nil // nil value
		})

		ItBehavesLikeAMiddleware(s, makeSubject)
	})

	s.When("logging function is provided", func(s *testcase.Spec) {
		loggedEvents := testcase.Let(s, func(t *testcase.T) []otelkit.NoTracingWarningEvent {
			return []otelkit.NoTracingWarningEvent{}
		})
		notifyFn.Let(s, func(t *testcase.T) func(otelkit.NoTracingWarningEvent) {
			return func(event otelkit.NoTracingWarningEvent) {
				loggedEvents.Set(t, append(loggedEvents.Get(t), event))
			}
		})

		ItBehavesLikeAMiddleware(s, makeSubject)

		s.And("the received request doesn't have tracing", func(s *testcase.Spec) {
			s.Before(func(t *testcase.T) {
				sc := trace.SpanContextFromContext(request.Get(t).Context())
				t.Must.False(sc.TraceID().IsValid())
				t.Must.False(sc.SpanID().IsValid())
				t.Must.False(sc.IsValid())
			})

			ItBehavesLikeAMiddleware(s, makeSubject)

			assertWarningIsMadeWithTheLoggingFunction := func(t *testcase.T) {
				events := loggedEvents.Get(t)
				t.Must.NotEmpty(events)
				t.Must.Equal(1, len(events))

				event := events[0]
				t.Must.Contain(event.MissingHeaders, "traceparent",
					"TraceContext propagator should look for the missing traceparent header")
				t.Must.NotEmpty(event.Request)
				t.Must.Equal(request.Get(t).Context(), event.Request.Context())
				t.Must.Equal(request.Get(t).Method, event.Request.Method)
				t.Must.Equal(request.Get(t).URL.String(), event.Request.URL.String())
				t.Must.Equal(request.Get(t).Header, event.Request.Header)
			}

			s.Then("it logs about the missing tracing value", func(t *testcase.T) {
				act(t)

				assertWarningIsMadeWithTheLoggingFunction(t)
			})

			s.And("the request is populated with tracing id because tracer.Start", func(s *testcase.Spec) {
				s.Before(func(t *testcase.T) {
					ctxWithTracing, span := tracer.Get(t).Start(request.Get(t).Context(), "spanName")
					t.Cleanup(func() { span.End() })
					request.Set(t, request.Get(t).WithContext(ctxWithTracing))
				})

				s.Then("the missing tracing headers still cause a warning", func(t *testcase.T) {
					act(t)

					assertWarningIsMadeWithTheLoggingFunction(t)
				})
			})
		})

		s.And("the received request has trace", func(s *testcase.Spec) {
			GivenRequestHeaderHasTracing(s)

			ItBehavesLikeAMiddleware(s, makeSubject)

			s.Then("no log is made about the missing tracing headers", func(t *testcase.T) {
				act(t)

				t.Must.Empty(loggedEvents.Get(t))
			})
		})
	})
}

func GivenRequestHeaderHasTracing(s *testcase.Spec) {
	spanContextConfig.Bind(s)
	s.Before(func(t *testcase.T) {
		t.Log("injecting tracing into the request")
		sc := trace.NewSpanContext(spanContextConfig.Get(t))
		t.Must.True(sc.IsValid())
		ctx := trace.ContextWithRemoteSpanContext(context.Background(), sc)

		t.Must.Empty(request.Get(t).Header)
		propagator.Get(t).Inject(ctx, propagation.HeaderCarrier(request.Get(t).Header))
		t.Must.NotEmpty(request.Get(t).Header)
	})
}

func ItBehavesLikeAMiddleware(s *testcase.Spec, makeHandler func(t *testcase.T, next http.Handler) http.Handler) {
	s.Context("it behaves like a middleware", func(s *testcase.Spec) {
		stubHandler.Bind(s)
		request.Bind(s)
		responseRecorder.Bind(s)

		subject := func(t *testcase.T) http.Handler {
			return makeHandler(t, stubHandler.Get(t))
		}
		act := func(t *testcase.T) {
			subject(t).ServeHTTP(responseRecorder.Get(t), request.Get(t))
		}

		s.Then("it will forward the request", func(t *testcase.T) {
			act(t)

			t.Must.Equal(1, len(stubHandler.Get(t).Requests),
				"it should have received a request")

			receivedRequest := stubHandler.Get(t).Requests[0]
			t.Must.Equal(request.Get(t).URL, receivedRequest.URL)
			t.Must.Equal(request.Get(t).Header, receivedRequest.Header)
			t.Must.Equal(request.Get(t).Method, receivedRequest.Method)

			actualBody, err := io.ReadAll(receivedRequest.Body)
			t.Must.Nil(err)
			t.Must.Equal(requestBodyContent.Get(t), string(actualBody))
		})

		s.Then("it will forward the response writer", func(t *testcase.T) {
			act(t)

			t.Must.Equal(stubHandler.Get(t).ExpectedResponseCode, responseRecorder.Get(t).Code)
		})
	})
}

var stubHandler = testcase.Var[*StubHandler]{
	ID: "stub HTTP handler",
	Init: func(t *testcase.T) *StubHandler {
		return &StubHandler{ExpectedResponseCode: t.Random.SliceElement([]int{
			http.StatusOK,
			http.StatusAccepted,
			http.StatusBadRequest,
			http.StatusTeapot,
			http.StatusInternalServerError,
		}).(int)}
	},
}

type StubHandler struct {
	Requests             []*http.Request
	ExpectedResponseCode int
}

func (s *StubHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Requests = append(s.Requests, r)
	w.WriteHeader(s.ExpectedResponseCode)
}

// TestHighLevel mimics an environment where a microservice,
// where inbound request contains a traceparent,
// and outbound request propagates the parent trace id.
func TestHighLevel(tt *testing.T) {
	t := testcase.NewT(tt, nil)

	ctx, sp := MakeTestSpanContext(nil)
	t.Must.True(sp.IsValid())
	tID := sp.TraceID()
	t.Log("parent TraceID:", tID.String())

	nextMicroService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actualTraceHeader := r.Header.Get(traceParentHeaderKey)
		t.Should.NotEmpty(actualTraceHeader, "we should have a tracing id received in the request")
		t.Should.Contain(actualTraceHeader, tID.String(), "the initial parent tracing ID should be present")
		w.WriteHeader(http.StatusTeapot)
	}))
	defer nextMicroService.Close()

	c := nextMicroService.Client()
	c.Transport = otelkit.HTTPRoundTripper{
		Next:       c.Transport,
		Propagator: propagator.Get(t),
		Tracer:     tracer.Get(t),
		SpanNameFn: func(r *http.Request) string {
			return exampleSpanName.Get(t)
		},
	}

	type ctxKey struct{}

	myMicroService := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Log("myMicroService traceID", trace.SpanContextFromContext(r.Context()).TraceID().String())
		t.Log("myMicroService spanID", trace.SpanContextFromContext(r.Context()).SpanID().String())

		ctxTraceID := trace.SpanFromContext(r.Context()).SpanContext().TraceID().String()
		t.Must.Equal(tID.String(), ctxTraceID, "otelhttp middleware works")
		t.Must.Equal(tID.String(), r.Context().Value(ctxKey{}).(string), "with context middleware works")

		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, nextMicroService.URL+"/foo/bar/baz", strings.NewReader("Hello, world!"))
		t.Must.Nil(err)
		resp, err := c.Do(req)
		t.Must.Nil(err)
		t.Must.Equal(http.StatusTeapot, resp.StatusCode)

		w.WriteHeader(http.StatusTeapot)
	})

	withCtxMW := otelkit.HTTPMiddlewareWithContext{
		Next: myMicroService,
		WithContextFn: func(ctx context.Context, sc trace.SpanContext) context.Context {
			return context.WithValue(ctx, ctxKey{}, sc.TraceID().String())
		},
	}

	otelHTTPHandler := otelhttp.NewHandler(withCtxMW, "server",
		otelhttp.WithPropagators(propagator.Get(t)),
		otelhttp.WithTracerProvider(tracerProvider.Get(t)))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/baz/bar/foo", nil)
	propagator.Get(t).Inject(ctx, propagation.HeaderCarrier(req.Header)) // add tracing to the header
	t.Logf("%#v", req.Header)

	otelHTTPHandler.ServeHTTP(rr, req)
	t.Must.Equal(http.StatusTeapot, rr.Code)
}

func TestHTTPRoundTripper(t *testing.T) {
	s := testcase.NewSpec(t)
	s.NoSideEffect()

	nextRoundTripper := testcase.Let(s, func(t *testcase.T) *StubRoundTripper {
		return &StubRoundTripper{Response: &http.Response{StatusCode: http.StatusOK + t.Random.IntN(10)}}
	})
	spanNameFn := testcase.Var[func(*http.Request) string]{ID: "span name function"}

	makeSubject := func(t *testcase.T, next http.RoundTripper) http.RoundTripper {
		return otelkit.HTTPRoundTripper{
			Next:       next,
			Propagator: propagator.Get(t),
			Tracer:     tracer.Get(t),
			SpanNameFn: spanNameFn.Get(t),
		}
	}
	subject := func(t *testcase.T) otelkit.HTTPRoundTripper {
		return makeSubject(t, nextRoundTripper.Get(t)).(otelkit.HTTPRoundTripper)
	}
	act := func(t *testcase.T) (*http.Response, error) {
		return subject(t).RoundTrip(request.Get(t))
	}
	onSuccess := func(t *testcase.T) *http.Response {
		resp, err := subject(t).RoundTrip(request.Get(t))
		t.Must.Nil(err)
		return resp
	}

	s.When("span name function is absent", func(s *testcase.Spec) {
		spanNameFn.Let(s, func(t *testcase.T) func(*http.Request) string {
			t.Log("Given no span name generator function is provided")
			return nil
		})

		ItBehavesLikeARoundTripper(s, makeSubject)

		s.Then("it uses a hard-coded fallback span name", func(t *testcase.T) {
			onSuccess(t)

			exportedSpans := stubSpanExporter.Get(t).ExportedSpans()
			t.Must.NotEmpty(exportedSpans)
		})

		ThenItExportsTheRequestAttributes(s, act)
	})

	s.When("span function is provided", func(s *testcase.Spec) {
		s.Before(func(t *testcase.T) {
			t.Skip()
		})
		spanNameFn.Let(s, func(t *testcase.T) func(*http.Request) string {
			t.Log("Given that span name generator function is provided")
			return func(r *http.Request) string {
				t.Must.Equal(request.Get(t), r)
				//t.Must.Equal(request.Get(t).URL.Path, r.URL.Path, "request ")
				return exampleSpanName.Get(t)
			}
		})

		ThenItExportsTheRequestAttributes(s, act)

		s.And("error occurs in the next round tripper", func(s *testcase.Spec) {
			expectedErr := errors.New("boom")

			s.Before(func(t *testcase.T) {
				nextRoundTripper.Get(t).Err = expectedErr
			})

			s.Then("error is returned", func(t *testcase.T) {
				_, err := act(t)
				t.Must.ErrorIs(expectedErr, err)
			})
		})

		s.And("the received request has trace", func(s *testcase.Spec) {
			GivenRequestContextHasTracing(s)

			s.Then("the outbound request is propagating the tracing context", func(t *testcase.T) {
				_, err := act(t)
				t.Must.Nil(err)
				receivedRequest := getLastReceivedRequest(t, nextRoundTripper.Get(t).Requests)

				sp := getSpanContextFromRequest(t, receivedRequest)
				t.Must.True(sp.IsValid())
				t.Must.Equal(spanContextConfig.Get(t).TraceID.String(), sp.TraceID().String(),
					"the parent tracing id should be extractable from the request headers")
			})

			s.Then("the oubound request is part of a newly made span context", func(t *testcase.T) {
				_, err := act(t)
				t.Must.Nil(err)
				receivedRequest := getLastReceivedRequest(t, nextRoundTripper.Get(t).Requests)

				sp := getSpanContextFromRequest(t, receivedRequest)
				t.Must.True(sp.IsValid())
				t.Must.True(sp.SpanID().IsValid())
				t.Must.NotEqual(spanContextConfig.Get(t).SpanID.String(), sp.SpanID().String(),
					"the received span id belongs to a new span context")
			})

			s.Then("after the request, trace is exported", func(t *testcase.T) {
				_, err := act(t)
				t.Must.Nil(err)

				t.Eventually(func(it assert.It) {
					exportedSpans := stubSpanExporter.Get(t).ExportedSpans()
					it.Must.True(0 < len(exportedSpans))
					lastSpan := exportedSpans[len(exportedSpans)-1]
					it.Logf("%#v", lastSpan)
				})
			})

			s.And("the next round tripper encounters an unrecoverable error", func(s *testcase.Spec) {
				s.Before(func(t *testcase.T) {
					nextRoundTripper.Get(t).DoPanic = true
				})

				s.Then("panic is not swallowed up", func(t *testcase.T) {
					t.Must.Panic(func() { _, _ = act(t) })
				})

				s.Then("span is still cleaned up and exported", func(t *testcase.T) {
					t.Must.Panic(func() { _, _ = act(t) })

					t.Eventually(func(it assert.It) {
						exportedSpans := stubSpanExporter.Get(t).ExportedSpans()
						it.Must.True(0 < len(exportedSpans))
						lastSpan := exportedSpans[len(exportedSpans)-1]
						it.Logf("%#v", lastSpan)
					})
				})
			})
		})
	})
}

func ThenItExportsTheRequestAttributes(s *testcase.Spec, act func(t *testcase.T) (*http.Response, error)) {
	s.Then("it exports the request attributes", func(t *testcase.T) {
		_, err := act(t)
		t.Must.Nil(err)

		exportOut := stubSpanExporter.Get(t).Pretty(t)
		t.Must.Contain(exportOut, "http-request")
		t.Must.Contain(exportOut, request.Get(t).Method)
		t.Must.Contain(exportOut, request.Get(t).URL.Path)
		t.Must.Contain(exportOut, request.Get(t).URL.Scheme)
		t.Must.Contain(exportOut, request.Get(t).URL.Host)
	})
}

func ItBehavesLikeARoundTripper(s *testcase.Spec, makeRT func(t *testcase.T, tripper http.RoundTripper) http.RoundTripper) {
	s.Context(`it behaves as a round-tripper`, func(s *testcase.Spec) {
		stub := testcase.Let(s, func(t *testcase.T) *StubRoundTripper {
			return &StubRoundTripper{
				Response: &http.Response{
					StatusCode: t.Random.SliceElement([]int{
						http.StatusOK,
						http.StatusTeapot,
						http.StatusInternalServerError,
					}).(int),
				},
			}
		})
		subject := func(t *testcase.T) http.RoundTripper {
			return makeRT(t, stub.Get(t))
		}
		act := func(t *testcase.T) (*http.Response, error) {
			return subject(t).RoundTrip(request.Get(t))
		}

		s.Test("round tripper act as a middleware in the round trip pipeline", func(t *testcase.T) {
			response, err := act(t)
			t.Must.Nil(err)

			t.Log(stub.Get(t).Response)

			t.Must.Equal(stub.Get(t).Response.StatusCode, response.StatusCode)
		})

		s.Test("the next round tripper receives the request", func(t *testcase.T) {
			_, err := act(t)
			t.Must.Nil(err)
			t.Must.Equal(1, len(stub.Get(t).Requests), "it should have received a request")
			receivedRequest := stub.Get(t).Requests[0]
			// just some sanity check
			t.Must.Equal(request.Get(t).URL, receivedRequest.URL)
			t.Must.Equal(request.Get(t).Header, receivedRequest.Header)
			t.Must.Equal(request.Get(t).Method, receivedRequest.Method)

			actualBody, err := io.ReadAll(receivedRequest.Body)
			t.Must.Nil(err)
			t.Must.Equal(requestBodyContent.Get(t), string(actualBody))
		})
	})
}

func GivenRequestContextHasTracing(s *testcase.Spec) {
	s.Before(func(t *testcase.T) {
		// creates a recording Span with trace.Tracer.Start
		ctxWithTracing, span := tracer.Get(t).Start(request.Get(t).Context(), exampleSpanName.Get(t))
		t.Cleanup(func() { span.End() })

		sc := span.SpanContext()
		t.Must.True(sc.IsValid())
		spanContextConfig.Set(t, trace.SpanContextConfig{
			TraceID:    sc.TraceID(),
			SpanID:     sc.SpanID(),
			TraceFlags: sc.TraceFlags(),
			TraceState: sc.TraceState(),
			Remote:     sc.IsRemote(),
		})

		t.Log("given request's context has tracing")
		request.Set(t, request.Get(t).WithContext(ctxWithTracing))
		t.Must.Equal(sc.TraceID(), trace.SpanContextFromContext(request.Get(t).Context()).TraceID())
		t.Must.Equal(spanContextConfig.Get(t).TraceID, sc.TraceID())
	})
}

type StubRoundTripper struct {
	Response *http.Response
	Err      error

	Requests []*http.Request
	DoPanic  bool
}

func (f *StubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.Requests = append(f.Requests, req)
	if f.DoPanic {
		panic("oh no!")
	}
	return f.Response, f.Err
}

func TestContextWithBaggage_smoke(t *testing.T) {
	rnd := random.New(random.CryptoSeed{})

	t.Run("happy", func(t *testing.T) {
		stub := otelkit.Stub(t)

		ctx, span := stub.TracerProvider.Tracer("tracer").Start(context.Background(), "spanName")

		b := baggage.FromContext(ctx)
		assert.Equal(t, 0, b.Len())

		key := rnd.StringNC(8, random.CharsetAlpha())
		val := rnd.StringNC(8, random.CharsetAlpha())
		prpkey := rnd.StringNC(5, random.CharsetAlpha())

		keyProperty, err := baggage.NewKeyProperty(prpkey)
		assert.NoError(t, err)
		member1, err := baggage.NewMember(key, val, keyProperty)
		assert.NoError(t, err)

		ctx, err = otelkit.ContextWithBaggage(ctx, member1)
		assert.NoError(t, err)

		ctx, err = otelkit.ContextWithBaggage(ctx, func() (baggage.Member, error) {
			return baggage.NewMember("m2-key", "m2-value")
		})
		assert.NoError(t, err)

		span.End()

		t.Log(stub.SpanExporter.Pretty(t))

		assert.OneOf(t, stub.SpanExporter.ExportedSpans(), func(it assert.It, got traceSDK.ReadOnlySpan) {

		})

	})

	t.Run("rainy", func(t *testing.T) {
		expErr := rnd.Error()
		ctx := context.Background()

		_, err := otelkit.ContextWithBaggage(ctx, func() (baggage.Member, error) {
			return baggage.Member{}, expErr
		})
		assert.ErrorIs(t, expErr, err)

		_, err = otelkit.ContextWithBaggage(ctx, baggage.Member{})
		assert.Error(t, expErr, err)
	})
}
