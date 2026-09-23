// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"crypto/x509"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer/internal/access"
	"github.com/dicer-sh/dicer/internal/certificate"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// Authentication decides who a request is from, and refuses the ones from
// nobody the daemon trusts. Every server the API is served on has one, and
// which one is what the transport calls for: the socket has its file
// permissions, TCP has client certificates.
//
// Handlers find the caller with access.FromContext.
type Authentication struct {
	// access recognises client certificates. Nil means the transport has
	// already decided, and every caller is local.
	access *access.Manager
}

// LocalAuthentication is for the API socket. Whoever can open the socket may
// use the API, so every caller is access.LocalIdentity.
func LocalAuthentication() Authentication {
	return Authentication{}
}

// CertificateAuthentication is for a TLS listener. A caller is the trusted
// client its certificate belongs to, and is refused if there is none --
// unless it is enrolling, which is how a certificate comes to be trusted.
func CertificateAuthentication(m *access.Manager) Authentication {
	return Authentication{access: m}
}

// unauthenticatedMethods are the methods an untrusted caller may use. Enroll
// has its own proof of entitlement: the token's secret.
var unauthenticatedMethods = map[string]bool{
	dicerdv1.AccessService_Enroll_FullMethodName: true,
}

// authenticate returns ctx carrying the caller's identity.
func (a Authentication) authenticate(ctx context.Context, fullMethod string) (context.Context, error) {
	if a.access == nil {
		return access.NewContext(ctx, access.LocalIdentity()), nil
	}

	if unauthenticatedMethods[fullMethod] {
		return ctx, nil
	}

	cert, ok := peerCertificate(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "no client certificate")
	}
	fingerprint := certificate.Fingerprint(cert.Raw)

	client, err := a.access.Authenticate(fingerprint)
	if errors.Is(err, access.ErrDenied) {
		return nil, status.Errorf(codes.Unauthenticated,
			"client certificate %s is not trusted; enrol it with a token from 'dicer token create'", fingerprint)
	}
	if err != nil {
		return nil, toStatus(err)
	}

	return access.NewContext(ctx, access.ClientIdentity(client.Name)), nil
}

// UnaryInterceptor authenticates unary calls.
func (a Authentication) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := a.authenticate(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

// StreamInterceptor authenticates streaming calls.
func (a Authentication) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := a.authenticate(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}

		return handler(srv, &contextStream{ServerStream: ss, ctx: ctx})
	}
}

// contextStream is a server stream with a context of its own, which is how a
// stream interceptor hands a handler anything.
type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextStream) Context() context.Context { return s.ctx }

// peerCertificate returns the certificate the caller presented, if it
// connected over TLS with one.
func peerCertificate(ctx context.Context) (*x509.Certificate, bool) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, false
	}

	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.PeerCertificates) == 0 {
		return nil, false
	}

	return info.State.PeerCertificates[0], true
}
