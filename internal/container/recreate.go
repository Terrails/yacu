package container

import (
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/terrails/yacu/internal/utils"
)

// Config to create the container's replacement with.
//
// When a container is created, the daemon fills everything the user left unset
// from the image (User, Env, Labels, Cmd, Entrypoint, ...), and the result is what
// inspecting the container returns. Recreating from that as-is would carry the old
// image's defaults over to the new one, e.g. an outdated VERSION environment
// variable. So every value equal to the old image's default is removed again to
// let the daemon fill it from the new image, undoing the daemon's merge.
//
// A value the user set explicitly to the image's default is indistinguishable from
// an inherited one and follows the new image's default from then on.
func (c *Container) CreateConfig() *container.Config {
	config := *c.Raw.Config

	// Docker names the container's host after its short ID unless one is given
	if len(c.ID) >= 12 && config.Hostname == utils.ShortId(c.ID) {
		config.Hostname = ""
	}

	// a container joining another's network namespace (network_mode container:<parent>)
	// is given the parent's host and domain name when started, and the daemon rejects
	// creating one with those or with exposed ports, which only the image's EXPOSE can
	// have put there
	if c.Raw.HostConfig != nil && c.Raw.HostConfig.NetworkMode.IsContainer() {
		config.Hostname = ""
		config.Domainname = ""
		config.ExposedPorts = nil
	}

	if c.Image == nil || c.Image.Raw == nil || c.Image.Raw.Config == nil {
		return &config
	}
	removeImageDefaults(&config, c.Image.Raw.Config, c.Raw.HostConfig)
	return &config
}

func removeImageDefaults(config *container.Config, image *dockerspec.DockerOCIImageConfig, hostConfig *container.HostConfig) {
	if config.User == image.User {
		config.User = ""
	}
	if config.WorkingDir == image.WorkingDir {
		config.WorkingDir = ""
	}
	if config.StopSignal == image.StopSignal {
		config.StopSignal = ""
	}

	config.Env = slices.DeleteFunc(slices.Clone(config.Env), func(env string) bool {
		return slices.Contains(image.Env, env)
	})

	if config.Labels != nil {
		config.Labels = maps.Clone(config.Labels)
		maps.DeleteFunc(config.Labels, func(key, value string) bool {
			imageValue, ok := image.Labels[key]
			return ok && imageValue == value
		})
	}

	// the image's Cmd is only inherited together with its Entrypoint, a Cmd given with
	// a custom Entrypoint is the user's even if it matches the image's
	if slices.Equal(config.Entrypoint, image.Entrypoint) {
		config.Entrypoint = nil
		if slices.Equal(config.Cmd, image.Cmd) {
			config.Cmd = nil
		}
	}

	if config.ExposedPorts != nil {
		config.ExposedPorts = maps.Clone(config.ExposedPorts)
		for port := range config.ExposedPorts {
			_, fromImage := image.ExposedPorts[string(port)]
			// kept for published ports in case the new image no longer exposes them
			_, published := hostConfig.PortBindings[port]
			if fromImage && !published {
				delete(config.ExposedPorts, port)
			}
		}
	}

	if config.Volumes != nil {
		config.Volumes = maps.Clone(config.Volumes)
		maps.DeleteFunc(config.Volumes, func(path string, _ struct{}) bool {
			_, fromImage := image.Volumes[path]
			return fromImage
		})
	}

	config.Healthcheck = removeHealthcheckDefaults(config.Healthcheck, image.Healthcheck)
}

// the daemon inherits each unset (zero) field of a healthcheck separately
func removeHealthcheckDefaults(healthcheck, image *container.HealthConfig) *container.HealthConfig {
	if healthcheck == nil || image == nil {
		return healthcheck
	}

	result := *healthcheck
	if slices.Equal(result.Test, image.Test) {
		result.Test = nil
	}
	if result.Interval == image.Interval {
		result.Interval = 0
	}
	if result.Timeout == image.Timeout {
		result.Timeout = 0
	}
	if result.StartPeriod == image.StartPeriod {
		result.StartPeriod = 0
	}
	if result.StartInterval == image.StartInterval {
		result.StartInterval = 0
	}
	if result.Retries == image.Retries {
		result.Retries = 0
	}

	if len(result.Test) == 0 && result.Interval == 0 && result.Timeout == 0 &&
		result.StartPeriod == 0 && result.StartInterval == 0 && result.Retries == 0 {
		return nil
	}
	return &result
}

// Host config to create the container's replacement with, given the config of the
// image it is created from.
//
// A volume mounted without a source (for a VOLUME in the image, `-v /data` or a
// compose service volume without one) is anonymous: every new container gets a
// fresh, empty one, so the replacement would lose its data. Like docker compose
// when it recreates a container, the replacement mounts the previous container's
// anonymous volume instead where the new image declares a VOLUME or the user
// declared a volume without a source. Mounts with a source are kept as they are.
func (c *Container) CreateHostConfig(newImage *dockerspec.DockerOCIImageConfig) *container.HostConfig {
	hostConfig := *c.Raw.HostConfig

	// paths at which a volume is mounted without a source
	anonymous := map[string]bool{}
	if newImage != nil {
		for target := range newImage.Volumes {
			anonymous[path.Clean(target)] = true
		}
	}
	for target := range c.CreateConfig().Volumes {
		anonymous[path.Clean(target)] = true
	}
	for _, m := range hostConfig.Mounts {
		if m.Type == mount.TypeVolume && len(m.Source) == 0 {
			anonymous[path.Clean(m.Target)] = true
		}
	}

	// paths at which the user mounts something with a source, those are kept as they are.
	// Binds also allow a volume without a source (a lone `target`), which neither the
	// docker CLI nor compose produce, it is left as it is
	explicit := map[string]bool{}
	for _, bind := range hostConfig.Binds {
		explicit[path.Clean(bindTarget(bind))] = true
	}
	for _, m := range hostConfig.Mounts {
		if m.Type != mount.TypeVolume || len(m.Source) > 0 {
			explicit[path.Clean(m.Target)] = true
		}
	}

	// the previous container's anonymous volumes to mount, by path
	inherited := map[string]container.MountPoint{}
	var targets []string
	for _, m := range c.Raw.Mounts {
		target := path.Clean(m.Destination)
		if m.Type == mount.TypeVolume && len(m.Name) > 0 && anonymous[target] && !explicit[target] {
			inherited[target] = m
			targets = append(targets, target)
		}
	}
	if len(inherited) == 0 {
		return &hostConfig
	}

	mounts := make([]mount.Mount, 0, len(hostConfig.Mounts)+len(inherited))
	for _, m := range hostConfig.Mounts {
		target := path.Clean(m.Target)
		if previous, ok := inherited[target]; ok && m.Type == mount.TypeVolume && len(m.Source) == 0 {
			// the user's mount with its options, now of the previous volume
			m.Source = previous.Name
			delete(inherited, target)
		}
		mounts = append(mounts, m)
	}
	for _, target := range targets {
		if previous, ok := inherited[target]; ok {
			mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: previous.Name, Target: target, ReadOnly: !previous.RW})
		}
	}

	hostConfig.Mounts = mounts
	return &hostConfig
}

// the container path of a bind in the `source:target[:options]` or `target` form
func bindTarget(bind string) string {
	parts := strings.Split(bind, ":")
	if len(parts) > 1 {
		return parts[1]
	}
	return parts[0]
}

// Network endpoints to connect the container's replacement to, keyed by network name.
//
// Inspecting a container returns its endpoints including operational data assigned
// by the daemon (IDs, assigned addresses, DNS names), which is dropped so the
// replacement gets its own. What the user can configure is kept: static addresses
// (IPAMConfig), aliases, links, driver options, gateway priority and the MAC
// address, which the daemon cannot tell apart from a generated one but which is
// free again once the previous container is stopped.
func (c *Container) EndpointsConfig() map[string]*network.EndpointSettings {
	if c.Raw.NetworkSettings == nil {
		return nil
	}

	endpoints := make(map[string]*network.EndpointSettings, len(c.Raw.NetworkSettings.Networks))
	for name, endpoint := range c.Raw.NetworkSettings.Networks {
		if endpoint == nil {
			endpoints[name] = nil
			continue
		}

		// older daemons list the container's short ID among the aliases
		aliases := slices.DeleteFunc(slices.Clone(endpoint.Aliases), func(alias string) bool {
			return len(c.ID) >= 12 && alias == utils.ShortId(c.ID)
		})

		endpoints[name] = &network.EndpointSettings{
			IPAMConfig: endpoint.IPAMConfig,
			Links:      endpoint.Links,
			Aliases:    aliases,
			MacAddress: endpoint.MacAddress,
			DriverOpts: endpoint.DriverOpts,
			GwPriority: endpoint.GwPriority,
		}
	}
	return endpoints
}
