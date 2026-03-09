package tracing

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.20.0"
)

func InitTracer(serviceName, version string) (*sdktrace.TracerProvider, error) {
	ctx := context.Background()

	env := os.Getenv("DEPLOY_ENV")
	if env == "" {
		env = "development"
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
			semconv.DeploymentEnvironment(env),
		),
	)
	if err != nil {
		return nil, err
	}

	var tp *sdktrace.TracerProvider

	switch {
	case os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "":
		exp, expErr := otlptracehttp.New(ctx)
		if expErr != nil {
			return nil, expErr
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(res),
		)

	case os.Getenv("OTEL_EXPORTER_STDOUT") == "true":
		exp, expErr := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if expErr != nil {
			return nil, expErr
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(res),
		)

	default:
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
		)
	}

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp, nil
}
