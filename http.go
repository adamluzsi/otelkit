package otelkit

import (
	"context"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.7.0"
	"go.opentelemetry.io/otel/trace"
	"net/http"
)

type HTTPMiddlewareWithContext struct {
	Next          http.Handler
	WithContextFn func(context.Context, trace.SpanContext) context.Context
}

func (mw HTTPMiddlewareWithContext) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sp := trace.SpanContextFromContext(ctx)
	if sp.IsValid() && mw.WithContextFn != nil {
		ctx = mw.WithContextFn(ctx, sp)
	}
	mw.Next.ServeHTTP(w, r.WithContext(ctx))
}

type HTTPMiddlewareNoTracingWarning struct {
	Next       http.Handler
	Propagator propagation.TextMapPropagator
	NotifyFn   func(NoTracingWarningEvent)
}

type NoTracingWarningEvent struct {
	MissingHeaders []string
	Request        *http.Request
}

func (mw HTTPMiddlewareNoTracingWarning) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	spy := &spyHeaderCarrier{HeaderCarrier: propagation.HeaderCarrier(r.Header)}
	sp := trace.SpanContextFromContext(mw.Propagator.Extract(context.Background(), spy))
	if !sp.IsValid() {
		mw.notify(r, spy.MissingHeaders)
	}

	mw.Next.ServeHTTP(w, r)
}

func (mw HTTPMiddlewareNoTracingWarning) notify(r *http.Request, missingHeaders []string) {
	if mw.NotifyFn == nil {
		return
	}
	mw.NotifyFn(NoTracingWarningEvent{
		MissingHeaders: missingHeaders,
		Request:        r.Clone(r.Context()),
	})
}

type spyHeaderCarrier struct {
	propagation.HeaderCarrier
	MissingHeaders []string
}

func (s *spyHeaderCarrier) Get(key string) string {
	value := s.HeaderCarrier.Get(key)
	if value == "" {
		s.MissingHeaders = append(s.MissingHeaders, key)
	}
	return value
}

type HTTPRoundTripper struct {
	Next       http.RoundTripper
	Propagator propagation.TextMapPropagator
	Tracer     trace.Tracer
	SpanNameFn func(r *http.Request) string
}

const defaultSpanName = "http-request"

func (r HTTPRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	spanStartOptions := []trace.SpanStartOption{
		trace.WithAttributes(
			semconv.HTTPMethodKey.String(request.Method),
			semconv.HTTPURLKey.String(request.URL.String()),
			semconv.HTTPSchemeKey.String(request.URL.Scheme),
			semconv.HTTPHostKey.String(request.Host)),
		trace.WithSpanKind(trace.SpanKindClient),
	}

	spanName := defaultSpanName
	if r.SpanNameFn != nil {
		spanName = r.SpanNameFn(request)
	}

	ctx, span := r.Tracer.Start(request.Context(), spanName, spanStartOptions...)
	defer span.End()

	r.Propagator.Inject(ctx, propagation.HeaderCarrier(request.Header))
	return r.Next.RoundTrip(request.WithContext(ctx))
}

func ContextWithBaggage[Member baggage.Member | func() (baggage.Member, error)](
	ctx context.Context, CorrelationContextData ...Member) (context.Context, error) {

	b := baggage.FromContext(ctx)
	var err error
	for _, v := range CorrelationContextData {
		switch m := any(v).(type) {
		case baggage.Member:
			b, err = b.SetMember(m)
			if err != nil {
				return nil, err
			}
		case func() (baggage.Member, error):
			member, err := m()
			if err != nil {
				return nil, err
			}

			b, err = b.SetMember(member)
			if err != nil {
				return nil, err
			}
		}
	}
	return baggage.ContextWithBaggage(ctx, b), nil
}
