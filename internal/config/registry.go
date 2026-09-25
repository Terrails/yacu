package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/containers/image/v5/pkg/docker/config"
	"github.com/containers/image/v5/types"
	"github.com/distribution/reference"
)

type RegistryEntries []RegistryEntry

type RegistryEntry struct {
	Domain   string `yaml:"domain,omitempty"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	// alternatives to Password, read when the config is loaded
	PasswordFile string `yaml:"password_file,omitempty"`
	PasswordEnv  string `yaml:"password_env,omitempty"`
	Insecure     bool   `yaml:"insecure,omitempty"`
}

// Reads the passwords of entries given as password_file or password_env.
func (e RegistryEntries) LoadPasswords() error {
	for i := range e {
		entry := &e[i]

		set := 0
		for _, value := range []string{entry.Password, entry.PasswordFile, entry.PasswordEnv} {
			if len(value) > 0 {
				set++
			}
		}
		if set > 1 {
			return fmt.Errorf("registry %q: only one of password, password_file and password_env can be set", entry.Domain)
		}

		switch {
		case len(entry.PasswordFile) > 0:
			data, err := os.ReadFile(entry.PasswordFile)
			if err != nil {
				return fmt.Errorf("registry %q: reading password_file failed: %w", entry.Domain, err)
			}
			// files usually end with a newline, which is not part of the password
			entry.Password = strings.TrimRight(string(data), "\r\n")
		case len(entry.PasswordEnv) > 0:
			value, ok := os.LookupEnv(entry.PasswordEnv)
			if !ok {
				return fmt.Errorf("registry %q: environment variable %s of password_env is not set", entry.Domain, entry.PasswordEnv)
			}
			entry.Password = value
		}
	}
	return nil
}

// Docker Hub is reached and logged in to under several host names, and `docker login`
// stores it as https://index.docker.io/v1/. All of them are reduced to docker.io,
// which is what image references are resolved to.
func normalizeDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")
	domain, _, _ = strings.Cut(domain, "/")

	switch domain {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return "docker.io"
	}
	return domain
}

// the configured entry for the registry at domain
func (e RegistryEntries) entryFor(domain string) *RegistryEntry {
	domain = normalizeDomain(domain)
	for i := range e {
		if normalizeDomain(e[i].Domain) == domain {
			return &e[i]
		}
	}
	return nil
}

// Credentials for the registry of named: those configured for it, otherwise the
// ones stored by `docker login` (or `podman login`) in the Docker config file
// ($DOCKER_CONFIG or ~/.docker/config.json) or its credential helpers.
// Empty when there are none, for anonymous access.
func (e RegistryEntries) GetCredentials(named reference.Named) (types.DockerAuthConfig, error) {
	if entry := e.entryFor(reference.Domain(named)); entry != nil && (len(entry.Username) > 0 || len(entry.Password) > 0) {
		return types.DockerAuthConfig{Username: entry.Username, Password: entry.Password}, nil
	}

	credentials, err := config.GetCredentialsForRef(nil, named)
	if err != nil {
		return types.DockerAuthConfig{}, fmt.Errorf("reading stored credentials for %s failed: %w", reference.Domain(named), err)
	}
	return credentials, nil
}

// The context to access the registry of named with.
func (e RegistryEntries) GetSystemContextFor(named reference.Named) (*types.SystemContext, error) {
	credentials, err := e.GetCredentials(named)
	if err != nil {
		return nil, err
	}

	sysCtx := &types.SystemContext{DockerAuthConfig: &credentials}
	if entry := e.entryFor(reference.Domain(named)); entry != nil && entry.Insecure {
		sysCtx.DockerDaemonInsecureSkipTLSVerify = true
		sysCtx.DockerInsecureSkipTLSVerify = types.NewOptionalBool(true)
	}
	return sysCtx, nil
}
