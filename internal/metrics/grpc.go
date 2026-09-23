// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// grpcMetrics measures the API this daemon serves.
type grpcMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newGRPCMetrics() grpcMetrics {
	return grpcMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "grpc_requests_total",
			Help:      "gRPC calls served, by full method and response code.",
		}, []string{"method", "code"}),

		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "grpc_request_duration_seconds",
			Help:      "Time a gRPC call took, from the first byte to the final status.",
			// Most calls are a file read and a reply; starting an instance
			// and pulling an image are the long tail, and a log stream runs
			// for as long as the client stays.
			Buckets: prometheus.ExponentialBuckets(0.001, 4, 9),
		}, []string{"method"}),
	}
}

func (gm grpcMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{gm.requests, gm.duration}
}

// UnaryServerInterceptor times unary calls and counts them by method and
// response code.
func (m *Metrics) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		started := time.Now()
		resp, err := handler(ctx, req)
		m.recordCall(info.FullMethod, err, time.Since(started))

		return resp, err
	}
}

// StreamServerInterceptor does the same for streaming calls. Their duration
// is the lifetime of the stream, which for a log follow is however long the
// client stayed -- worth knowing, but not a latency.
func (m *Metrics) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler,
	) error {
		started := time.Now()
		err := handler(srv, ss)
		m.recordCall(info.FullMethod, err, time.Since(started))

		return err
	}
}

// recordCall records one finished call. The code label comes from the gRPC
// status, so a handler returning a plain error is counted as Unknown -- which
// is what the client sees.
func (m *Metrics) recordCall(method string, err error, d time.Duration) {
	m.grpc.requests.WithLabelValues(method, status.Code(err).String()).Inc()
	m.grpc.duration.WithLabelValues(method).Observe(d.Seconds())
}
