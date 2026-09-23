// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer/internal/archive"
	"github.com/konradasb/dicer/internal/naming"
)

// copyEndpoint is one side of a copy: a path on this machine, or a path in
// an instance.
type copyEndpoint struct {
	instance string
	path     string
}

// String writes the endpoint as it is given on the command line.
func (e copyEndpoint) String() string {
	if e.instance == "" {
		return e.path
	}
	return e.instance + ":" + e.path
}

// parseCopyEndpoint reads NAME:PATH as a path in an instance and anything
// else as a local path. Write ./a:b for a local path containing a colon.
func parseCopyEndpoint(arg string) copyEndpoint {
	name, path, ok := strings.Cut(arg, ":")
	if ok && naming.Validate(name) == nil {
		return copyEndpoint{instance: name, path: path}
	}

	return copyEndpoint{path: arg}
}

func newInstanceCopyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "cp SRC DEST",
		Short: "Copy files between this machine and a running instance",
		Long: "Copy a file or directory between this machine and a running instance. A path in an " +
			"instance is written NAME:PATH; a relative one is taken from the guest's root.\n\n" +
			"What is copied lands as cp -r would put it: into DEST if it is a directory, in its " +
			"place if it is a file, and at it if nothing is there. Modes, times and symlinks are " +
			"kept; ownership is not, so what is copied belongs to whoever receives it.",
		Example: "  dicer instance cp ./app web:/srv\n" +
			"  dicer instance cp web:/var/log/app.log .",
		Args:    needs([]string{"a source", "a destination"}),
		Aliases: []string{"copy"},
		RunE: func(cmd *cobra.Command, args []string) error {
			src, dest := parseCopyEndpoint(args[0]), parseCopyEndpoint(args[1])

			var (
				n   int64
				err error
			)
			switch {
			case src.instance == "" && dest.instance != "":
				n, err = copyToInstance(cmd, src.path, dest)
			case src.instance != "" && dest.instance == "":
				n, err = copyFromInstance(cmd, src, dest.path)
			case src.instance != "":
				return errors.New("copying between two instances is not supported; copy to this machine, then on")
			default:
				return fmt.Errorf("neither %s nor %s is in an instance; write one as NAME:PATH", src, dest)
			}
			if err != nil {
				return err
			}

			succeeded(cmd, "Copied %s to %s (%s)", src, dest, size(n))
			return nil
		},
	}
}

// copyToInstance streams a local path into an instance and returns the bytes
// sent.
func copyToInstance(cmd *cobra.Command, src string, dest copyEndpoint) (int64, error) {
	client, cleanup, err := newClient(cmd)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	pr, pw := io.Pipe()
	go func() {
		// Whatever packing says is handed to the reader, so a local failure
		// ends the upload and is what the caller sees.
		_ = pw.CloseWithError(archive.Pack(pw, src))
	}()

	counted := &countingReader{r: pr}
	err = sendArchive(cmd.Context(), client, dest.instance, dest.path, counted)
	_ = pr.CloseWithError(err)

	return counted.n, err
}

// copyFromInstance receives a path from an instance and unpacks it locally,
// returning how much was received.
func copyFromInstance(cmd *cobra.Command, src copyEndpoint, dest string) (int64, error) {
	client, cleanup, err := newClient(cmd)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	var (
		pr, pw  = io.Pipe()
		recvErr error
	)
	go func() {
		recvErr = receiveArchive(cmd.Context(), client, src.instance, src.path, pw)
		_ = pw.CloseWithError(recvErr)
	}()

	counted := &countingReader{r: pr}
	err = archive.Unpack(counted, dest)
	_ = pr.CloseWithError(err)

	// A failure from the other end is reported as it said it, not as a
	// broken archive: "no such file", not "read archive: ...".
	if err != nil && recvErr != nil {
		return counted.n, recvErr
	}

	return counted.n, err
}

// countingReader counts what is read through it, so that a copy can report
// how much it moved.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err
}
