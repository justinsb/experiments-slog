package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"k8s.io/klog/v2"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	klog.InitFlags(nil)

	listen := "localhost:3000"
	flag.Parse()

	sink := &Sink{
		dir: "data",
	}

	ts := &traceServer{sink: sink}
	ms := &metricsServer{sink: sink}
	ls := &logsServer{sink: sink}

	klog.Infof("listening on %q", listen)
	lis, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("failed to listen on %q: %w", listen, err)
	}
	var opts []grpc.ServerOption

	grpcServer := grpc.NewServer(opts...)
	collectortracepb.RegisterTraceServiceServer(grpcServer, ts)
	collectormetricspb.RegisterMetricsServiceServer(grpcServer, ms)
	collectorlogspb.RegisterLogsServiceServer(grpcServer, ls)

	listenErr := make(chan error)
	go func() {
		listenErr <- grpcServer.Serve(lis)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-listenErr:
		return err
	}
}

type traceServer struct {
	collectortracepb.UnimplementedTraceServiceServer

	sink *Sink
}

func (s *traceServer) Export(ctx context.Context, req *collectortracepb.ExportTraceServiceRequest) (*collectortracepb.ExportTraceServiceResponse, error) {
	klog.Infof("trace.Export %v", prototext.Format(req))
	if err := s.sink.Export(ctx, "traces", req); err != nil {
		return nil, status.Errorf(codes.Internal, "error writing data")
	}
	return &collectortracepb.ExportTraceServiceResponse{}, nil
}

type metricsServer struct {
	collectormetricspb.UnimplementedMetricsServiceServer

	sink *Sink
}

func (s *metricsServer) Export(ctx context.Context, req *collectormetricspb.ExportMetricsServiceRequest) (*collectormetricspb.ExportMetricsServiceResponse, error) {
	klog.Infof("metrics.Export %v", prototext.Format(req))
	if err := s.sink.Export(ctx, "metrics", req); err != nil {
		return nil, status.Errorf(codes.Internal, "error writing data")
	}
	return &collectormetricspb.ExportMetricsServiceResponse{}, nil
}

type logsServer struct {
	collectorlogspb.UnimplementedLogsServiceServer

	sink *Sink
}

func (s *logsServer) Export(ctx context.Context, req *collectorlogspb.ExportLogsServiceRequest) (*collectorlogspb.ExportLogsServiceResponse, error) {
	klog.Infof("logs.Export %v", prototext.Format(req))
	if err := s.sink.Export(ctx, "logs", req); err != nil {
		return nil, status.Errorf(codes.Internal, "error writing data")
	}
	return &collectorlogspb.ExportLogsServiceResponse{}, nil

}

type Sink struct {
	dir string
}

func (s *Sink) Export(ctx context.Context, stream string, msg proto.Message) error {
	n := strconv.FormatInt(time.Now().UnixNano(), 10)
	p := filepath.Join(s.dir, stream, n)

	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", filepath.Dir(p), err)
	}

	b, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to serialize message: %w", err)
	}

	if err := os.WriteFile(p, b, 0644); err != nil {
		return fmt.Errorf("failed to write file %q: %w", p, err)
	}
	return nil
}
