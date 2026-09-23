// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"
	"time"

	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// CreateToken invites a client to enrol under a name, and returns the token
// to hand it and when it expires. A zero ttl leaves the daemon's default,
// DefaultTokenTTL.
//
// The token returned is the only copy of its secret: the daemon keeps a hash,
// so a token that is lost is withdrawn and created again, not recovered.
func (c *Client) CreateToken(ctx context.Context, name string, ttl time.Duration) (JoinToken, time.Time, error) {
	resp, err := c.access.CreateToken(ctx, &dicerdv1.CreateTokenRequest{
		Name: name,
		Ttl:  duration(ttl),
	})
	if err != nil {
		return JoinToken{}, time.Time{}, err
	}

	token, err := ParseJoinToken(resp.GetToken())
	if err != nil {
		return JoinToken{}, time.Time{}, err
	}

	return token, goTime(resp.GetExpireTime()), nil
}

// ListTokens returns every token that can still be enrolled with. Their
// secrets are not among what it returns: the daemon does not have them.
func (c *Client) ListTokens(ctx context.Context) ([]AccessToken, error) {
	resp, err := c.access.ListTokens(ctx, &dicerdv1.ListTokensRequest{})
	if err != nil {
		return nil, err
	}

	out := make([]AccessToken, 0, len(resp.GetTokens()))
	for _, t := range resp.GetTokens() {
		out = append(out, accessTokenFromProto(t))
	}

	return out, nil
}

// DeleteToken withdraws a token before it is used.
func (c *Client) DeleteToken(ctx context.Context, name string) error {
	_, err := c.access.DeleteToken(ctx, &dicerdv1.DeleteTokenRequest{Name: name})

	return err
}

// ListClients returns every client the daemon trusts.
func (c *Client) ListClients(ctx context.Context) ([]TrustedClient, error) {
	resp, err := c.access.ListClients(ctx, &dicerdv1.ListClientsRequest{})
	if err != nil {
		return nil, err
	}

	out := make([]TrustedClient, 0, len(resp.GetClients()))
	for _, tc := range resp.GetClients() {
		out = append(out, trustedClientFromProto(tc))
	}

	return out, nil
}

// GetClient returns one trusted client by name, certificate included.
func (c *Client) GetClient(ctx context.Context, name string) (TrustedClient, error) {
	resp, err := c.access.GetClient(ctx, &dicerdv1.GetClientRequest{Name: name})
	if err != nil {
		return TrustedClient{}, err
	}

	return trustedClientFromProto(resp), nil
}

// DeleteClient stops the daemon trusting a client. Its next request is
// refused.
func (c *Client) DeleteClient(ctx context.Context, name string) error {
	_, err := c.access.DeleteClient(ctx, &dicerdv1.DeleteClientRequest{Name: name})

	return err
}

// enroll presents an enrolment secret along with this client's certificate,
// which the daemon trusts from then on. It is what Enroll does once it has
// found a daemon to do it with.
func (c *Client) enroll(ctx context.Context, secret string) (TrustedClient, error) {
	resp, err := c.access.Enroll(ctx, &dicerdv1.EnrollRequest{Secret: secret})
	if err != nil {
		return TrustedClient{}, err
	}

	return trustedClientFromProto(resp), nil
}
