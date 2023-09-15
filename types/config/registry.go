package config

import "github.com/containers/image/v5/types"

type RegistryEntries []RegistryEntry

type RegistryEntry struct {
	Domain   string `yaml:"domain,omitempty"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	Insecure bool   `yaml:"insecure,omitempty"`
}

func (e RegistryEntries) GetAuthConfigFor(domain string) *RegistryEntry {
	for _, entry := range e {
		if entry.Domain == domain {
			return &entry
		}
	}
	return nil
}

func (e RegistryEntries) GetSystemContextFor(domain string) *types.SystemContext {
	for _, entry := range e {
		if entry.Domain == domain {
			return entry.GetSystemContext()
		}
	}
	return &types.SystemContext{}
}

func (e RegistryEntry) ConvertToAuthConfig() *types.DockerAuthConfig {
	return &types.DockerAuthConfig{
		Username: e.Username,
		Password: e.Password,
	}
}

func (e RegistryEntry) GetSystemContext() *types.SystemContext {
	authConfig := e.ConvertToAuthConfig()

	return &types.SystemContext{
		DockerDaemonInsecureSkipTLSVerify: e.Insecure,
		DockerInsecureSkipTLSVerify:       types.NewOptionalBool(e.Insecure),
		DockerAuthConfig:                  authConfig,
	}
}
