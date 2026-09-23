// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// trustedClientFromProto is TrustedClientToProto backwards.
func trustedClientFromProto(p *dicerdv1.Client) TrustedClient {
	if p == nil {
		return TrustedClient{}
	}

	return TrustedClient{
		Name:        p.GetName(),
		Fingerprint: p.GetFingerprint(),
		Certificate: p.GetCertificate(),
		Subject:     p.GetSubject(),
		ExpiresAt:   goTime(p.GetExpireTime()),
		CreatedAt:   goTime(p.GetCreateTime()),
	}
}

// AccessTokenToProto converts an outstanding invitation to enrol. Its secret
// is not on the wire: the daemon does not have it to send.

// accessTokenFromProto is AccessTokenToProto backwards.
func accessTokenFromProto(p *dicerdv1.Token) AccessToken {
	if p == nil {
		return AccessToken{}
	}

	return AccessToken{
		Name:      p.GetName(),
		CreatedAt: goTime(p.GetCreateTime()),
		ExpiresAt: goTime(p.GetExpireTime()),
	}
}
