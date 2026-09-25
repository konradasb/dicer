// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package registry

import (
	"fmt"
	"os"
	"strings"

	"github.com/docker/docker-credential-helpers/client"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

// Auth is how to log in to one registry: with a username and a password,
// given or read from a file, or through a credential helper.
type Auth struct {
	Username     string
	Password     string
	PasswordFile string

	// CredentialHelper names a docker-credential-<name> program on the
	// daemon's PATH, which gives the credentials each time they are needed.
	CredentialHelper string
}

// CredentialsError reports that the credentials configured for a registry
// could not be had: a password file that cannot be read, or a credential
// helper that fails.
type CredentialsError struct {
	Registry string
	Err      error
}

// Error implements error.
func (e *CredentialsError) Error() string {
	return fmt.Sprintf("credentials for %s: %v", e.Registry, e.Err)
}

// Unwrap returns the cause.
func (e *CredentialsError) Unwrap() error { return e.Err }

// keychain gives each registry the credentials configured for it, by host,
// and every other registry none: an image there is pulled anonymously.
type keychain map[string]Auth

// NewKeychain returns a keychain of the credentials in auths, keyed by
// registry host. Docker Hub may be named docker.io.
func NewKeychain(auths map[string]Auth) authn.Keychain {
	k := make(keychain, len(auths))
	for host, auth := range auths {
		k[canonicalHost(host)] = auth
	}
	return k
}

// Resolve returns the credentials for a registry. A password file is read
// each time, so a rotated password is used without a restart.
func (k keychain) Resolve(res authn.Resource) (authn.Authenticator, error) {
	auth, ok := k[canonicalHost(res.RegistryStr())]
	if !ok {
		return authn.Anonymous, nil
	}

	if auth.CredentialHelper != "" {
		return helperCredentials(auth.CredentialHelper, res.RegistryStr())
	}

	password := auth.Password
	if auth.PasswordFile != "" {
		data, err := os.ReadFile(auth.PasswordFile)
		if err != nil {
			return nil, &CredentialsError{Registry: res.RegistryStr(), Err: err}
		}
		password = strings.TrimSpace(string(data))
	}

	return authn.FromConfig(authn.AuthConfig{Username: auth.Username, Password: password}), nil
}

// helperCredentials asks a credential helper for a registry's credentials.
func helperCredentials(helper, host string) (authn.Authenticator, error) {
	program := "docker-credential-" + helper

	creds, err := client.Get(client.NewShellProgramFunc(program), host)
	if err != nil {
		return nil, &CredentialsError{Registry: host, Err: fmt.Errorf("%s: %w", program, err)}
	}

	// By the helpers' convention, this username means the secret is an
	// identity token rather than a password.
	if creds.Username == "<token>" {
		return authn.FromConfig(authn.AuthConfig{IdentityToken: creds.Secret}), nil
	}
	return authn.FromConfig(authn.AuthConfig{Username: creds.Username, Password: creds.Secret}), nil
}

// canonicalHost names a registry as references resolve it, so that
// docker.io and index.docker.io are the same registry.
func canonicalHost(host string) string {
	host = strings.ToLower(host)
	switch host {
	case "docker.io", "registry-1.docker.io":
		return name.DefaultRegistry
	}
	return host
}
