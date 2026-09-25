// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

// credentials resolves the credentials a keychain gives a registry.
func credentials(t *testing.T, k authn.Keychain, registry string) *authn.AuthConfig {
	t.Helper()

	reg, err := name.NewRegistry(registry)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := k.Resolve(reg)
	if err != nil {
		t.Fatalf("resolve %s: %v", registry, err)
	}
	cfg, err := auth.Authorization()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestKeychainGivesARegistryItsPassword(t *testing.T) {
	k := NewKeychain(map[string]Auth{"ghcr.io": {Username: "bot", Password: "s3cret"}})

	if got := credentials(t, k, "ghcr.io"); got.Username != "bot" || got.Password != "s3cret" {
		t.Errorf("ghcr.io = %+v, want bot / s3cret", got)
	}
}

// A registry the keychain does not name is pulled from anonymously, as if
// nothing were configured: no other credentials are looked for.
func TestKeychainLeavesOtherRegistriesAnonymous(t *testing.T) {
	k := NewKeychain(map[string]Auth{"ghcr.io": {Username: "bot", Password: "s3cret"}})

	if got := credentials(t, k, "quay.io"); *got != (authn.AuthConfig{}) {
		t.Errorf("quay.io = %+v, want no credentials", got)
	}
}

// A password file is read when the credentials are needed, and the newline
// an editor leaves at its end is not part of the password.
func TestKeychainReadsThePasswordFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	k := NewKeychain(map[string]Auth{"ghcr.io": {Username: "bot", PasswordFile: file}})

	if got := credentials(t, k, "ghcr.io"); got.Password != "first" {
		t.Errorf("password = %q, want the file's, without its newline", got.Password)
	}

	// A rotated password is used at once.
	if err := os.WriteFile(file, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := credentials(t, k, "ghcr.io"); got.Password != "second" {
		t.Errorf("password after rotation = %q, want second", got.Password)
	}
}

// Docker Hub is docker.io to people and index.docker.io to references.
func TestKeychainNamesDockerHubEitherWay(t *testing.T) {
	k := NewKeychain(map[string]Auth{"docker.io": {Username: "bot", Password: "s3cret"}})

	ref, err := name.ParseReference("alpine:3.21")
	if err != nil {
		t.Fatal(err)
	}
	if got := credentials(t, k, ref.Context().RegistryStr()); got.Username != "bot" {
		t.Errorf("%s = %+v, want docker.io's credentials", ref.Context().RegistryStr(), got)
	}
}

// A credential helper is a program on PATH that prints the credentials as
// JSON; the helpers' "<token>" username marks an identity token.
func TestKeychainAsksTheCredentialHelper(t *testing.T) {
	dir := t.TempDir()
	helper := `#!/bin/sh
read -r host
case "$host" in
ghcr.io) echo '{"ServerURL":"ghcr.io","Username":"bot","Secret":"from-helper"}' ;;
*) echo '{"ServerURL":"'"$host"'","Username":"<token>","Secret":"identity"}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker-credential-fake"), []byte(helper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	k := NewKeychain(map[string]Auth{
		"ghcr.io": {CredentialHelper: "fake"},
		"quay.io": {CredentialHelper: "fake"},
	})

	if got := credentials(t, k, "ghcr.io"); got.Username != "bot" || got.Password != "from-helper" {
		t.Errorf("ghcr.io = %+v, want the helper's bot / from-helper", got)
	}
	if got := credentials(t, k, "quay.io"); got.IdentityToken != "identity" {
		t.Errorf("quay.io = %+v, want the helper's identity token", got)
	}
}
