// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package access decides who may use the daemon's API over the network.
//
// Locally, access is the API socket's file permissions, and this package has
// no say. Over the network every connection is mutual TLS, and a client is
// trusted by the fingerprint of the certificate it presents: a list of
// fingerprints, each under a name, is all the state there is. Revoking a
// client is removing its entry.
//
// A client gets onto the list by enrolling. Someone who already has access
// creates a token for a name; the token carries the daemon's addresses, its
// certificate's fingerprint and a one-time secret. The client connects,
// checks the daemon against that fingerprint, and presents the secret along
// with its own certificate, which is trusted under the token's name from then
// on. Neither side's private key ever leaves the machine it was made on, and
// the secret is good for one enrolment, for a limited time.
package access

import (
	"errors"
)

// ErrInvalidToken is returned for an enrolment secret that matches no
// outstanding token. It says no more than that on purpose: whether a secret
// was never issued, has expired or has been used is nothing a caller who
// does not hold a valid one needs to know.
var ErrInvalidToken = errors.New("invalid or expired enrolment token")

// ErrDenied is returned for a certificate that belongs to no trusted
// client.
var ErrDenied = errors.New("client certificate is not trusted")

// ErrRemoteAccessDisabled is returned when a token is asked for but the
// daemon does not listen on the network, so there is nothing to enrol with.
var ErrRemoteAccessDisabled = errors.New("the daemon does not listen on the network; set api.tcp.listen to enrol remote clients")
