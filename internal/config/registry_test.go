package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containers/image/v5/types"
	"github.com/distribution/reference"
)

// isolates the credentials stored by `docker login` from the host's, returning the docker config directory
func isolateStoredCredentials(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("REGISTRY_AUTH_FILE", "")

	dockerConfig := filepath.Join(home, "docker")
	if err := os.MkdirAll(dockerConfig, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dockerConfig)
	return dockerConfig
}

// stores credentials the way `docker login` does
func dockerLogin(t *testing.T, dockerConfig, server, username, password string) {
	t.Helper()
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	data := `{"auths": {"` + server + `": {"auth": "` + auth + `"}}}`
	if err := os.WriteFile(filepath.Join(dockerConfig, "config.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func parse(t *testing.T, ref string) reference.Named {
	t.Helper()
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		t.Fatal(err)
	}
	return named
}

func TestGetCredentialsPrefersConfiguredEntry(t *testing.T) {
	dockerConfig := isolateStoredCredentials(t)
	dockerLogin(t, dockerConfig, "ghcr.io", "stored", "stored")
	entries := RegistryEntries{{Domain: "GHCR.io", Username: "configured", Password: "secret"}}

	credentials, err := entries.GetCredentials(parse(t, "ghcr.io/owner/app:latest"))
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Username != "configured" || credentials.Password != "secret" {
		t.Fatalf("got %+v, want the configured credentials", credentials)
	}
}

func TestGetCredentialsFallsBackToDockerLogin(t *testing.T) {
	dockerConfig := isolateStoredCredentials(t)
	// the key docker login uses for Docker Hub
	dockerLogin(t, dockerConfig, "https://index.docker.io/v1/", "hubuser", "hubtoken")
	// an entry without credentials, e.g. only marking the registry insecure
	entries := RegistryEntries{{Domain: "docker.io", Insecure: true}}

	credentials, err := entries.GetCredentials(parse(t, "nginx:latest"))
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Username != "hubuser" || credentials.Password != "hubtoken" {
		t.Fatalf("got %+v, want the credentials stored by docker login", credentials)
	}

	anonymous, err := entries.GetCredentials(parse(t, "ghcr.io/owner/app:latest"))
	if err != nil {
		t.Fatal(err)
	}
	if len(anonymous.Username) > 0 || len(anonymous.Password) > 0 {
		t.Fatalf("got %+v for a registry without credentials, want none", anonymous)
	}
}

func TestGetCredentialsMatchesDockerHubAliases(t *testing.T) {
	isolateStoredCredentials(t)

	for _, domain := range []string{"docker.io", "index.docker.io", "registry-1.docker.io", "https://index.docker.io/v1/"} {
		t.Run(domain, func(t *testing.T) {
			entries := RegistryEntries{{Domain: domain, Username: "user", Password: "pass"}}

			credentials, err := entries.GetCredentials(parse(t, "library/nginx:latest"))
			if err != nil {
				t.Fatal(err)
			}
			if credentials.Username != "user" {
				t.Fatalf("entry for %q was not used for a Docker Hub image", domain)
			}
		})
	}
}

func TestGetSystemContextForInsecureRegistry(t *testing.T) {
	isolateStoredCredentials(t)
	entries := RegistryEntries{{Domain: "registry.lan:5000", Insecure: true}}

	sysCtx, err := entries.GetSystemContextFor(parse(t, "registry.lan:5000/app:latest"))
	if err != nil {
		t.Fatal(err)
	}
	if !sysCtx.DockerDaemonInsecureSkipTLSVerify || sysCtx.DockerInsecureSkipTLSVerify != types.OptionalBoolTrue {
		t.Fatalf("registry not marked insecure: %+v", sysCtx)
	}
}

func TestLoadPasswords(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("from-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YACU_TEST_PASSWORD", "from-env")

	entries := RegistryEntries{
		{Domain: "a.lan", Password: "inline"},
		{Domain: "b.lan", PasswordFile: passwordFile},
		{Domain: "c.lan", PasswordEnv: "YACU_TEST_PASSWORD"},
	}
	if err := entries.LoadPasswords(); err != nil {
		t.Fatal(err)
	}

	for i, want := range []string{"inline", "from-file", "from-env"} {
		if entries[i].Password != want {
			t.Errorf("password of %s is %q, want %q", entries[i].Domain, entries[i].Password, want)
		}
	}
}

func TestLoadPasswordsRejectsInvalidEntries(t *testing.T) {
	tests := map[string]RegistryEntry{
		"several sources": {Domain: "a.lan", Password: "inline", PasswordEnv: "YACU_TEST_PASSWORD"},
		"missing file":    {Domain: "a.lan", PasswordFile: filepath.Join(t.TempDir(), "missing")},
		"unset variable":  {Domain: "a.lan", PasswordEnv: "YACU_TEST_UNSET_PASSWORD"},
	}
	for name, entry := range tests {
		t.Run(name, func(t *testing.T) {
			err := RegistryEntries{entry}.LoadPasswords()
			if err == nil || !strings.Contains(err.Error(), "a.lan") {
				t.Fatalf("expected an error naming the registry, got %v", err)
			}
		})
	}
}
