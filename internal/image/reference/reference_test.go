// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package reference

import (
	"testing"
)

const testDigest = "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

func TestParseNormalisesReferences(t *testing.T) {
	for _, tc := range []struct {
		name           string
		input          string
		wantString     string
		wantRepository string
		wantDigest     string
	}{
		{
			name:           "short name with tag",
			input:          "alpine:3.18",
			wantString:     "docker.io/library/alpine:3.18",
			wantRepository: "docker.io/library/alpine",
		},
		{
			name:           "short name without tag is tagged latest",
			input:          "alpine",
			wantString:     "docker.io/library/alpine:latest",
			wantRepository: "docker.io/library/alpine",
		},
		{
			name:           "fully qualified with tag",
			input:          "docker.io/library/alpine:3.18",
			wantString:     "docker.io/library/alpine:3.18",
			wantRepository: "docker.io/library/alpine",
		},
		{
			name:           "digest",
			input:          "alpine@" + testDigest,
			wantString:     "docker.io/library/alpine@" + testDigest,
			wantRepository: "docker.io/library/alpine",
			wantDigest:     testDigest,
		},
		{
			name:           "custom registry",
			input:          "gcr.io/my-project/my-image:v1.0.0",
			wantString:     "gcr.io/my-project/my-image:v1.0.0",
			wantRepository: "gcr.io/my-project/my-image",
		},
		{
			name:           "localhost registry",
			input:          "localhost:5000/test:latest",
			wantString:     "localhost:5000/test:latest",
			wantRepository: "localhost:5000/test",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			if got := ref.String(); got != tc.wantString {
				t.Errorf("String() = %q, want %q", got, tc.wantString)
			}
			if got := ref.Repository(); got != tc.wantRepository {
				t.Errorf("Repository() = %q, want %q", got, tc.wantRepository)
			}
			if got := ref.Digest(); got != tc.wantDigest {
				t.Errorf("Digest() = %q, want %q", got, tc.wantDigest)
			}
			if got, want := ref.HasDigest(), tc.wantDigest != ""; got != want {
				t.Errorf("HasDigest() = %t, want %t", got, want)
			}
		})
	}
}

func TestParseRejectsInvalidReference(t *testing.T) {
	if _, err := Parse("INVALID::**"); err == nil {
		t.Error("Parse accepted an invalid reference")
	}
}

func TestFamiliarStringShortensReferences(t *testing.T) {
	for _, tc := range []struct {
		input, want string
	}{
		{"docker.io/library/busybox:latest", "busybox:latest"},
		{"gcr.io/project/image:v1", "gcr.io/project/image:v1"},
		{"INVALID::**", "INVALID::**"},
	} {
		if got := FamiliarString(tc.input); got != tc.want {
			t.Errorf("FamiliarString(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestShortDigestAbbreviatesLongDigests(t *testing.T) {
	for _, tc := range []struct {
		digest, want string
	}{
		{"sha256:1a2b3c4d5e6f7a8b9c0d", "sha256:1a2b3c4d5e6f"},
		{"sha256:1a2b3c", "sha256:1a2b3c"},
		{"1a2b3c4d5e6f7a8b9c0d", "1a2b3c4d5e6f7a8b9c0d"},
	} {
		if got := ShortDigest(tc.digest); got != tc.want {
			t.Errorf("ShortDigest(%q) = %q, want %q", tc.digest, got, tc.want)
		}
	}
}
