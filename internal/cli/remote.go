// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer/internal/cli/remote"
)

type printableRemote struct {
	Remotes []namedRemote
}

// namedRemote is a remote with its name, and whether it is the current one.
type namedRemote struct {
	name    string
	remote  remote.Remote
	current bool
}

func (p *printableRemote) Columns() []string {
	return []string{"Name", "Address", "TLS", "Current"}
}

func (p *printableRemote) Rows() []map[string]any {
	rows := make([]map[string]any, 0, len(p.Remotes))
	for _, r := range p.Remotes {
		current := ""
		if r.current {
			current = "*"
		}
		rows = append(rows, map[string]any{
			"Name":    r.name,
			"Address": r.remote.Address,
			"TLS":     tlsSummary(r.remote.TLS),
			"Current": current,
		})
	}
	return rows
}

// tlsSummary says in a word what a remote establishes about the daemon and
// about itself.
func tlsSummary(t *remote.TLS) string {
	switch {
	case t == nil:
		return ""
	case t.CertFile != "" && t.CAFile != "":
		return "mutual"
	case t.CertFile != "":
		return "client cert"
	default:
		return "server only"
	}
}

func newRemoteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remote",
		Short:   "Manage the daemons this client talks to",
		Aliases: []string{"remotes"},
		Long: "Manage the daemons this client talks to.\n\n" +
			"The built-in remote \"" + remote.Local + "\" is the daemon on this machine, on its " +
			"socket. A daemon on another machine is added with its address, and with the TLS " +
			"material that verifies it and identifies this client to it.",
	}

	cmd.AddCommand(
		newRemoteCreateCommand(),
		newRemoteListCommand(),
		newRemoteDeleteCommand(),
		newRemoteUseCommand(),
	)

	return cmd
}

func newRemoteCreateCommand() *cobra.Command {
	var tlsFlags remoteTLSFlags

	cmd := &cobra.Command{
		Use:   "create NAME ADDRESS",
		Short: "Add a daemon to talk to",
		Long: "Add a daemon to talk to.\n\n" +
			"A unix:// socket is controlled by its file permissions: whoever can open it " +
			"may do anything, and nothing further identifies either end.\n\n" +
			"A daemon's TCP listener, HOST:PORT, is reached over TLS, or over nothing at all. With --tls-ca " +
			"this client verifies the daemon and the connection is encrypted; with " +
			"--tls-cert and --tls-key it identifies itself in turn, which a daemon " +
			"configured with api.tcp.tls.client_ca_file requires. Given neither, the " +
			"connection is plaintext and unauthenticated, so reach the daemon only over a " +
			"network you trust as far as you trust the host.\n\n" +
			"The files are read on every connection, not copied here, so a renewed " +
			"certificate is picked up without the remote being changed.",
		Example: "  dicer remote create prod dicer1.example.com:7443 \\\n" +
			"    --tls-ca ~/.dicer/ca.pem \\\n" +
			"    --tls-cert ~/.dicer/client.pem --tls-key ~/.dicer/client-key.pem\n" +
			"  dicer remote create test unix:///run/dicer-test/dicer.sock",
		Args:    needs([]string{"a name for the remote", "an address: HOST:PORT or unix:///PATH"}),
		Aliases: []string{"new", "add"},
		RunE: func(cmd *cobra.Command, args []string) error {
			name, address := args[0], args[1]

			err := updateRemoteConfig(func(cfg *remote.Config) error {
				if _, err := cfg.Remote(name); err == nil {
					return fmt.Errorf("remote %q already exists", name)
				}

				r, err := remote.Parse(address)
				if err != nil {
					return err
				}
				r.TLS = tlsFlags.remoteTLS()

				return cfg.Create(name, r)
			})
			if err != nil {
				return err
			}

			succeeded(cmd, "Remote %s created. Use it with: dicer remote use %s", name, name)

			return nil
		},
	}

	tlsFlags.register(cmd)

	return cmd
}

// remoteTLSFlags are the TLS files a remote is created with.
type remoteTLSFlags struct {
	cert       string
	key        string
	ca         string
	serverName string
}

func (f *remoteTLSFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.cert, "tls-cert", "",
		"Certificate identifying this client to the daemon, in PEM")
	cmd.Flags().StringVar(&f.key, "tls-key", "",
		"Private key for --tls-cert, in PEM")
	cmd.Flags().StringVar(&f.ca, "tls-ca", "",
		"Authorities the daemon's certificate is checked against, in PEM")
	cmd.Flags().StringVar(&f.serverName, "tls-server-name", "",
		"Name the daemon's certificate must carry, if not the host in ADDRESS")

	_ = cmd.MarkFlagFilename("tls-cert")
	_ = cmd.MarkFlagFilename("tls-key")
	_ = cmd.MarkFlagFilename("tls-ca")
}

// remoteTLS returns the TLS settings the flags give, or nil. Remote.Validate
// checks them.
func (f *remoteTLSFlags) remoteTLS() *remote.TLS {
	if f.cert == "" && f.key == "" && f.ca == "" && f.serverName == "" {
		return nil
	}

	return &remote.TLS{
		CertFile:   absolutePath(f.cert),
		KeyFile:    absolutePath(f.key),
		CAFile:     absolutePath(f.ca),
		ServerName: f.serverName,
	}
}

// absolutePath resolves a path against the working directory, since a remote
// is read back from somewhere else entirely.
func absolutePath(path string) string {
	if path == "" {
		return ""
	}

	resolved, err := filepath.Abs(path)
	if err != nil {
		return path
	}

	return resolved
}

func newRemoteListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List remotes",
		Args:    noArgs,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadRemoteConfig()
			if err != nil {
				return err
			}

			current := cfg.CurrentName()
			p := &printableRemote{}
			for _, name := range cfg.Names() {
				r, _ := cfg.Remote(name)
				p.Remotes = append(p.Remotes, namedRemote{name: name, remote: r, current: name == current})
			}

			return render(cmd, p)
		},
	}

	addOutputFlags(cmd, true)

	return cmd
}

func newRemoteDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete (NAME | --all)",
		Short: "Forget a remote, or all of them",
		Long: "Forgets a remote, or with --all every remote added, asking first on a\n" +
			"terminal. The built-in " + remote.Local + " remote cannot be forgotten.",
		Args: func(cmd *cobra.Command, args []string) error {
			if all, _ := cmd.Flags().GetBool("all"); all {
				return noArgs(cmd, args)
			}
			return one("a remote name")(cmd, args)
		},
		Aliases:           []string{"rm", "remove"},
		ValidArgsFunction: completeRemotes,
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateRemoteConfig(func(cfg *remote.Config) error {
				names := args
				if all, _ := cmd.Flags().GetBool("all"); all {
					names = slices.DeleteFunc(cfg.Names(), func(n string) bool { return n == remote.Local })
					if ok, err := confirmDeleteAll(cmd, "remotes", names); err != nil || !ok {
						return err
					}
				}

				for _, name := range names {
					if err := cfg.Delete(name); err != nil {
						return err
					}
					succeeded(cmd, "Remote %s deleted", name)
				}
				return nil
			})
		},
	}
	addDeleteAllFlags(cmd, "remotes")

	return cmd
}

func newRemoteUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use NAME",
		Short: "Make a remote the current one",
		Long: "Make a remote the one commands talk to unless told otherwise " +
			"with --remote or $" + remoteEnv + ".",
		Args:              one("a remote name"),
		ValidArgsFunction: completeRemotes,
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateRemoteConfig(func(cfg *remote.Config) error {
				if err := cfg.Use(args[0]); err != nil {
					return err
				}
				succeeded(cmd, "Now using remote %s", args[0])
				return nil
			})
		},
	}
}

// loadRemoteConfig loads the remotes this client knows.
func loadRemoteConfig() (*remote.Config, error) {
	dir, err := remote.ConfigDir()
	if err != nil {
		return nil, err
	}
	return remote.Load(dir)
}

// updateRemoteConfig loads the remotes, changes them and saves them. Nothing
// is saved if change fails.
func updateRemoteConfig(change func(*remote.Config) error) error {
	dir, err := remote.ConfigDir()
	if err != nil {
		return err
	}
	cfg, err := remote.Load(dir)
	if err != nil {
		return err
	}

	if err := change(cfg); err != nil {
		return err
	}

	return cfg.Save(dir)
}
