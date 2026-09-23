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

	"github.com/dicer-sh/dicer/internal/access"
)

// Audit logs every call that can change something, with who made it and how
// it ended. Reads are left out: they are most of the traffic and none of the
// consequences.
//
// It must run after Authentication, whose identity it records.
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

	// Only Enroll is served without an identity: the caller is not trusted
	// until it succeeds.
	caller := "unauthenticated"
	if id, ok := access.FromContext(ctx); ok {
		caller = id.String()
	}

	a.logger.InfoContext(ctx, "api call",
		"method", method,
		"caller", caller,
		"code", status.Code(err).String(),
		"duration", time.Since(started),
	)
}

// isRead reports whether a method only reads, going by the resource-oriented
// naming the API follows: Get and List methods have no side effects.
func isRead(method string) bool {
	return strings.HasPrefix(method, "Get") || strings.HasPrefix(method, "List")
}
