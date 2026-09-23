// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExecute(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantOut  string
	}{
		{"success", nil, 0, ""},
		{"plain error", errors.New("boom"), 1, "Error: boom\n"},
		{
			"a daemon's error is printed as its message alone",
			status.Error(codes.NotFound, `no instance "web"`),
			1, "Error: no instance \"web\"\n",
		},
		{
			"however it is wrapped",
			fmt.Errorf("%w (did you mean web?)", status.Error(codes.NotFound, `no instance "wbe"`)),
			1, "Error: no instance \"wbe\" (did you mean web?)\n",
		},
		{"exit status passes through silently", &exitError{code: 42}, 42, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{
				Use:           "test",
				SilenceErrors: true,
				SilenceUsage:  true,
				RunE:          func(*cobra.Command, []string) error { return tt.err },
			}
			cmd.SetArgs(nil)

			var stderr bytes.Buffer
			if code := exitStatus(cmd, &stderr); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if got := stderr.String(); got != tt.wantOut {
				t.Errorf("stderr = %q, want %q", got, tt.wantOut)
			}
		})
	}
}
