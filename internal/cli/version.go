// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"io"
	"runtime"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer/internal/version"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// versionInfo is what 'dicer version' reports of the client, and of the
// daemon if it could be asked.
type versionInfo struct {
	Client clientVersion  `json:"client" yaml:"client"`
	Server *serverVersion `json:"server,omitempty" yaml:"server,omitempty"`
}

type clientVersion struct {
	Version string `json:"version" yaml:"version"`
	Commit  string `json:"commit" yaml:"commit"`
	Built   string `json:"built" yaml:"built"`
	Go      string `json:"go" yaml:"go"`
	OS      string `json:"os" yaml:"os"`
	Arch    string `json:"arch" yaml:"arch"`
}

type serverVersion struct {
	Version string `json:"version" yaml:"version"`
	Host    string `json:"host" yaml:"host"`
	Remote  string `json:"remote" yaml:"remote"`
}

func newVersionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show the client's version, and the daemon's",
		Long: "Shows this client's version, and that of the daemon it talks to. The\n" +
			"client's is shown even when the daemon cannot be reached.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := versionInfo{Client: clientVersion{
				Version: version.Version,
				Commit:  orDash(version.Commit),
				Built:   version.BuildDate,
				Go:      runtime.Version(),
				OS:      runtime.GOOS,
				Arch:    runtime.GOARCH,
			}}

			server, serverErr := daemonVersion(cmd)
			info.Server = server

			format, _ := cmd.Flags().GetString("format")
			if format == "table" || format == "text" {
				if err := writeVersion(cmd.OutOrStdout(), info); err != nil {
					return err
				}
			} else if err := writeStructured(cmd.OutOrStdout(), format, info); err != nil {
				return err
			}

			return serverErr
		},
	}

	cmd.Flags().String("format", "table", "Output format: table, json or yaml")
	_ = cmd.RegisterFlagCompletionFunc("format", completeFormats)

	return cmd
}

// daemonVersion asks the daemon the command is aimed at for its version.
func daemonVersion(cmd *cobra.Command) (*serverVersion, error) {
	t, err := resolveTarget(cmd)
	if err != nil {
		return nil, err
	}

	client, cleanup, err := newClient(cmd)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	host, err := client.GetHostInfo(cmd.Context(), &dicerdv1.GetHostInfoRequest{})
	if err != nil {
		return nil, err
	}

	return &serverVersion{Version: host.GetVersion(), Host: host.GetHostname(), Remote: t.String()}, nil
}

// writeVersion writes the versions for a person to read.
func writeVersion(w io.Writer, info versionInfo) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	c := info.Client
	lines := []string{
		"Client:",
		" Version:\t" + c.Version,
		" Commit:\t" + c.Commit,
		" Built:\t" + c.Built,
		" Go:\t" + c.Go,
		" OS/Arch:\t" + c.OS + "/" + c.Arch,
	}
	if s := info.Server; s != nil {
		lines = append(lines,
			"",
			"Server:",
			" Version:\t"+s.Version,
			" Host:\t"+s.Host,
			" Remote:\t"+s.Remote,
		)
	}

	for _, line := range lines {
		if _, err := fmt.Fprintln(tw, line); err != nil {
			return err
		}
	}
	return tw.Flush()
}
