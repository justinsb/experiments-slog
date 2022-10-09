package main

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/metric/global"
	"go.opentelemetry.io/otel/metric/instrument"
	"go.opentelemetry.io/otel/metric/instrument/asyncfloat64"
	"go.opentelemetry.io/otel/metric/instrument/syncfloat64"
	"k8s.io/klog/v2"
)

func init() {
	if err := initMetrics(); err != nil {
		klog.Fatalf("failed to init metrics: %v", err)
	}
}

var consumption Gauge
var production Gauge
var consumptionSync syncfloat64.Histogram
var productionSync syncfloat64.Histogram

type Gauge struct {
	inner asyncfloat64.Gauge
	value float64
}

func (g *Gauge) Observe(ctx context.Context, value float64) {
	g.value = value
}
func (g *Gauge) callback(ctx context.Context) {
	g.inner.Observe(ctx, g.value)
}

func initMetrics() error {
	meter := global.Meter("justinsb.com/energy")
	var err error
	consumptionInner, err := meter.AsyncFloat64().Gauge("consumption", instrument.WithDescription("current consumption"))
	if err != nil {
		return fmt.Errorf("error creating metric: %w", err)
	}
	consumption.inner = consumptionInner
	productionInner, err := meter.AsyncFloat64().Gauge("production", instrument.WithDescription("current production"))
	if err != nil {
		return fmt.Errorf("error creating metric: %w", err)
	}
	production.inner = productionInner
	meter.RegisterCallback([]instrument.Asynchronous{consumptionInner, productionInner},
		func(ctx context.Context) {
			consumption.callback(ctx)
			production.callback(ctx)
		})

	consumptionSync, err = meter.SyncFloat64().Histogram("consumption-sync", instrument.WithDescription("current consumption"))
	if err != nil {
		return fmt.Errorf("error creating metric: %w", err)
	}
	productionSync, err = meter.SyncFloat64().Histogram("production-sync", instrument.WithDescription("current production"))
	if err != nil {
		return fmt.Errorf("error creating metric: %w", err)
	}
	return nil
}
