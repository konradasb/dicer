// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package remote

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/konradasb/dicer/internal/errdefs"
)

// TLS is the certificate files a remote's TCP address is reached with. CAFile
// verifies the daemon; CertFile and KeyFile identify the CLI to a daemon that
// requires client certificates. Either half may be given alone. The files
// are read on every connection, so a renewed certificate is picked up.
type TLS struct {
	// CertFile and KeyFile are the client's certificate and private key, in
	// PEM. Give both or neither.
	CertFile string `yaml:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`

	// CAFile holds the PEM authorities the daemon's certificate is checked
	// against. Empty uses the host's root CAs.
	CAFile string `yaml:"ca_file,omitempty"`

	// ServerName is the name the daemon's certificate must carry. Empty
	// uses the host from the remote's address.
	ServerName string `yaml:"server_name,omitempty"`
}

// Validate reports whether the settings are a usable combination. The files
// are not read.
func (t *TLS) Validate() error {
	if t == nil {
		return nil
	}

	switch {
	case t.CertFile != "" && t.KeyFile == "":
		return errdefs.InvalidArgument("tls: cert_file is set without key_file")
	case t.KeyFile != "" && t.CertFile == "":
		return errdefs.InvalidArgument("tls: key_file is set without cert_file")
	case t.CertFile == "" && t.CAFile == "":
		return errdefs.InvalidArgument(
			"tls: nothing is configured; give ca_file, or cert_file and key_file, or leave tls out entirely")
	}

	return nil
}

// Config reads the files into the configuration a connection is made with.
func (t *TLS) Config() (*tls.Config, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: t.ServerName}

	if t.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load the client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	if t.CAFile != "" {
		pool, err := certPool(t.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}

	return cfg, nil
}

// certPool reads a PEM file of certificate authorities.
func certPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the certificate authorities: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no certificates; want PEM", path)
	}

	return pool, nil
}
