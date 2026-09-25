package updater

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	yacucontainer "github.com/terrails/yacu/internal/container"
)

func composeLabels(project, service, dependsOn string) map[string]string {
	labels := map[string]string{
		yacucontainer.LABEL_PROJECT: project,
		yacucontainer.LABEL_SERVICE: service,
	}
	if len(dependsOn) > 0 {
		labels[yacucontainer.LABEL_DEPENDS_ON] = dependsOn
	}
	return labels
}

// an image exposing a port, which a container joining another's network must not be created with
func addExposingImage(api *fakeDocker, ref string, id byte, created, repoDigest string, tagged bool) {
	if tagged {
		api.addImage(ref, sha(id), created, repoDigest)
	} else {
		api.addUntaggedImage(sha(id), created, repoDigest)
	}
	img := api.images[sha(id)]
	img.Config = &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{ExposedPorts: map[string]struct{}{"8080/tcp": {}}}}
	api.images[sha(id)] = img
}

// setupVPN creates a running "vpn" container with two containers joining its network:
// "qbit" from compose (network_mode: service:vpn) and "web" from `docker run --network container:vpn`
func setupVPN(t *testing.T) (*fakeDocker, map[string]string) {
	api := newFakeDocker()
	api.addImage("test/vpn:latest", sha('a'), oldCreated, "test/vpn@"+sha('1'))
	addExposingImage(api, "test/qbit:latest", 'c', oldCreated, "test/qbit@"+sha('3'), true)
	api.addImage("test/web:latest", sha('e'), oldCreated, "test/web@"+sha('5'))

	vpnID := api.addContainer("vpn", "test/vpn:latest", composeLabels("p", "vpn", ""), true)
	api.containers[vpnID].Config.Hostname = vpnID[:12]

	// compose turned service:vpn into the ID and added an implicit depends_on
	qbitID := api.addContainer("qbit", "test/qbit:latest", composeLabels("p", "qbit", "vpn:service_started:true"), true)
	qbit := api.containers[qbitID]
	qbit.HostConfig.NetworkMode = container.NetworkMode("container:" + vpnID)
	qbit.NetworkSettings.Networks = map[string]*network.EndpointSettings{}
	// what the daemon put there: the parent's host name and the image's exposed port
	qbit.Config.Hostname = vpnID[:12]
	qbit.Config.ExposedPorts = nat.PortSet{"8080/tcp": {}}

	webID := api.addContainer("web", "test/web:latest", nil, true)
	api.containers[webID].HostConfig.NetworkMode = "container:vpn"
	api.containers[webID].NetworkSettings.Networks = map[string]*network.EndpointSettings{}

	return api, map[string]string{"vpn": vpnID, "qbit": qbitID, "web": webID}
}

func TestUpdateContainerMovesNamespaceChildren(t *testing.T) {
	api, ids := setupVPN(t)
	app := newTestApp(t, api)
	cnt := loadContainer(t, app, ids["vpn"])
	api.addImage("test/vpn:latest", sha('b'), newCreated, "test/vpn@"+sha('2'))

	newVPN, warnings, err := app.UpdateContainer(context.Background(), cnt)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	assertRunning(t, api, "vpn", newVPN.ID)

	// referenced by ID, so recreated to join the new container
	qbit := api.byName("qbit")
	if qbit == nil || qbit.ID == ids["qbit"] {
		t.Fatal("qbit was not recreated")
	}
	assertRunning(t, api, "qbit", qbit.ID)
	if want := container.NetworkMode("container:" + newVPN.ID); qbit.HostConfig.NetworkMode != want {
		t.Fatalf("qbit network mode is %s, want %s", qbit.HostConfig.NetworkMode, want)
	}
	if qbit.Image != sha('c') {
		t.Fatalf("qbit was recreated from image %s, want the one it ran %s", qbit.Image, sha('c'))
	}

	// referenced by name, so restarting it was enough
	assertRunning(t, api, "web", ids["web"])
	if api.called("ContainerStop", ids["web"]) != 1 {
		t.Fatal("web was not stopped with the container whose network it joins")
	}

	if len(api.containers) != 3 {
		t.Fatalf("expected the previous vpn and qbit to be removed, have %d containers", len(api.containers))
	}
}

func TestUpdateContainerRestartsNamespaceChildrenOnRollback(t *testing.T) {
	api, ids := setupVPN(t)
	app := newTestApp(t, api)
	cnt := loadContainer(t, app, ids["vpn"])
	api.addImage("test/vpn:latest", sha('b'), newCreated, "test/vpn@"+sha('2'))
	api.fail = failWhen("ContainerCreate", func(name string) bool { return name == "vpn" })

	_, _, err := app.UpdateContainer(context.Background(), cnt)

	assertUpdateError(t, err, "Unable to create container")
	assertRunning(t, api, "vpn", ids["vpn"])
	assertRunning(t, api, "qbit", ids["qbit"])
	assertRunning(t, api, "web", ids["web"])
}

func TestUpdateContainerLeavesChildWithMovedTagStopped(t *testing.T) {
	api, ids := setupVPN(t)
	app := newTestApp(t, api)
	cnt := loadContainer(t, app, ids["vpn"])
	api.addImage("test/vpn:latest", sha('b'), newCreated, "test/vpn@"+sha('2'))
	// e.g. pulled for another container, qbit should not be updated along with vpn
	addExposingImage(api, "test/qbit:latest", 'd', newCreated, "test/qbit@"+sha('4'), true)

	newVPN, warnings, err := app.UpdateContainer(context.Background(), cnt)
	if err != nil {
		t.Fatal(err)
	}
	assertRunning(t, api, "vpn", newVPN.ID)

	qbit := api.byName("qbit")
	if qbit == nil || qbit.ID != ids["qbit"] || qbit.State.Status == container.StateRunning {
		t.Fatal("qbit should be left as it was, stopped")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "qbit") || !strings.Contains(warnings[0], "left stopped") {
		t.Fatalf("expected a warning that qbit was left stopped, got %v", warnings)
	}
}

func TestUpdateContainerJoiningAnotherNetwork(t *testing.T) {
	api, ids := setupVPN(t)
	app := newTestApp(t, api)
	cnt := loadContainer(t, app, ids["qbit"])
	addExposingImage(api, "test/qbit:latest", 'd', newCreated, "test/qbit@"+sha('4'), true)

	newQbit, _, err := app.UpdateContainer(context.Background(), cnt)
	if err != nil {
		t.Fatal(err)
	}

	assertRunning(t, api, "qbit", newQbit.ID)
	if want := container.NetworkMode("container:" + ids["vpn"]); api.byName("qbit").HostConfig.NetworkMode != want {
		t.Fatalf("qbit network mode is %s, want %s", api.byName("qbit").HostConfig.NetworkMode, want)
	}
}

func TestApplyUpdatesUpdatesNamespaceChildBeforeParent(t *testing.T) {
	api, ids := setupVPN(t)
	app := newTestApp(t, api)
	hook := withRecordingHook(app)

	created, _ := time.Parse(time.RFC3339, newCreated)
	for _, ref := range []string{"test/vpn:latest", "test/qbit:latest"} {
		if _, err := app.DB.SaveRemoteImage(ref, "docker.io", created, digest.Digest(sha('9'))); err != nil {
			t.Fatal(err)
		}
	}
	api.addUntaggedImage(sha('b'), newCreated, "test/vpn@"+sha('9'))
	api.pullResult("test/vpn:latest", sha('b'))
	addExposingImage(api, "test/qbit:latest", 'd', newCreated, "test/qbit@"+sha('9'), false)
	api.pullResult("test/qbit:latest", sha('d'))

	// the parent is listed first
	app.ApplyUpdates(context.Background(), yacucontainer.Containers{loadContainer(t, app, ids["vpn"]), loadContainer(t, app, ids["qbit"])})

	if hook.errors != 0 {
		t.Fatalf("%d error notifications sent", hook.errors)
	}
	vpn, qbit := api.byName("vpn"), api.byName("qbit")
	if vpn.Image != sha('b') || qbit.Image != sha('d') {
		t.Fatalf("vpn runs %s and qbit %s, want both updated", vpn.Image, qbit.Image)
	}
	assertRunning(t, api, "qbit", qbit.ID)
	if want := container.NetworkMode("container:" + vpn.ID); qbit.HostConfig.NetworkMode != want {
		t.Fatalf("qbit network mode is %s, want %s", qbit.HostConfig.NetworkMode, want)
	}
}
