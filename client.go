// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"cmp"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/konradasb/dicer/internal/defaults"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// DefaultAddress is where a daemon serves its API unless configured
// otherwise: its Unix socket, as a gRPC target.
const DefaultAddress = "unix://" + defaults.Socket

// The keepalive a client uses unless WithKeepalive says otherwise.
const (
	// DefaultKeepaliveInterval is how long a connection may carry nothing
	// before the client pings the daemon.
	DefaultKeepaliveInterval = 30 * time.Second

	// DefaultKeepaliveTimeout is how long the client waits for the answer
	// to a ping before giving the connection up.
	DefaultKeepaliveTimeout = 10 * time.Second
)

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
	address           string
	tls               *tls.Config
	keepaliveInterval time.Duration
	keepaliveTimeout  time.Duration
	dialOptions       []grpc.DialOption
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

// WithKeepalive sets how long a connection may carry nothing before the
// client pings the daemon, and how long it waits for the answer before
// giving the connection up, failing the calls in flight on it with
// codes.Unavailable. That is how a daemon that has gone without closing the
// connection, because its host lost power or the network between dropped,
// is noticed; without it, a call waits for as long as its context lets it.
//
// The defaults are DefaultKeepaliveInterval and DefaultKeepaliveTimeout. An
// interval of zero turns the pings off, and a timeout of zero is the
// default. gRPC raises an interval below 10s to 10s, and a daemon closes the
// connection of a client that pings more often than its
// api.keepalive.min_client_interval.
//
// A connection over a Unix socket is never pinged: the kernel closes it if
// the daemon goes.
func WithKeepalive(interval, timeout time.Duration) Option {
	return func(o *options) {
		o.keepaliveInterval, o.keepaliveTimeout = interval, timeout
	}
}

// WithDialOptions adds gRPC dial options, such as interceptors. They are
// applied after WithKeepalive's, so a keepalive given here replaces it.
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
	o := options{
		address:           DefaultAddress,
		keepaliveInterval: DefaultKeepaliveInterval,
		keepaliveTimeout:  DefaultKeepaliveTimeout,
	}
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

	var dialOptions []grpc.DialOption
	if o.keepaliveInterval > 0 && !isSocket(o.address) {
		dialOptions = append(dialOptions, grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                o.keepaliveInterval,
			Timeout:             cmp.Or(o.keepaliveTimeout, DefaultKeepaliveTimeout),
			PermitWithoutStream: true,
		}))
	}

	dialOptions = append(dialOptions, o.dialOptions...)
	dialOptions = append(dialOptions, grpc.WithTransportCredentials(creds))

	conn, err := grpc.NewClient(o.address, dialOptions...)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", o.address, err)
	}

	return &Client{DaemonServiceClient: dicerdv1.NewDaemonServiceClient(conn), conn: conn}, nil
}

// isSocket reports whether a gRPC target is a Unix socket.
func isSocket(target string) bool {
	return strings.HasPrefix(target, "unix:") || strings.HasPrefix(target, "unix-abstract:")
}

// Close closes the connection to the daemon. Calls in flight are cancelled.
func (c *Client) Close() error {
	return c.conn.Close()
}
