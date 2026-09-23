// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package reference

import (
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantRepo   string
		wantTag    string
		wantDigest string
		wantErr    bool
	}{
		{
			name:     "simple name with tag",
			input:    "alpine:3.18",
			wantRepo: "docker.io/library/alpine",
			wantTag:  "3.18",
		},
		{
			name:     "name without tag (should add latest)",
			input:    "alpine",
			wantRepo: "docker.io/library/alpine",
			wantTag:  "latest",
		},
		{
			name:     "fully qualified with tag",
			input:    "docker.io/library/alpine:3.18",
			wantRepo: "docker.io/library/alpine",
			wantTag:  "3.18",
		},
		{
			name:       "with digest",
			input:      "alpine@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			wantRepo:   "docker.io/library/alpine",
			wantDigest: "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		},
		{
			name:     "custom registry",
			input:    "gcr.io/my-project/my-image:v1.0.0",
			wantRepo: "gcr.io/my-project/my-image",
			wantTag:  "v1.0.0",
		},
		{
			name:     "localhost registry",
			input:    "localhost:5000/test:latest",
			wantRepo: "localhost:5000/test",
			wantTag:  "latest",
		},
		{
			name:    "invalid reference",
			input:   "INVALID::**",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := Parse(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("Parse() should return error")
				}
				return
			}

			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			if ref.Repository() != tt.wantRepo {
				t.Errorf("Repository() = %v, want %v", ref.Repository(), tt.wantRepo)
			}

			if tt.wantTag != "" && ref.Tag() != tt.wantTag {
				t.Errorf("Tag() = %v, want %v", ref.Tag(), tt.wantTag)
			}

			if tt.wantDigest != "" && ref.Digest() != tt.wantDigest {
				t.Errorf("Digest() = %v, want %v", ref.Digest(), tt.wantDigest)
			}

			if ref.String() == "" {
				t.Error("String() returned empty string")
			}
		})
	}
}

func TestRef_HasDigest(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"alpine:latest", false},
		{"alpine@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", true},
		{"gcr.io/project/image:v1", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			ref, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			if got := ref.HasDigest(); got != tt.want {
				t.Errorf("HasDigest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRef_DigestHex(t *testing.T) {
	tests := []struct {
		input   string
		wantHex string
	}{
		{"alpine:latest", ""},
		{"alpine@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			ref, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			if got := ref.DigestHex(); got != tt.wantHex {
				t.Errorf("DigestHex() = %v, want %v", got, tt.wantHex)
			}
		})
	}
}
