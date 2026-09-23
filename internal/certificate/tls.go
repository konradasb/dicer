// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package certificate

import "crypto/tls"

// ServerConfig is the TLS configuration the daemon serves the API with.
//
// It asks every client for a certificate and accepts any: there is no CA to
// verify a chain against, and a client's certificate is checked against the
// trusted fingerprints after the handshake, where the one method an untrusted
// client may call can be let through. A certificate is required even for
// that -- it is the key being enrolled.
func ServerConfig(pair tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.RequireAnyClientCert,
	}
}

// ClientConfig is the TLS configuration a client connects to the daemon
// with: presenting pair, and accepting only a daemon whose certificate has
// the pinned fingerprint.
func ClientConfig(pair tls.Certificate, pinned string) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{pair},
		// Chain and hostname verification are replaced, not skipped: the
		// daemon's certificate is self-signed and may be reached by any
		// address, so what identifies it is its fingerprint, which
		// VerifyConnection checks on every handshake, resumed or not.
		InsecureSkipVerify: true, //nolint:gosec // verified by fingerprint; see above
		VerifyConnection:   VerifyPinned(pinned),
	}
}
