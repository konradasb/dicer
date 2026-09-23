// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"log/slog"
	"path"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// Audit logs every call that can change something, and how it ended.
type Audit struct {
	logger *slog.Logger
}

// NewAudit returns an Audit that logs to logger.
func NewAudit(logger *slog.Logger) Audit {
	return Audit{logger: logger.With("component", "audit")}
}

// UnaryInterceptor audits unary calls.
func (a Audit) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		started := time.Now()
		resp, err := handler(ctx, req)
		a.record(ctx, info.FullMethod, started, err)

		return resp, err
	}
}

// StreamInterceptor audits streaming calls, when they end.
func (a Audit) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		started := time.Now()
		err := handler(srv, ss)
		a.record(ss.Context(), info.FullMethod, started, err)

		return err
	}
}

// record logs one finished call, unless it could not have changed anything.
func (a Audit) record(ctx context.Context, fullMethod string, started time.Time, err error) {
	method := path.Base(fullMethod)
	if isRead(method) {
		return
	}

	a.logger.InfoContext(ctx, "api call",
		"method", method,
		"code", status.Code(err).String(),
		"duration", time.Since(started),
	)
}

// isRead reports whether a method is a Get or List.
func isRead(method string) bool {
	return strings.HasPrefix(method, "Get") || strings.HasPrefix(method, "List")
}
