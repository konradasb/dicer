// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/mdlayher/vsock"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/guest"
	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
)

// NewCommand returns the root command for the in-guest agent.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "dicer-agent",
		Short:         "Serve exec and file copy requests from the host: runs inside the VM",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       dicer.Version,
		Args:          cobra.NoArgs,
		RunE:          runRootCmd,
	}

	cmd.SetVersionTemplate(dicer.VersionString() + "\n")
	cmd.Flags().Uint32P("port", "p", guest.AgentPort, "The vsock port to listen on.")

	cmd.AddCommand(newReportExitCommand())

	return cmd
}

// newReportExitCommand reports an exit code on the status disk. dicer-init
// does this itself for a workload it runs; a guest that boots systemd has
// systemd run this as it powers off, since dicer-init is long gone by then.
func newReportExitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "report-exit",
		Short:  "Report how the guest ended to the host",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			device, _ := cmd.Flags().GetString("device")
			code, _ := cmd.Flags().GetInt("code")

			return guest.WriteStatus(device, guest.Status{Boots: 1, ExitCode: &code})
		},
	}

	cmd.Flags().String("device", "", "The status disk")
	cmd.Flags().Int("code", 0, "The exit code to report")
	_ = cmd.MarkFlagRequired("device")

	return cmd
}

func runRootCmd(cmd *cobra.Command, _ []string) error {
	port, err := cmd.Flags().GetUint32("port")
	if err != nil {
		return fmt.Errorf("get port flag: %w", err)
	}

	var l *vsock.Listener
	for i := range 10 {
		l, err = vsock.Listen(port, nil)
		if err == nil {
			break
		}
		slog.Warn("vsock listen failed, retrying", "attempt", i+1, "port", port, "err", err)
		time.Sleep(time.Second)
	}
	if err != nil {
		return fmt.Errorf("vsock listen on port %d: %w", port, err)
	}
	defer func() { _ = l.Close() }()

	slog.Info("dicer agent listening", "port", port)

	grpcServer := grpc.NewServer()
	diceragentv1.RegisterAgentServiceServer(grpcServer, &server{})

	if err := grpcServer.Serve(l); err != nil {
		return fmt.Errorf("grpc serve: %w", err)
	}

	return nil
}
