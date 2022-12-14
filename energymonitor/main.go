package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/justinsb/experiments-slog/energymonitor/attrs"
	"github.com/justinsb/experiments-slog/energymonitor/kslog"

	"golang.org/x/exp/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
	"go.opentelemetry.io/otel/trace"
)

var tracer = kslog.Tracer("energymonitor")

// Initializes an OTLP exporter, and configures the corresponding trace and
// metric providers.
func initProvider(otelEndpoint string) (func(), error) {
	ctx := context.Background()

	log := slog.FromContext(ctx)

	log.Info("configuring opentelemetry", slog.String("otel.endpoint", otelEndpoint))

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String("energymonitor"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create opentelemetry resource: %w", err)
	}

	conn, err := grpc.DialContext(ctx, otelEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to create GRPC connection to opentelemetry collector %q: %w", otelEndpoint, err)
	}

	// Set up a trace exporter
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, fmt.Errorf("failed to create opentelemetry trace exporter: %w", err)
	}

	// Register the trace exporter with a TracerProvider, using a batch
	// span processor to aggregate spans before export.
	bsp := sdktrace.NewBatchSpanProcessor(traceExporter)
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
	)
	otel.SetTracerProvider(tracerProvider)

	// set global propagator to tracecontext (the default is no-op).
	otel.SetTextMapPropagator(propagation.TraceContext{})

	metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, fmt.Errorf("error creating opentelemetry metric exporter: %w", err)
	}

	metricReader := metric.NewPeriodicReader(metricExporter)
	meterProvider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metricReader),
	)
	global.SetMeterProvider(meterProvider)

	return func() {
		if err := meterProvider.Shutdown(context.Background()); err != nil {
			log.Error("failed to shutdown opentelemetry metric provider", err)
		}
		if err := tracerProvider.Shutdown(context.Background()); err != nil {
			log.Error("failed to shutdown opentelemetry tracer provider", err)
		}
	}, nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func installHooks() error {
	client := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
	http.DefaultClient = client

	return nil
}

func run(ctx context.Context) error {
	kslog.InitFlags(nil)
	flag.Parse()

	shutdown, err := initProvider(os.Getenv("OTEL_ENDPOINT"))
	if err != nil {
		return fmt.Errorf("failed to initialize otel provider: %w", err)
	}
	defer shutdown()

	if err := installHooks(); err != nil {
		return err
	}

	reader, err := NewMeterReader()
	if err != nil {
		return fmt.Errorf("error from NewMeterReader: %w", err)
	}

	readMeterForever(ctx, reader)

	return nil
}

func readMeterForever(ctx context.Context, reader *MeterReader) error {
	interval := 1 * time.Minute
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			ticker.Reset(interval)
			if err := readMeterOnce(ctx, reader); err != nil {
				slog.Error("error reading meter", err)
			}
		}
	}
}

func readMeterOnce(ctx context.Context, reader *MeterReader) error {
	readerID := "todo"

	ctx, span, _ := tracer.Start(ctx, "MeterReader-Read", trace.WithAttributes(attribute.String("reader", readerID)))
	defer span.End()

	if err := reader.ReadProduction(ctx); err != nil {
		return fmt.Errorf("error reading production: %w", err)
	}

	return nil
}

type ProductionInfo struct {
	Production  []Measurement `json:"production"`
	Consumption []Measurement `json:"consumption"`
}

type Measurement struct {
	Type            string `json:"type"`
	MeasurementType string `json:"measurementType"`

	ActiveCount       int     `json:"activeCount"`
	ReadingTime       int64   `json:"readingTime"`
	WattsNow          float64 `json:"wNow"`
	WattHoursLifetime float64 `json:"whLifetime"`
}

type MeterReader struct {
	baseURL url.URL
}

func NewMeterReader() (*MeterReader, error) {
	baseURL := os.Getenv("BASE_URL")
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("error parsing BASE_URL=%q: %w", baseURL, err)
	}
	return &MeterReader{baseURL: *u}, nil
}

func (r *MeterReader) ReadProduction(ctx context.Context) error {
	httpClient := http.DefaultClient

	ctx, span, log := tracer.Start(ctx, "ReadProduction")
	defer span.End()

	u := r.baseURL.JoinPath("production.json")
	u.RawQuery = "details=1"
	productionURL := u.String()
	log.Info("doing http request", attrs.HTTPMethod("GET"), attrs.HTTPURL(productionURL))
	t := time.Now()
	request, err := http.NewRequestWithContext(ctx, "GET", productionURL, nil)
	if err != nil {
		return fmt.Errorf("error build HTTP request for %q: %w", productionURL, err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("error doing HTTP GET %q: %w", productionURL, err)
	}
	if response.StatusCode != 200 {
		return fmt.Errorf("unexpected result %d from HTTP GET %q: %s", response.StatusCode, productionURL, response.Status)
	}
	b, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("error reading response to HTTP GET %q: %w", productionURL, err)
	}

	var info ProductionInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return fmt.Errorf("error parsing %q data: %w", productionURL, err)
	}

	log.Debug("http response", slog.String("body", string(b)))

	for _, m := range info.Production {
		if m.Type != "eim" {
			continue
		}
		if m.MeasurementType != "production" {
			continue
		}
		log.Info("read production", slog.Int64("time", t.UnixNano()), slog.Float64("watts", m.WattsNow))

		productionSync.Record(ctx, m.WattsNow)
		production.Observe(ctx, m.WattsNow)

		span.AddEvent("observed production", trace.WithAttributes(attribute.Float64("value", m.WattsNow)))
	}

	for _, m := range info.Consumption {
		if m.Type != "eim" {
			continue
		}
		if m.MeasurementType != "total-consumption" {
			continue
		}
		log.Info("read consumption", slog.Int64("time", t.UnixNano()), slog.Float64("watts", m.WattsNow))
		consumptionSync.Record(ctx, m.WattsNow)
		consumption.Observe(ctx, m.WattsNow)
		span.AddEvent("observed consumption", trace.WithAttributes(attribute.Float64("value", m.WattsNow)))
	}

	return nil
}
