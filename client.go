// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/user"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/dicer-sh/dicer/internal/certificate"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// Client talks to a Dicer daemon.
//
// Its methods take and return the types in this package, not the generated
// protobuf messages: the wire format is an implementation detail of the
// client and of the daemon, and nothing built on either has to know it.
//
//	instances, err := c.ListInstances(ctx)
//
// Errors come back in the classes this package defines, so what went wrong is
// matched with errors.Is rather than by reading a message:
//
//	if errors.Is(err, dicer.ErrNotFound) { ... }
//
// A Client is safe for concurrent use, and should be closed when done with.
type Client struct {
	daemon dicerdv1.DaemonServiceClient
	access dicerdv1.AccessServiceClient

	conn   *grpc.ClientConn
	remote Remote
}

// options are what NewClient was asked for.
type options struct {
	remote      Remote
	identity    *tls.Certificate
	dialOptions []grpc.DialOption
}

// An Option configures a Client.
type Option func(*options)

// WithRemote sets the daemon to talk to, address and fingerprint together.
// It is what Enroll returns, and what a caller that keeps its own remotes
// has.
func WithRemote(r Remote) Option {
	return func(o *options) { o.remote = r }
}

// WithAddress sets where the daemon is: "unix:///path/to/socket", or
// "tcp://host:port" for one on another machine, which also needs
// WithFingerprint. The default is the daemon on this machine, on
// DefaultSocket.
func WithAddress(address string) Option {
	return func(o *options) { o.remote.Address = address }
}

// WithFingerprint pins the certificate the daemon must present, as
// "sha256:" and a hex-encoded hash. A client talks to no daemon over the
// network without one: there is no certificate authority to vouch for it,
// so the fingerprint is what says the daemon is the right one.
//
// It is reported by 'dicer info' on the host, and carried by an enrolment
// token.
func WithFingerprint(fingerprint string) Option {
	return func(o *options) { o.remote.Fingerprint = fingerprint }
}

// WithIdentity sets the key pair the client identifies itself with over the
// network. The daemon trusts it by its fingerprint, which enrolling is what
// arranges; until then its calls are refused.
//
// LoadIdentity keeps a pair in a directory, the way the CLI does. A caller
// that keeps its key elsewhere -- a secret manager, or a file of its own --
// loads it however it likes and passes it here.
//
// It is not needed over a socket, where the socket's permissions decide.
func WithIdentity(pair tls.Certificate) Option {
	return func(o *options) { o.identity = &pair }
}

// WithDialOptions adds gRPC dial options: interceptors, keepalives, message
// size limits. They are appended to the ones the client sets up itself --
// the transport's credentials, and the interceptors that put errors in their
// classes -- and a caller that passes credentials of its own will find they
// do not take.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// NewClient connects to a daemon. With no options it is the one on this
// machine, over its socket:
//
//	c, err := dicer.NewClient()
//
// One on another machine is reached over mutual TLS, pinned to its
// certificate, as the client its key is enrolled as:
//
//	pair, err := dicer.LoadIdentity(dir)
//	c, err := dicer.NewClient(
//		dicer.WithAddress("tcp://host:7443"),
//		dicer.WithFingerprint("sha256:..."),
//		dicer.WithIdentity(pair),
//	)
//
// Connecting is lazy, as it is in gRPC: a daemon that is not there is
// reported by the first call, not here. A socket that is not there is
// reported here, because the error a call would give for it says less.
func NewClient(opts ...Option) (*Client, error) {
	o := options{remote: LocalRemote()}
	for _, opt := range opts {
		opt(&o)
	}

	if err := o.remote.Validate(); err != nil {
		return nil, err
	}

	conn, err := dial(o)
	if err != nil {
		return nil, err
	}

	return &Client{
		daemon: dicerdv1.NewDaemonServiceClient(conn),
		access: dicerdv1.NewAccessServiceClient(conn),
		conn:   conn,
		remote: o.remote,
	}, nil
}

// dial connects to the remote: over its socket if it is local, and over
// mutual TLS, pinned to its fingerprint, if it is not.
func dial(o options) (*grpc.ClientConn, error) {
	dialOptions := append([]grpc.DialOption{
		grpc.WithChainUnaryInterceptor(classifyUnary),
		grpc.WithChainStreamInterceptor(classifyStream),
	}, o.dialOptions...)

	if o.remote.IsLocal() {
		path := o.remote.SocketPath()

		// Checked up front because gRPC connects lazily, and its error for
		// a missing socket, arriving with the first call, says less.
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("there is no socket at %s; is dicerd running?", path)
			}
			return nil, err
		}

		return grpc.NewClient("passthrough:///dicerd", append(dialOptions,
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)...)
	}

	if o.identity == nil {
		return nil, InvalidArgument("a daemon reached over the network needs an identity to present to it; " +
			"see dicer.WithIdentity")
	}

	return grpc.NewClient(o.remote.HostPort(), append(dialOptions,
		grpc.WithTransportCredentials(
			credentials.NewTLS(certificate.ClientConfig(*o.identity, o.remote.Fingerprint))))...)
}

// Remote returns the daemon the client is talking to.
func (c *Client) Remote() Remote {
	return c.remote
}

// Conn returns the underlying connection, for a caller that needs a gRPC
// facility this client does not expose: a keepalive, a stats handler, its
// state. The generated service clients are deliberately not reachable
// through it -- what this client does not offer, the API does not offer.
func (c *Client) Conn() *grpc.ClientConn {
	return c.conn
}

// Close closes the connection to the daemon. Calls in flight are cancelled.
func (c *Client) Close() error {
	return c.conn.Close()
}

// LoadIdentity returns the key pair kept in dir, generating one on first
// use. The key never leaves the machine; only its certificate is sent, and
// the daemon trusts it by fingerprint once enrolled.
//
// The pair is kept as cert.pem and key.pem, readable only by its owner,
// which is where the dicer CLI keeps its own: pass it
// filepath.Join(configDir, "client") to share one identity with it.
func LoadIdentity(dir string) (tls.Certificate, error) {
	return certificate.LoadOrGenerate(dir, identityName())
}

// Fingerprint returns the fingerprint of a key pair's certificate: what the
// daemon knows a client by, and what 'dicer client list' shows.
func Fingerprint(pair tls.Certificate) (string, error) {
	return certificate.FingerprintOf(pair)
}

// identityName is what a generated certificate is issued to: user@host. Only
// a person reading the certificate sees it; the daemon names a client
// whatever its token said.
func identityName() string {
	name := "dicer"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	if host, err := os.Hostname(); err == nil {
		name += "@" + host
	}

	return name
}
