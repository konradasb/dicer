// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package access

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// secretBytes is the size of an enrolment secret: enough that guessing one
// is not a strategy, so no rate limiting is needed to make it so.
const secretBytes = 32

// newSecret returns a fresh enrolment secret.
func newSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashSecret returns what a secret is recorded and looked up as.
//
// A plain hash is enough, with no salt or stretching: those defend
// low-entropy secrets that people choose, and this one is 256 random bits.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
