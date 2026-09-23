// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/access"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// accessHandler handles the RPCs of AccessService.
type accessHandler struct {
	access *access.Manager
}

func (h *accessHandler) CreateToken(
	_ context.Context, req *dicerdv1.CreateTokenRequest,
) (*dicerdv1.CreateTokenResponse, error) {
	ttl := dicer.DefaultTokenTTL
	if req.GetTtl() != nil {
		ttl = req.GetTtl().AsDuration()
	}

	join, token, err := h.access.CreateToken(req.GetName(), ttl)
	if err != nil {
		return nil, toStatus(err)
	}

	return &dicerdv1.CreateTokenResponse{
		Token:      join.String(),
		ExpireTime: timestamppb.New(token.ExpiresAt),
	}, nil
}

func (h *accessHandler) ListTokens(
	_ context.Context, _ *dicerdv1.ListTokensRequest,
) (*dicerdv1.ListTokensResponse, error) {
	tokens, err := h.access.ListTokens()
	if err != nil {
		return nil, toStatus(err)
	}

	resp := &dicerdv1.ListTokensResponse{Tokens: make([]*dicerdv1.Token, 0, len(tokens))}
	for _, t := range tokens {
		resp.Tokens = append(resp.Tokens, tokenToProto(t))
	}

	return resp, nil
}

func (h *accessHandler) DeleteToken(
	_ context.Context, req *dicerdv1.DeleteTokenRequest,
) (*emptypb.Empty, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	if err := h.access.DeleteToken(req.GetName()); err != nil {
		return nil, toStatus(err)
	}

	return &emptypb.Empty{}, nil
}

func (h *accessHandler) ListClients(
	_ context.Context, _ *dicerdv1.ListClientsRequest,
) (*dicerdv1.ListClientsResponse, error) {
	clients, err := h.access.ListClients()
	if err != nil {
		return nil, toStatus(err)
	}

	resp := &dicerdv1.ListClientsResponse{Clients: make([]*dicerdv1.Client, 0, len(clients))}
	for _, c := range clients {
		resp.Clients = append(resp.Clients, clientToProto(c))
	}

	return resp, nil
}

func (h *accessHandler) GetClient(
	_ context.Context, req *dicerdv1.GetClientRequest,
) (*dicerdv1.Client, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	client, err := h.access.GetClient(req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}

	return clientToProto(client), nil
}

func (h *accessHandler) DeleteClient(
	_ context.Context, req *dicerdv1.DeleteClientRequest,
) (*emptypb.Empty, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	if err := h.access.DeleteClient(req.GetName()); err != nil {
		return nil, toStatus(err)
	}

	return &emptypb.Empty{}, nil
}

// Enroll trusts the certificate the caller connected with. The certificate
// is taken from the connection, never from the request, so that a client can
// only enrol a key it holds.
func (h *accessHandler) Enroll(ctx context.Context, req *dicerdv1.EnrollRequest) (*dicerdv1.Client, error) {
	cert, ok := peerCertificate(ctx)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition,
			"enrolment needs a TLS connection with a client certificate; a local caller is trusted already")
	}
	if req.GetSecret() == "" {
		return nil, status.Error(codes.InvalidArgument, "secret is required")
	}

	client, err := h.access.Enroll(req.GetSecret(), cert)
	if err != nil {
		return nil, toStatus(err)
	}

	return clientToProto(client), nil
}
