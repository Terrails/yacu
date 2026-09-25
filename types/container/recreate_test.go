package container

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/terrails/yacu/types/image"
)

var testID = strings.Repeat("ab", 32)

func newRecreateContainer(config *container.Config, hostConfig *container.HostConfig, imageConfig *dockerspec.DockerOCIImageConfig) *Container {
	if hostConfig == nil {
		hostConfig = &container.HostConfig{}
	}
	return &Container{
		ID: testID,
		Raw: &container.InspectResponse{
			ContainerJSONBase: &container.ContainerJSONBase{ID: testID, HostConfig: hostConfig},
			Config:            config,
		},
		Image: &image.ImageData{Raw: &dockerimage.InspectResponse{Config: imageConfig}},
	}
}

func imageConfig(config ocispec.ImageConfig, healthcheck *dockerspec.HealthcheckConfig) *dockerspec.DockerOCIImageConfig {
	return &dockerspec.DockerOCIImageConfig{
		ImageConfig:             config,
		DockerOCIImageConfigExt: dockerspec.DockerOCIImageConfigExt{Healthcheck: healthcheck},
	}
}

func TestCreateConfigRemovesImageDefaults(t *testing.T) {
	img := imageConfig(ocispec.ImageConfig{
		User:         "app",
		WorkingDir:   "/app",
		StopSignal:   "SIGQUIT",
		Env:          []string{"PATH=/usr/bin", "VERSION=1.0", "FOO=image"},
		Labels:       map[string]string{"org.opencontainers.image.version": "1.0", "maintainer": "someone", "tier": "image"},
		ExposedPorts: map[string]struct{}{"80/tcp": {}, "443/tcp": {}},
		Volumes:      map[string]struct{}{"/data": {}},
		Entrypoint:   []string{"/entrypoint.sh"},
		Cmd:          []string{"serve"},
	}, nil)

	// as returned by inspect: the user's settings merged with the image's defaults
	config := &container.Config{
		Hostname:     testID[:12],
		Image:        "test/app:latest",
		User:         "app",
		WorkingDir:   "/app",
		StopSignal:   "SIGQUIT",
		Env:          []string{"FOO=mine", "EXTRA=1", "PATH=/usr/bin", "VERSION=1.0"},
		Labels:       map[string]string{"org.opencontainers.image.version": "1.0", "maintainer": "someone", "tier": "mine", "com.docker.compose.project": "p"},
		ExposedPorts: nat.PortSet{"80/tcp": {}, "443/tcp": {}, "8080/tcp": {}},
		Volumes:      map[string]struct{}{"/data": {}, "/extra": {}},
		Entrypoint:   []string{"/entrypoint.sh"},
		Cmd:          []string{"serve"},
	}
	hostConfig := &container.HostConfig{PortBindings: nat.PortMap{"443/tcp": {{HostPort: "8443"}}}}

	got := newRecreateContainer(config, hostConfig, img).CreateConfig()

	want := &container.Config{
		Image:  "test/app:latest",
		Env:    []string{"FOO=mine", "EXTRA=1"},
		Labels: map[string]string{"tier": "mine", "com.docker.compose.project": "p"},
		// published, so kept even though the image exposes it
		ExposedPorts: nat.PortSet{"443/tcp": {}, "8080/tcp": {}},
		Volumes:      map[string]struct{}{"/extra": {}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
}

func TestCreateConfigDoesNotModifyInspectData(t *testing.T) {
	img := imageConfig(ocispec.ImageConfig{
		Env:          []string{"VERSION=1.0"},
		Labels:       map[string]string{"version": "1.0"},
		ExposedPorts: map[string]struct{}{"80/tcp": {}},
		Volumes:      map[string]struct{}{"/data": {}},
	}, nil)
	config := &container.Config{
		Hostname:     testID[:12],
		Env:          []string{"KEEP=1", "VERSION=1.0"},
		Labels:       map[string]string{"version": "1.0"},
		ExposedPorts: nat.PortSet{"80/tcp": {}},
		Volumes:      map[string]struct{}{"/data": {}},
	}

	newRecreateContainer(config, nil, img).CreateConfig()

	if config.Hostname != testID[:12] || !slices.Equal(config.Env, []string{"KEEP=1", "VERSION=1.0"}) ||
		len(config.Labels) != 1 || len(config.ExposedPorts) != 1 || len(config.Volumes) != 1 {
		t.Fatalf("inspect data was modified: %+v", config)
	}
}

func TestCreateConfigCmdAndEntrypoint(t *testing.T) {
	img := imageConfig(ocispec.ImageConfig{Entrypoint: []string{"/entrypoint.sh"}, Cmd: []string{"serve"}}, nil)

	tests := []struct {
		name                    string
		entrypoint, cmd         []string
		wantEntrypoint, wantCmd []string
	}{
		{"both inherited", []string{"/entrypoint.sh"}, []string{"serve"}, nil, nil},
		{"custom cmd", []string{"/entrypoint.sh"}, []string{"migrate"}, nil, []string{"migrate"}},
		// the image's Cmd is not inherited with a custom Entrypoint, so it was set by the user
		{"custom entrypoint", []string{"/bin/sh", "-c"}, []string{"serve"}, []string{"/bin/sh", "-c"}, []string{"serve"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := &container.Config{Entrypoint: test.entrypoint, Cmd: test.cmd}
			got := newRecreateContainer(config, nil, img).CreateConfig()

			if !slices.Equal(got.Entrypoint, test.wantEntrypoint) || !slices.Equal(got.Cmd, test.wantCmd) {
				t.Fatalf("got entrypoint %v cmd %v, want entrypoint %v cmd %v", got.Entrypoint, got.Cmd, test.wantEntrypoint, test.wantCmd)
			}
		})
	}
}

func TestCreateConfigHealthcheck(t *testing.T) {
	imageHealthcheck := &dockerspec.HealthcheckConfig{Test: []string{"CMD", "healthcheck"}, Interval: 30 * time.Second, Retries: 3}
	img := imageConfig(ocispec.ImageConfig{}, imageHealthcheck)

	tests := []struct {
		name        string
		healthcheck *container.HealthConfig
		want        *container.HealthConfig
	}{
		{"inherited", &container.HealthConfig{Test: []string{"CMD", "healthcheck"}, Interval: 30 * time.Second, Retries: 3}, nil},
		{"custom interval", &container.HealthConfig{Test: []string{"CMD", "healthcheck"}, Interval: 10 * time.Second, Retries: 3}, &container.HealthConfig{Interval: 10 * time.Second}},
		{"disabled", &container.HealthConfig{Test: []string{"NONE"}, Interval: 30 * time.Second, Retries: 3}, &container.HealthConfig{Test: []string{"NONE"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := newRecreateContainer(&container.Config{Healthcheck: test.healthcheck}, nil, img).CreateConfig()

			if !reflect.DeepEqual(got.Healthcheck, test.want) {
				t.Fatalf("got %+v, want %+v", got.Healthcheck, test.want)
			}
		})
	}
}

func TestCreateConfigKeepsCustomHostname(t *testing.T) {
	config := &container.Config{Hostname: "media-server"}
	if got := newRecreateContainer(config, nil, imageConfig(ocispec.ImageConfig{}, nil)).CreateConfig(); got.Hostname != "media-server" {
		t.Fatalf("hostname is %q", got.Hostname)
	}
}

func TestEndpointsConfigDropsOperationalData(t *testing.T) {
	ipam := &network.EndpointIPAMConfig{IPv4Address: "192.168.1.50"}
	cnt := newRecreateContainer(&container.Config{}, nil, nil)
	cnt.Raw.NetworkSettings = &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
		"br0": {
			IPAMConfig:  ipam,
			Aliases:     []string{testID[:12], "web"},
			MacAddress:  "02:42:c0:a8:01:32",
			DriverOpts:  map[string]string{"opt": "1"},
			GwPriority:  1,
			NetworkID:   "network-id",
			EndpointID:  "endpoint-id",
			Gateway:     "192.168.1.1",
			IPAddress:   "192.168.1.50",
			IPPrefixLen: 24,
			DNSNames:    []string{"web", testID[:12]},
		},
	}}

	got := cnt.EndpointsConfig()

	want := map[string]*network.EndpointSettings{
		"br0": {
			IPAMConfig: ipam,
			Aliases:    []string{"web"},
			MacAddress: "02:42:c0:a8:01:32",
			DriverOpts: map[string]string{"opt": "1"},
			GwPriority: 1,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got["br0"], want["br0"])
	}
	if aliases := cnt.Raw.NetworkSettings.Networks["br0"].Aliases; len(aliases) != 2 {
		t.Fatalf("inspect data was modified: %v", aliases)
	}
}

func TestCreateHostConfigReusesAnonymousVolumes(t *testing.T) {
	oldImage := imageConfig(ocispec.ImageConfig{Volumes: map[string]struct{}{"/var/lib/data": {}, "/dropped": {}}}, nil)
	newImage := imageConfig(ocispec.ImageConfig{Volumes: map[string]struct{}{"/var/lib/data": {}}}, nil)

	userMounts := []mount.Mount{
		{Type: mount.TypeVolume, Target: "/compose-anonymous", VolumeOptions: &mount.VolumeOptions{NoCopy: true}},
		{Type: mount.TypeVolume, Source: "named", Target: "/named"},
		{Type: mount.TypeTmpfs, Target: "/tmp"},
	}
	cnt := newRecreateContainer(
		// "/cache" was declared by the user with `-v /cache`
		&container.Config{Volumes: map[string]struct{}{"/var/lib/data": {}, "/dropped": {}, "/cache": {}}},
		&container.HostConfig{Binds: []string{"/mnt/media:/media:ro", "config:/config"}, Mounts: slices.Clone(userMounts)},
		oldImage,
	)
	cnt.Raw.Mounts = []container.MountPoint{
		{Type: mount.TypeVolume, Name: "anon-data", Destination: "/var/lib/data", RW: true},
		{Type: mount.TypeVolume, Name: "anon-cache", Destination: "/cache/", RW: false},
		{Type: mount.TypeVolume, Name: "anon-compose", Destination: "/compose-anonymous", RW: true},
		// no longer a VOLUME of the new image
		{Type: mount.TypeVolume, Name: "anon-dropped", Destination: "/dropped", RW: true},
		{Type: mount.TypeVolume, Name: "config", Destination: "/config", RW: true},
		{Type: mount.TypeVolume, Name: "named", Destination: "/named", RW: true},
		{Type: mount.TypeBind, Source: "/mnt/media", Destination: "/media"},
		{Type: mount.TypeTmpfs, Destination: "/tmp", RW: true},
	}

	got := cnt.CreateHostConfig(newImage)

	want := []mount.Mount{
		{Type: mount.TypeVolume, Source: "anon-compose", Target: "/compose-anonymous", VolumeOptions: &mount.VolumeOptions{NoCopy: true}},
		{Type: mount.TypeVolume, Source: "named", Target: "/named"},
		{Type: mount.TypeTmpfs, Target: "/tmp"},
		{Type: mount.TypeVolume, Source: "anon-data", Target: "/var/lib/data"},
		{Type: mount.TypeVolume, Source: "anon-cache", Target: "/cache", ReadOnly: true},
	}
	if !reflect.DeepEqual(got.Mounts, want) {
		t.Fatalf("got mounts\n%+v\nwant\n%+v", got.Mounts, want)
	}
	if !slices.Equal(got.Binds, []string{"/mnt/media:/media:ro", "config:/config"}) {
		t.Fatalf("binds changed: %v", got.Binds)
	}
	if !reflect.DeepEqual(cnt.Raw.HostConfig.Mounts, userMounts) {
		t.Fatalf("inspect data was modified: %+v", cnt.Raw.HostConfig.Mounts)
	}
}

func TestCreateHostConfigWithoutAnonymousVolumes(t *testing.T) {
	hostConfig := &container.HostConfig{Binds: []string{"data:/data"}}
	cnt := newRecreateContainer(&container.Config{}, hostConfig, imageConfig(ocispec.ImageConfig{}, nil))
	cnt.Raw.Mounts = []container.MountPoint{{Type: mount.TypeVolume, Name: "data", Destination: "/data", RW: true}}

	got := cnt.CreateHostConfig(imageConfig(ocispec.ImageConfig{Volumes: map[string]struct{}{"/data": {}}}, nil))

	if len(got.Mounts) != 0 || !slices.Equal(got.Binds, hostConfig.Binds) {
		t.Fatalf("expected the host config unchanged, got binds %v mounts %+v", got.Binds, got.Mounts)
	}
}

func TestCreateConfigForContainerNetworkMode(t *testing.T) {
	// given the parent's host name when started, and the image's exposed port
	config := &container.Config{Hostname: "parent-host", Domainname: "parent.lan", ExposedPorts: nat.PortSet{"8080/tcp": {}}}
	hostConfig := &container.HostConfig{NetworkMode: "container:vpn"}

	got := newRecreateContainer(config, hostConfig, imageConfig(ocispec.ImageConfig{}, nil)).CreateConfig()

	if got.Hostname != "" || got.Domainname != "" || got.ExposedPorts != nil {
		t.Fatalf("the daemon rejects these with container network mode: %+v", got)
	}
}
