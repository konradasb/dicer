// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"errors"
	"testing"
)

func TestConvertError(t *testing.T) {
	cause := errors.New("mkfs.erofs failed")
	err := &ConvertError{Digest: "sha256:abc123", Format: "erofs", Cause: cause}

	want := "convert image sha256:abc123 to erofs: mkfs.erofs failed"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is(err, cause) = false, want true")
	}
}
