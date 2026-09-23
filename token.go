// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dicer-sh/dicer/internal/certificate"
)

// joinTokenPrefix begins every encoded JoinToken. It names the format's
// version, so that a later one can be told apart, and makes a token
// recognisable at a glance.
const joinTokenPrefix = "dicer1."

// JoinToken is what a client is handed to enrol with: everything it needs to
// find the daemon, recognise it, and prove it was invited. It is a secret
// until it is used.
type JoinToken struct {
	// Name is what the client will be trusted as.
	Name string `json:"name"`

	// Addresses are the host:port pairs the daemon can be reached at, to
	// be tried in order.
	Addresses []string `json:"addresses"`

	// Fingerprint is the daemon's certificate fingerprint. The client
	// refuses any daemon that does not present it.
	Fingerprint string `json:"fingerprint"`

	// Secret is the one-time enrolment secret.
	Secret string `json:"secret"`
}

// String encodes the token for copying from one terminal to another.
func (t JoinToken) String() string {
	// Marshalling a struct of strings cannot fail. The secret is meant to be
	// in it: carrying it is what the token is for.
	data, _ := json.Marshal(t)
	return joinTokenPrefix + base64.RawURLEncoding.EncodeToString(data)
}

// ParseJoinToken decodes a token encoded by JoinToken.String.
func ParseJoinToken(s string) (JoinToken, error) {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(s), joinTokenPrefix)
	if !ok {
		return JoinToken{}, errors.New("not a Dicer enrolment token")
	}

	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return JoinToken{}, fmt.Errorf("decode enrolment token: %w", err)
	}

	var t JoinToken
	if err := json.Unmarshal(data, &t); err != nil {
		return JoinToken{}, fmt.Errorf("decode enrolment token: %w", err)
	}

	if err := t.validate(); err != nil {
		return JoinToken{}, fmt.Errorf("invalid enrolment token: %w", err)
	}

	return t, nil
}

// validate reports whether every field of a decoded token is usable.
func (t JoinToken) validate() error {
	if t.Name == "" {
		return errors.New("no client name")
	}
	if len(t.Addresses) == 0 {
		return errors.New("no daemon address")
	}
	if err := certificate.ValidateFingerprint(t.Fingerprint); err != nil {
		return err
	}
	if t.Secret == "" {
		return errors.New("no secret")
	}

	return nil
}
