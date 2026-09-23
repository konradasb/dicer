// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

// TODO: I don't like the "access" name.

import (
	"crypto/x509"
	"time"

	"github.com/dicer-sh/dicer/internal/certificate"
)

// TrustedClient is a party trusted to use the API over the network.
//
// It is not this package's Client, which is the thing making the calls. This
// is how a daemon records one it will accept them from.
type TrustedClient struct {
	// Name is what it is trusted under, chosen when its token was created.
	Name string `yaml:"name" json:"name"`

	// Fingerprint is the fingerprint of the certificate it presents:
	// "sha256:" and the hash of the certificate in hex. Only this decides
	// trust.
	Fingerprint string `yaml:"fingerprint" json:"fingerprint"`

	// Certificate is the certificate it enrolled with, PEM-encoded. It is
	// kept whole so that what it says -- whom it was issued to, and when --
	// can be shown, and so that it can be exported.
	Certificate string `yaml:"certificate" json:"certificate"`

	// Subject is whom that certificate was issued to, as the client wrote
	// it: by default the user and host that generated it. Informational.
	Subject string `yaml:"-" json:"subject,omitempty"`

	// ExpiresAt is when the certificate says it expires. Informational:
	// trust is by fingerprint, and certificate dates are not enforced.
	ExpiresAt time.Time `yaml:"-" json:"expires_at,omitzero"`

	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
}

// ParseCertificate returns the certificate the client enrolled with.
func (c TrustedClient) ParseCertificate() (*x509.Certificate, error) {
	return certificate.ParsePEM(c.Certificate)
}

// DefaultTokenTTL is how long a token is good for when a request names no
// lifetime: long enough to carry it to another machine, short enough that one
// lost on the way is not a standing risk.
const DefaultTokenTTL = time.Hour

// AccessToken is an outstanding invitation to enrol, as the daemon records
// it. The secret itself is not kept, only its hash: whoever can read the
// daemon's files can already do anything the token would let them.
type AccessToken struct {
	// Name is what the client that enrols with the token is trusted as.
	Name string `yaml:"name" json:"name"`

	// SecretHash is the SHA-256 of the secret, in hex. It is also what the
	// token is looked up by when a client presents the secret.
	SecretHash string `yaml:"secret_hash" json:"-"`

	ExpiresAt time.Time `yaml:"expires_at" json:"expires_at"`
	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
} // TODO: How is access token different from join token? Should they be the same type?

// Expired reports whether the token can no longer be used at t.
func (t AccessToken) Expired(at time.Time) bool {
	return !at.Before(t.ExpiresAt)
}
