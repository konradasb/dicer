// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer/internal/defaults"
	"github.com/konradasb/dicer/internal/hostinfo"
)

// newValidateCommand returns the validate subcommand, which checks a
// configuration as serve would, without starting anything.
func newValidateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check a configuration file before the daemon uses it",
		Long: "Checks a configuration file as the daemon would when it starts: that it\n" +
			"parses, knows every key, and holds valid values, that the TLS files it\n" +
			"names can be read, as can the registries' password files and credential\n" +
			"helpers, and that this host can give instances what it allows.\n" +
			"Run it before restarting the daemon after a change, since a configuration\n" +
			"the daemon cannot use stops it from starting.",
		Example: "  dicerd validate\n" +
			"  dicerd validate --config ./config.yaml",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _ := cmd.Flags().GetString("config")

			if err := validateConfig(path); err != nil {
				return err
			}

			if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
				cmd.Printf("There is no %s: the daemon runs on its defaults, which are valid.\n", path)
				return nil
			}
			cmd.Printf("%s is valid.\n", path)
			return nil
		},
	}

	cmd.Flags().StringP("config", "f", defaults.Config, "Configuration file; a missing file means all defaults")

	return cmd
}

// validateConfig checks the configuration at path as the daemon does when it
// starts, stopping at the first problem.
func validateConfig(path string) error {
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}

	// What loadConfig cannot see, which the daemon only finds out as it
	// starts: files it names, and the host it runs on.
	if tls := cfg.API.TCP.TLS; cfg.API.TCP.Enabled() && tls.Enabled() {
		if _, err := serverTLSConfig(tls, slog.New(slog.DiscardHandler)); err != nil {
			return fmt.Errorf("api.tcp.tls: %w", err)
		}
	}

	for host, r := range cfg.Registries {
		if err := r.checkHost(host); err != nil {
			return err
		}
	}

	cpus, err := hostinfo.CPUCount()
	if err != nil {
		return fmt.Errorf("read the host's CPUs: %w", err)
	}
	memory, err := hostinfo.MemoryTotal()
	if err != nil {
		return fmt.Errorf("read the host's memory: %w", err)
	}
	if _, err := cfg.Resources.capacity(cpus, memory); err != nil {
		return err
	}

	return nil
}
