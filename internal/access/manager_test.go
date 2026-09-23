// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package access

import (
	"crypto/x509"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/certificate"
)

// fakeStore is an in-memory Store.
type fakeStore struct {
	clients map[string]dicer.TrustedClient // by name
	tokens  map[string]dicer.AccessToken   // by name
}

func newFakeStore() *fakeStore {
	return &fakeStore{clients: make(map[string]dicer.TrustedClient), tokens: make(map[string]dicer.AccessToken)}
}

func (f *fakeStore) CreateClient(c dicer.TrustedClient) error {
	if _, ok := f.clients[c.Name]; ok {
		return dicer.ErrExists
	}
	f.clients[c.Name] = c
	return nil
}

func (f *fakeStore) GetClient(key string) (dicer.TrustedClient, error) {
	for _, c := range f.clients {
		if c.Name == key || c.Fingerprint == key {
			return c, nil
		}
	}
	return dicer.TrustedClient{}, dicer.ErrNotFound
}

func (f *fakeStore) ListClients() ([]dicer.TrustedClient, error) {
	return slices.Collect(maps.Values(f.clients)), nil
}

func (f *fakeStore) DeleteClient(key string) error {
	c, err := f.GetClient(key)
	if err != nil {
		return err
	}
	delete(f.clients, c.Name)
	return nil
}

func (f *fakeStore) CreateToken(t dicer.AccessToken) error {
	if _, ok := f.tokens[t.Name]; ok {
		return dicer.ErrExists
	}
	f.tokens[t.Name] = t
	return nil
}

func (f *fakeStore) GetToken(key string) (dicer.AccessToken, error) {
	for _, t := range f.tokens {
		if t.Name == key || t.SecretHash == key {
			return t, nil
		}
	}
	return dicer.AccessToken{}, dicer.ErrNotFound
}

func (f *fakeStore) ListTokens() ([]dicer.AccessToken, error) {
	return slices.Collect(maps.Values(f.tokens)), nil
}

func (f *fakeStore) DeleteToken(key string) error {
	t, err := f.GetToken(key)
	if err != nil {
		return err
	}
	delete(f.tokens, t.Name)
	return nil
}

const serverFingerprint = "sha256:" + "aa00000000000000000000000000000000000000000000000000000000000000"

// newCertificate returns a freshly generated client certificate.
func newCertificate(t *testing.T, commonName string) *x509.Certificate {
	t.Helper()

	certPEM, _, err := certificate.Generate(commonName)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := certificate.ParsePEM(string(certPEM))
	if err != nil {
		t.Fatal(err)
	}

	return cert
}

// newTestManager returns a Manager over an empty store, reachable at one
// address, whose clock the test controls.
func newTestManager(t *testing.T) (*Manager, *fakeStore, *time.Time) {
	t.Helper()

	store := newFakeStore()
	m := NewManager(Config{
		Store:       store,
		Fingerprint: serverFingerprint,
		Addresses:   []string{"192.0.2.1:7443"},
	})

	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	return m, store, &now
}

func TestEnrollTrustsTheClient(t *testing.T) {
	m, _, _ := newTestManager(t)
	clientCert := newCertificate(t, "alice@laptop")

	join, _, err := m.CreateToken("laptop", dicer.DefaultTokenTTL)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	// The token has to survive being copied between terminals.
	parsed, err := dicer.ParseJoinToken(join.String())
	if err != nil {
		t.Fatalf("dicer.ParseJoinToken: %v", err)
	}
	if parsed.Fingerprint != serverFingerprint || parsed.Name != "laptop" {
		t.Errorf("parsed token = %+v, want the server fingerprint and the name", parsed)
	}

	client, err := m.Enroll(parsed.Secret, clientCert)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if client.Name != "laptop" {
		t.Errorf("client name = %q, want the token's", client.Name)
	}

	got, err := m.Authenticate(certificate.Fingerprint(clientCert.Raw))
	if err != nil {
		t.Fatalf("Authenticate after enrolling: %v", err)
	}
	if got.Name != "laptop" {
		t.Errorf("authenticated as %q, want laptop", got.Name)
	}

	// The whole certificate is kept, so what it says can be shown.
	stored, err := got.ParseCertificate()
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if stored.Subject.CommonName != "alice@laptop" {
		t.Errorf("stored certificate subject = %q, want the one enrolled", stored.Subject.CommonName)
	}
}

func TestTokenIsSingleUse(t *testing.T) {
	m, _, _ := newTestManager(t)

	join, _, err := m.CreateToken("laptop", dicer.DefaultTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enroll(join.Secret, newCertificate(t, "client")); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Enroll(join.Secret, newCertificate(t, "other")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("second Enroll = %v, want ErrInvalidToken", err)
	}
}

func TestExpiredTokenIsRefused(t *testing.T) {
	m, store, now := newTestManager(t)

	join, _, err := m.CreateToken("laptop", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	*now = now.Add(time.Minute)

	if _, err := m.Enroll(join.Secret, newCertificate(t, "client")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Enroll with an expired token = %v, want ErrInvalidToken", err)
	}
	if len(store.tokens) != 0 {
		t.Error("an expired token was left behind")
	}
}

func TestUnknownSecretIsRefused(t *testing.T) {
	m, _, _ := newTestManager(t)

	if _, err := m.Enroll("guess", newCertificate(t, "client")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Enroll = %v, want ErrInvalidToken", err)
	}
}

// The secret is only ever shown once. Whoever reads the daemon's files must
// not find it there.
func TestSecretIsNotStored(t *testing.T) {
	m, store, _ := newTestManager(t)

	join, _, err := m.CreateToken("laptop", dicer.DefaultTokenTTL)
	if err != nil {
		t.Fatal(err)
	}

	for _, token := range store.tokens {
		if strings.Contains(token.SecretHash, join.Secret) {
			t.Error("the secret is recorded in the clear")
		}
	}
}

func TestCreateTokenRejections(t *testing.T) {
	m, _, _ := newTestManager(t)

	if _, _, err := m.CreateToken("../laptop", dicer.DefaultTokenTTL); !errors.Is(err, dicer.ErrInvalidArgument) {
		t.Errorf("invalid name: %v, want ErrInvalidArgument", err)
	}
	if _, _, err := m.CreateToken("laptop", 0); !errors.Is(err, dicer.ErrInvalidArgument) {
		t.Errorf("zero lifetime: %v, want ErrInvalidArgument", err)
	}

	join, _, err := m.CreateToken("laptop", dicer.DefaultTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enroll(join.Secret, newCertificate(t, "client")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.CreateToken("laptop", dicer.DefaultTokenTTL); !errors.Is(err, dicer.ErrExists) {
		t.Errorf("name already trusted: %v, want ErrExists", err)
	}

	unreachable := NewManager(Config{Store: newFakeStore(), Fingerprint: serverFingerprint})
	if _, _, err := unreachable.CreateToken("laptop", dicer.DefaultTokenTTL); !errors.Is(err, ErrRemoteAccessDisabled) {
		t.Errorf("no network listener: %v, want ErrRemoteAccessDisabled", err)
	}
}

// An expired token must not keep its name from being invited again.
func TestExpiredTokenFreesItsName(t *testing.T) {
	m, _, now := newTestManager(t)

	if _, _, err := m.CreateToken("laptop", time.Minute); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)

	if _, _, err := m.CreateToken("laptop", dicer.DefaultTokenTTL); err != nil {
		t.Errorf("CreateToken after the first expired: %v", err)
	}
}

// Only tokens that can still be used are listed.
func TestListTokensLeavesOutExpired(t *testing.T) {
	m, _, now := newTestManager(t)

	if _, _, err := m.CreateToken("old", time.Minute); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	if _, _, err := m.CreateToken("new", dicer.DefaultTokenTTL); err != nil {
		t.Fatal(err)
	}

	tokens, err := m.ListTokens()
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "new" {
		t.Errorf("ListTokens = %+v, want only the unexpired token", tokens)
	}
}

// A withdrawn token cannot be enrolled with.
func TestDeleteTokenWithdraws(t *testing.T) {
	m, _, _ := newTestManager(t)

	join, _, err := m.CreateToken("laptop", dicer.DefaultTokenTTL)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.DeleteToken("laptop"); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if _, err := m.Enroll(join.Secret, newCertificate(t, "client")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("Enroll with a deleted token = %v, want ErrInvalidToken", err)
	}
	if err := m.DeleteToken("laptop"); !errors.Is(err, dicer.ErrNotFound) {
		t.Errorf("DeleteToken twice = %v, want ErrNotFound", err)
	}
}

func TestOneCertificateCannotEnrolTwice(t *testing.T) {
	m, _, _ := newTestManager(t)
	cert := newCertificate(t, "client")

	for i, name := range []string{"laptop", "desktop"} {
		join, _, err := m.CreateToken(name, dicer.DefaultTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.Enroll(join.Secret, cert)
		if i == 1 && !errors.Is(err, dicer.ErrExists) {
			t.Errorf("second enrolment of one certificate = %v, want ErrExists", err)
		}
	}
}

func TestDeleteClientRevokes(t *testing.T) {
	m, _, _ := newTestManager(t)
	cert := newCertificate(t, "client")

	join, _, err := m.CreateToken("laptop", dicer.DefaultTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enroll(join.Secret, cert); err != nil {
		t.Fatal(err)
	}

	if err := m.DeleteClient("laptop"); err != nil {
		t.Fatalf("DeleteClient: %v", err)
	}
	if _, err := m.Authenticate(certificate.Fingerprint(cert.Raw)); !errors.Is(err, ErrDenied) {
		t.Errorf("Authenticate after removal = %v, want ErrDenied", err)
	}
}

func TestAuthenticateRejectsNonFingerprints(t *testing.T) {
	m, store, _ := newTestManager(t)
	store.clients["laptop"] = dicer.TrustedClient{Name: "laptop", Fingerprint: serverFingerprint}

	// A lookup by name must not authenticate anyone.
	if _, err := m.Authenticate("laptop"); !errors.Is(err, ErrDenied) {
		t.Errorf("Authenticate(name) = %v, want ErrDenied", err)
	}
}

// Replacing the daemon's key re-enrols every client: its entry is deleted
// and it is invited again under the same name, keeping the key it has. The
// README documents exactly this, so it has to work.
func TestReEnrolAfterDelete(t *testing.T) {
	m, _, _ := newTestManager(t)
	cert := newCertificate(t, "alice@laptop")

	for range 2 {
		join, _, err := m.CreateToken("laptop", dicer.DefaultTokenTTL)
		if err != nil {
			t.Fatalf("CreateToken: %v", err)
		}
		if _, err := m.Enroll(join.Secret, cert); err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		if err := m.DeleteClient("laptop"); err != nil {
			t.Fatalf("DeleteClient: %v", err)
		}
	}
}
