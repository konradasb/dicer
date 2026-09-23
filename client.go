// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/konradasb/dicer/internal/defaults"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// DefaultAddress is where a daemon serves its API unless configured
// otherwise: its Unix socket, as a gRPC target.
const DefaultAddress = "unix://" + defaults.Socket

// Client is a connection to a Dicer daemon. It is the generated
// dicerdv1.DaemonServiceClient, so its methods are the API's RPCs, taking and
// returning the messages in package dicerdv1, and its errors are gRPC
// statuses.
//
// A Client is safe for concurrent use and should be closed when done.
type Client struct {
	dicerdv1.DaemonServiceClient

	conn *grpc.ClientConn
}

// options are what NewClient was asked for.
type options struct {
	address     string
	tls         *tls.Config
	dialOptions []grpc.DialOption
}

// An Option configures a Client.
type Option func(*options)

// WithAddress sets the daemon's address, as a gRPC target:
// "unix:///path/to/socket", or "host:port" for its TCP listener. The default
// is DefaultAddress.
func WithAddress(target string) Option {
	return func(o *options) { o.address = target }
}

// WithTLS secures the connection with cfg. A daemon's TCP listener needs it
// unless it is served without TLS; its certificate is checked against the
// address's host unless cfg names another.
func WithTLS(cfg *tls.Config) Option {
	return func(o *options) { o.tls = cfg }
}

// WithDialOptions adds gRPC dial options, such as interceptors or
// keepalives.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// NewClient connects to a daemon, by default the local one:
//
//	c, err := dicer.NewClient()
//
// A daemon's TCP listener, over TLS:
//
//	c, err := dicer.NewClient(dicer.WithAddress("host:7443"), dicer.WithTLS(cfg))
//
// Connecting is lazy: an unreachable daemon is reported by the first call.
// A local socket that is not there is reported here.
func NewClient(opts ...Option) (*Client, error) {
	o := options{address: DefaultAddress}
	for _, opt := range opts {
		opt(&o)
	}

	if path, ok := strings.CutPrefix(o.address, "unix://"); ok {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("there is no socket at %s; is dicerd running?", path)
		}
	}

	creds := insecure.NewCredentials()
	if o.tls != nil {
		creds = credentials.NewTLS(o.tls)
	}

	conn, err := grpc.NewClient(o.address, append(o.dialOptions, grpc.WithTransportCredentials(creds))...)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", o.address, err)
	}

	return &Client{DaemonServiceClient: dicerdv1.NewDaemonServiceClient(conn), conn: conn}, nil
}

// Close closes the connection to the daemon. Calls in flight are cancelled.
func (c *Client) Close() error {
	return c.conn.Close()
}
