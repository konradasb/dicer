// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// enrollTimeout bounds each attempt to enrol at one of a token's addresses,
// so that an unreachable one does not stall the next.
const enrollTimeout = 10 * time.Second

// Enroll presents a token's secret to the daemon that issued it, along with
// the certificate of identity, which the daemon trusts from then on under
// the token's name. It returns the remote it succeeded at, to be kept and
// passed to NewClient as WithRemote.
//
// This is how a client comes to be trusted: until its key is enrolled, a
// daemon refuses its calls. The token is spent by a successful enrolment,
// and carries the daemon's fingerprint, so the client checks the daemon it
// is enrolling with as closely as the daemon checks it.
//
// Every address in the token is tried in turn. Only one that cannot be
// reached is passed over: a daemon that answers and refuses has said all
// there is to say, since the secret is spent or wrong wherever else it is
// tried.
func Enroll(ctx context.Context, token JoinToken, identity tls.Certificate, opts ...Option) (Remote, error) {
	var errs []string

	for _, address := range token.Addresses {
		r := TCPRemote(address, token.Fingerprint)

		err := func() error {
			c, err := NewClient(append(opts, WithRemote(r), WithIdentity(identity))...)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			ctx, cancel := context.WithTimeout(ctx, enrollTimeout)
			defer cancel()

			_, err = c.enroll(ctx, token.Secret)
			return err
		}()
		if err == nil {
			return r, nil
		}

		if code := status.Code(err); code != codes.Unavailable && code != codes.DeadlineExceeded {
			return Remote{}, fmt.Errorf("enrol with the daemon at %s: %w", address, err)
		}
		errs = append(errs, address+": "+err.Error())
	}

	return Remote{}, fmt.Errorf("cannot reach the daemon at any address in the token (%s)",
		strings.Join(errs, "; "))
}
