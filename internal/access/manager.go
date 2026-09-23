// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package access

import (
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/certificate"
)

// Store keeps trusted clients and outstanding tokens. It is declared here,
// and satisfied by internal/filestore, so that access depends on what it needs
// rather than on the store that happens to provide it.
//
// Clients are looked up by name or fingerprint, tokens by name or secret
// hash. Neither kind of key can be mistaken for a name: a fingerprint has a
// colon, which no name may, and a secret hash is 64 characters with no dot,
// which is longer than a name's labels may be.
type Store interface {
	CreateClient(c dicer.TrustedClient) error
	GetClient(nameOrFingerprint string) (dicer.TrustedClient, error)
	ListClients() ([]dicer.TrustedClient, error)
	DeleteClient(nameOrFingerprint string) error

	CreateToken(t dicer.AccessToken) error
	GetToken(nameOrSecretHash string) (dicer.AccessToken, error)
	ListTokens() ([]dicer.AccessToken, error)
	DeleteToken(nameOrSecretHash string) error
}

// Config holds the dependencies for a Manager.
type Config struct {
	Store Store

	// Fingerprint is the daemon's certificate fingerprint, and Addresses
	// the host:port pairs it listens on. Both go into every token. With no
	// addresses the daemon is not reachable over the network, and no token
	// can be created.
	Fingerprint string
	Addresses   []string

	// Logger is where the Manager logs. Optional: defaults to slog.Default.
	Logger *slog.Logger
}

// Manager issues tokens, enrols clients and recognises them afterwards.
type Manager struct {
	store       Store
	fingerprint string
	addresses   []string
	logger      *slog.Logger

	// now is the clock tokens expire by. It is a field so that tests need
	// not wait for one to.
	now func() time.Time

	// mu serialises the changes to trust, so that a token cannot be spent
	// twice by enrolments racing each other.
	mu sync.Mutex
}

// NewManager creates a Manager.
func NewManager(cfg Config) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Manager{
		store:       cfg.Store,
		fingerprint: cfg.Fingerprint,
		addresses:   slices.Clone(cfg.Addresses),
		logger:      cfg.Logger.With("component", "access"),
		now:         time.Now,
	}
}

// CreateToken invites a client to enrol as name, for ttl. It returns the
// token to hand to the client, which is the only time the secret in it is
// available: the daemon keeps only its hash.
func (m *Manager) CreateToken(name string, ttl time.Duration) (dicer.JoinToken, dicer.AccessToken, error) {
	if err := dicer.ValidateName(name); err != nil {
		return dicer.JoinToken{}, dicer.AccessToken{}, err
	}
	if ttl <= 0 {
		return dicer.JoinToken{}, dicer.AccessToken{}, dicer.InvalidArgument("a token's lifetime must be positive")
	}
	if len(m.addresses) == 0 {
		return dicer.JoinToken{}, dicer.AccessToken{}, dicer.InvalidState("%w", ErrRemoteAccessDisabled)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.pruneExpired()

	if _, err := m.store.GetClient(name); err == nil {
		return dicer.JoinToken{}, dicer.AccessToken{}, dicer.Exists("client %q already exists", name)
	}

	secret, err := newSecret()
	if err != nil {
		return dicer.JoinToken{}, dicer.AccessToken{}, err
	}

	now := m.now()
	token := dicer.AccessToken{
		Name:       name,
		SecretHash: hashSecret(secret),
		ExpiresAt:  now.Add(ttl),
		CreatedAt:  now,
	}
	if err := m.store.CreateToken(token); err != nil {
		return dicer.JoinToken{}, dicer.AccessToken{}, err
	}

	m.logger.Info("created enrolment token", "client", name, "expires_at", token.ExpiresAt)

	return dicer.JoinToken{
		Name:        name,
		Addresses:   slices.Clone(m.addresses),
		Fingerprint: m.fingerprint,
		Secret:      secret,
	}, token, nil
}

// ListTokens returns every token that can still be enrolled with, sorted by
// name. Expired ones are pruned first rather than shown.
func (m *Manager) ListTokens() ([]dicer.AccessToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.pruneExpired()

	return m.store.ListTokens()
}

// DeleteToken withdraws the token created for name, before it is used.
func (m *Manager) DeleteToken(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	token, err := m.store.GetToken(name)
	if err != nil {
		return err
	}
	if err := m.store.DeleteToken(token.SecretHash); err != nil {
		return err
	}

	m.logger.Info("deleted enrolment token", "client", token.Name)

	return nil
}

// Enroll spends a token: the client presenting secret, with cert, is trusted
// from now on under the token's name. cert must be the one the client
// connected with, which proves it holds the key.
func (m *Manager) Enroll(secret string, cert *x509.Certificate) (dicer.TrustedClient, error) {
	fingerprint := certificate.Fingerprint(cert.Raw)

	m.mu.Lock()
	defer m.mu.Unlock()

	token, err := m.store.GetToken(hashSecret(secret))
	if errors.Is(err, dicer.ErrNotFound) {
		return dicer.TrustedClient{}, ErrInvalidToken
	}
	if err != nil {
		return dicer.TrustedClient{}, err
	}

	// Spent or not, an expired token is of no further use.
	if err := m.store.DeleteToken(token.SecretHash); err != nil {
		return dicer.TrustedClient{}, fmt.Errorf("spend token: %w", err)
	}
	if token.Expired(m.now()) {
		return dicer.TrustedClient{}, ErrInvalidToken
	}

	if existing, err := m.store.GetClient(fingerprint); err == nil {
		return dicer.TrustedClient{}, dicer.Exists("this certificate is already trusted as client %q", existing.Name)
	}

	client := dicer.TrustedClient{
		Name:        token.Name,
		Fingerprint: fingerprint,
		Certificate: certificate.EncodePEM(cert.Raw),
		CreatedAt:   m.now(),
	}
	if err := m.store.CreateClient(client); err != nil {
		return dicer.TrustedClient{}, err
	}

	m.logger.Info("enrolled client", "client", client.Name, "fingerprint", client.Fingerprint)

	return client, nil
}

// Authenticate returns the trusted client a certificate fingerprint belongs
// to, or ErrDenied.
func (m *Manager) Authenticate(fingerprint string) (dicer.TrustedClient, error) {
	if certificate.ValidateFingerprint(fingerprint) != nil {
		return dicer.TrustedClient{}, ErrDenied
	}

	client, err := m.store.GetClient(fingerprint)
	if errors.Is(err, dicer.ErrNotFound) {
		return dicer.TrustedClient{}, ErrDenied
	}
	if err != nil {
		return dicer.TrustedClient{}, err
	}

	// The lookup accepts a name too. A fingerprint cannot be a valid name,
	// but the check costs nothing and does not rest on that.
	if client.Fingerprint != fingerprint {
		return dicer.TrustedClient{}, ErrDenied
	}

	return client, nil
}

// Fingerprint returns the daemon's certificate fingerprint, which clients
// pin, or "" if it does not listen on the network.
func (m *Manager) Fingerprint() string {
	return m.fingerprint
}

// Addresses returns the addresses written into tokens, or none if the daemon
// does not listen on the network.
func (m *Manager) Addresses() []string {
	return slices.Clone(m.addresses)
}

// GetClient returns a trusted client by name or fingerprint.
func (m *Manager) GetClient(nameOrFingerprint string) (dicer.TrustedClient, error) {
	return m.store.GetClient(nameOrFingerprint)
}

// ListClients returns every trusted client, sorted by name.
func (m *Manager) ListClients() ([]dicer.TrustedClient, error) {
	return m.store.ListClients()
}

// DeleteClient stops trusting a client, by name or fingerprint. Its next
// request is refused; connections it already has are not cut, but carry no
// further request that is not.
func (m *Manager) DeleteClient(nameOrFingerprint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	client, err := m.store.GetClient(nameOrFingerprint)
	if err != nil {
		return err
	}
	if err := m.store.DeleteClient(client.Name); err != nil {
		return err
	}

	m.logger.Info("removed trusted client", "client", client.Name, "fingerprint", client.Fingerprint)

	return nil
}

// pruneExpired deletes the tokens that can no longer be used, so that one
// does not hold a name hostage forever. The caller must hold m.mu.
func (m *Manager) pruneExpired() {
	tokens, err := m.store.ListTokens()
	if err != nil {
		m.logger.Warn("cannot list tokens to prune", "error", err)
		return
	}

	now := m.now()
	for _, t := range tokens {
		if !t.Expired(now) {
			continue
		}
		if err := m.store.DeleteToken(t.SecretHash); err != nil {
			m.logger.Warn("cannot delete expired token", "client", t.Name, "error", err)
		}
	}
}
