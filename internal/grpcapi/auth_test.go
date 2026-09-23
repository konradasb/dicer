// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/access"
	"github.com/dicer-sh/dicer/internal/certificate"
	"github.com/dicer-sh/dicer/internal/filestore"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// tlsAPI is the API served over mutual TLS on loopback, as the daemon serves
// it, over a real definition store.
type tlsAPI struct {
	address     string
	fingerprint string
	access      *access.Manager
}

func newTLSAPI(t *testing.T) *tlsAPI {
	t.Helper()

	definitions, err := filestore.NewManager(filestore.Config{
		DataDir: filepath.Join(t.TempDir(), "data"),
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}

	pair, err := certificate.LoadOrGenerate(t.TempDir(), "dicerd")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := certificate.FingerprintOf(pair)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	m := access.NewManager(access.Config{
		Store:       definitions,
		Fingerprint: fingerprint,
		Addresses:   []string{listener.Addr().String()},
		Logger:      slog.New(slog.DiscardHandler),
	})

	auth := CertificateAuthentication(m)
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(certificate.ServerConfig(pair))),
		grpc.ChainUnaryInterceptor(auth.UnaryInterceptor()),
		grpc.ChainStreamInterceptor(auth.StreamInterceptor()),
	)
	NewServer(Config{Definitions: definitions, Access: m}).Register(server)

	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	return &tlsAPI{address: listener.Addr().String(), fingerprint: fingerprint, access: m}
}

// dial connects as a client holding its own key, pinning the fingerprint
// given.
func (a *tlsAPI) dial(t *testing.T, pinned string) (dicerdv1.DaemonServiceClient, dicerdv1.AccessServiceClient, string) {
	t.Helper()

	pair, err := certificate.LoadOrGenerate(t.TempDir(), "client")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := certificate.FingerprintOf(pair)

	conn, err := grpc.NewClient(a.address,
		grpc.WithTransportCredentials(credentials.NewTLS(certificate.ClientConfig(pair, pinned))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return dicerdv1.NewDaemonServiceClient(conn), dicerdv1.NewAccessServiceClient(conn), fingerprint
}

// enroll trusts the client under name, as 'dicer remote add' does.
func (a *tlsAPI) enroll(t *testing.T, client dicerdv1.AccessServiceClient, name string) {
	t.Helper()

	join, _, err := a.access.CreateToken(name, dicer.DefaultTokenTTL)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, err := client.Enroll(t.Context(), &dicerdv1.EnrollRequest{Secret: join.Secret}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()

	if got := status.Code(err); got != want {
		t.Errorf("code = %s (%v), want %s", got, err, want)
	}
}

func TestUntrustedClientIsRefused(t *testing.T) {
	api := newTLSAPI(t)
	daemon, accessClient, _ := api.dial(t, api.fingerprint)

	_, err := daemon.ListNetworks(t.Context(), &dicerdv1.ListNetworksRequest{})
	wantCode(t, err, codes.Unauthenticated)

	// Being able to manage trust would be a way around it.
	_, err = accessClient.ListClients(t.Context(), &dicerdv1.ListClientsRequest{})
	wantCode(t, err, codes.Unauthenticated)
}

func TestEnrolledClientIsServed(t *testing.T) {
	api := newTLSAPI(t)
	daemon, accessClient, fingerprint := api.dial(t, api.fingerprint)

	api.enroll(t, accessClient, "laptop")

	if _, err := daemon.ListNetworks(t.Context(), &dicerdv1.ListNetworksRequest{}); err != nil {
		t.Fatalf("ListNetworks after enrolling: %v", err)
	}

	clients, err := accessClient.ListClients(t.Context(), &dicerdv1.ListClientsRequest{})
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	// The daemon trusts the key the client connected with, not anything
	// the client claimed in the request.
	if len(clients.GetClients()) != 1 || clients.GetClients()[0].GetFingerprint() != fingerprint {
		t.Errorf("clients = %v, want the connecting certificate", clients.GetClients())
	}

	// The whole certificate is kept, and it is the one the client
	// connected with.
	client, err := accessClient.GetClient(t.Context(), &dicerdv1.GetClientRequest{Name: "laptop"})
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	cert, err := certificate.ParsePEM(client.GetCertificate())
	if err != nil {
		t.Fatalf("parse the stored certificate: %v", err)
	}
	if certificate.Fingerprint(cert.Raw) != fingerprint {
		t.Error("the stored certificate is not the one the client connected with")
	}
	if client.GetSubject() != "CN=client" || client.GetExpireTime() == nil {
		t.Errorf("subject = %q, expires = %v; want what the certificate says",
			client.GetSubject(), client.GetExpireTime())
	}
}

func TestEnrollWithWrongSecretIsRefused(t *testing.T) {
	api := newTLSAPI(t)
	_, accessClient, _ := api.dial(t, api.fingerprint)

	if _, _, err := api.access.CreateToken("laptop", dicer.DefaultTokenTTL); err != nil {
		t.Fatal(err)
	}

	_, err := accessClient.Enroll(t.Context(), &dicerdv1.EnrollRequest{Secret: "guess"})
	wantCode(t, err, codes.Unauthenticated)
}

func TestDeletedClientIsRefused(t *testing.T) {
	api := newTLSAPI(t)
	daemon, accessClient, _ := api.dial(t, api.fingerprint)
	api.enroll(t, accessClient, "laptop")

	if _, err := accessClient.DeleteClient(t.Context(), &dicerdv1.DeleteClientRequest{Name: "laptop"}); err != nil {
		t.Fatalf("DeleteClient: %v", err)
	}

	// The connection is still open; the next request on it is refused.
	_, err := daemon.ListNetworks(t.Context(), &dicerdv1.ListNetworksRequest{})
	wantCode(t, err, codes.Unauthenticated)
}

// A client that pins one fingerprint must refuse a daemon presenting another,
// or anyone in the middle could collect its requests.
func TestClientRefusesAnImpostor(t *testing.T) {
	api := newTLSAPI(t)
	impostor := "sha256:" + "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	daemon, _, _ := api.dial(t, impostor)

	_, err := daemon.ListNetworks(t.Context(), &dicerdv1.ListNetworksRequest{})
	wantCode(t, err, codes.Unavailable)

	// The refusal says what it may mean and what to do about it.
	if !strings.Contains(err.Error(), "re-create the remote") {
		t.Errorf("error = %v, want it to explain the mismatch", err)
	}
}

// Streaming calls pass through the same gate as unary ones.
func TestStreamingCallsAreAuthenticated(t *testing.T) {
	api := newTLSAPI(t)
	daemon, _, _ := api.dial(t, api.fingerprint)

	stream, err := daemon.PullImage(t.Context(), &dicerdv1.PullImageRequest{Ref: "alpine"})
	if err == nil {
		_, err = stream.Recv()
	}
	wantCode(t, err, codes.Unauthenticated)
}

// Enrolment trusts the key a caller connected with, so a caller with none --
// one on the API socket -- has nothing to enrol.
func TestEnrollOnTheSocketIsRefused(t *testing.T) {
	h := &accessHandler{}

	_, err := h.Enroll(context.Background(), &dicerdv1.EnrollRequest{Secret: "s"})
	wantCode(t, err, codes.FailedPrecondition)
}

// A local caller is identified as local, which is what the audit log names.
func TestLocalAuthenticationIdentifiesLocalCallers(t *testing.T) {
	var got access.Identity

	server := grpc.NewServer(
		grpc.Creds(insecure.NewCredentials()),
		grpc.ChainUnaryInterceptor(LocalAuthentication().UnaryInterceptor(),
			func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				got, _ = access.FromContext(ctx)
				return handler(ctx, req)
			}),
	)
	definitions, err := filestore.NewManager(filestore.Config{
		DataDir: filepath.Join(t.TempDir(), "data"),
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	NewServer(Config{Definitions: definitions}).Register(server)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := dicerdv1.NewDaemonServiceClient(conn).ListNetworks(t.Context(), &dicerdv1.ListNetworksRequest{}); err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	if !got.IsLocal() {
		t.Errorf("identity = %s, want local", got)
	}
}
