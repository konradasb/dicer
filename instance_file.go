// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"time"

	"github.com/konradasb/dicer/internal/archive"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// CopyTo copies src, a file or directory on this machine, to dest in a
// running instance, waiting for a guest that is still booting.
//
// What is copied lands as cp -r would put it: into dest if it is a
// directory, in its place if it is a file, and at it if nothing is there. A
// relative path in the guest is taken from its root directory. Modes, times
// and symlinks are kept. Ownership is not, so what is copied belongs to
// whoever receives it. CopyFrom and the other copying calls do the same.
func (s *Instances) CopyTo(ctx context.Context, name, src, dest string) error {
	pr, pw := io.Pipe()
	go func() {
		// A failure to pack ends the upload, and is what the caller sees.
		_ = pw.CloseWithError(archive.Pack(pw, src))
	}()

	err := s.CopyArchiveTo(ctx, name, dest, pr)
	_ = pr.CloseWithError(err)

	return err
}

// CopyFrom copies src, a file or directory in an instance, to dest on this
// machine.
func (s *Instances) CopyFrom(ctx context.Context, name, src, dest string) error {
	r, err := s.copyArchiveFrom(ctx, name, src)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()

	if err := archive.Unpack(r, dest); err != nil {
		// A failure at the other end is reported as it said it, such as
		// "no such file", rather than as a broken archive.
		if failure := r.failure(); failure != nil {
			return failure
		}
		return err
	}

	return nil
}

// CopyArchiveTo unpacks a tar archive into an instance at dest. The archive
// holds a single file or directory at its top level, as archive/tar writes
// it from fs.FileInfoHeader.
func (s *Instances) CopyArchiveTo(ctx context.Context, name, dest string, r io.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := s.api.CopyToInstance(ctx)
	if err != nil {
		return fromStatus(err)
	}
	send := func(req *dicerdv1.CopyToInstanceRequest) error {
		if err := stream.Send(req); errors.Is(err, io.EOF) {
			// The daemon ended the call, and says why when it is closed.
			_, err = stream.CloseAndRecv()
			return fromStatus(err)
		} else if err != nil {
			return fromStatus(err)
		}
		return nil
	}

	err = send(&dicerdv1.CopyToInstanceRequest{
		Payload: &dicerdv1.CopyToInstanceRequest_Start{
			Start: &dicerdv1.CopyToInstanceStart{Name: name, Path: dest},
		},
	})
	if err != nil {
		return err
	}

	err = sendChunks(r, func(chunk []byte) error {
		return send(&dicerdv1.CopyToInstanceRequest{Payload: &dicerdv1.CopyToInstanceRequest_Data{Data: chunk}})
	})
	if err != nil {
		return err
	}

	_, err = stream.CloseAndRecv()
	return fromStatus(err)
}

// sendChunks reads r to its end and passes it to send in chunks the size of
// a message, each its own copy. It returns the first error either has.
func sendChunks(r io.Reader, send func(chunk []byte) error) error {
	buf := make([]byte, archive.ChunkSize)
	for {
		n, readErr := io.ReadFull(r, buf)
		if n > 0 {
			if err := send(bytes.Clone(buf[:n])); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// CopyArchiveFrom returns a reader of a tar archive of src in an instance,
// which holds it as a single file or directory at its top level. The reader
// must be closed when done. Reading it returns the error the call failed
// with, such as ErrNotFound for a path that is not there.
func (s *Instances) CopyArchiveFrom(ctx context.Context, name, src string) (io.ReadCloser, error) {
	return s.copyArchiveFrom(ctx, name, src)
}

// copyArchiveFrom is CopyArchiveFrom, returning the reader as it is.
func (s *Instances) copyArchiveFrom(ctx context.Context, name, src string) (*streamReader, error) {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := s.api.CopyFromInstance(ctx, &dicerdv1.CopyFromInstanceRequest{Name: name, Path: src})
	if err != nil {
		cancel()
		return nil, fromStatus(err)
	}

	return &streamReader{
		receive: func() ([]byte, error) {
			chunk, err := stream.Recv()
			return chunk.GetData(), err
		},
		cancel: cancel,
	}, nil
}

// ReadFile returns the contents of a regular file in an instance.
func (s *Instances) ReadFile(ctx context.Context, name, file string) ([]byte, error) {
	r, err := s.copyArchiveFrom(ctx, name, file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()

	tr := tar.NewReader(r)
	header, err := tr.Next()
	switch {
	case errors.Is(err, io.EOF):
		return nil, fmt.Errorf("read %s: the archive is empty", file)
	case err != nil:
		return nil, readArchiveError(r, err)
	case header.Typeflag != tar.TypeReg:
		return nil, fmt.Errorf("read %s: not a regular file", file)
	}

	data, err := io.ReadAll(tr)
	if err != nil {
		return nil, readArchiveError(r, err)
	}

	return data, nil
}

// WriteFile writes data to a file in an instance, creating it with perm if
// it is not there, and replacing it if it is. Its directory must exist. If
// the path names a directory, the file is written into it, under the path's
// last element.
func (s *Instances) WriteFile(ctx context.Context, name, file string, data []byte, perm fs.FileMode) error {
	base := path.Base(file)
	if base == "/" || base == "." {
		return fmt.Errorf("%w: %q does not name a file", ErrInvalidArgument, file)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     base,
		Mode:     int64(perm.Perm()),
		Size:     int64(len(data)),
		ModTime:  time.Now(),
	})
	if err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}

	return s.CopyArchiveTo(ctx, name, file, &buf)
}

// readArchiveError returns the error for an archive that could not be read
// from r: the error the stream failed with, if it did, and err otherwise.
func readArchiveError(r *streamReader, err error) error {
	if failure := r.failure(); failure != nil {
		return failure
	}
	return fmt.Errorf("read archive: %w", err)
}
