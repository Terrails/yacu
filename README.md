# yacu — Yet Another Container Updater

### **WARNING**: Still in really early development with not enough testing done. Recommended only for non-critical systems.

A simple program written in Go and available in the form of a docker container capable of updating docker containers based on the age of the latest image.

Put simply. A chosen container will only be updated if its remote image is older than a set amount of days in order to ensure somewhat stable releases while giving the ability of automatic updates.

## Images
Images for `linux/amd64` and `linux/arm64` are published to `ghcr.io/terrails/yacu`:

* `latest` — built from the `master` branch
* `stable` — the latest release, also published under its version (e.g. `1.2.3`, `1.2` and `1`)

yacu needs the Docker socket to manage containers, and keeps its config, database and logs in `/data`. With compose:

```
services:
  yacu:
    image: ghcr.io/terrails/yacu:stable
    restart: unless-stopped
    # time to finish an update in progress when stopped, see Stopping
    stop_grace_period: 2m
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./yacu:/data
```

Only containers with the `yacu.enable=true` label are updated, unless `scanner.scan_all` is enabled (see Labels).

## Configuration
A config file is optional but highly recommended.  

YACU searches for a `yacu.yaml` config file in current working directory or uses a path passed via `--config` command line parameter.

In case of the docker container, `yacu.yaml` should be mounted in `/data` path of the container

To check for updates and apply them just once, e.g. from your own scheduler or while trying out a configuration, run yacu with `--once`. It exits when done, with exit code `1` if a container could not be checked or updated.

File examples can be viewed in `examples/config` folder in this repository.

Configuration is applied in this order: built-in defaults, the YAML file, then
environment variables. An environment variable overrides the same setting in
the file. Variables that are not set leave the file or default value unchanged.
Environment variables are parsed as integers, booleans, or logging levels as
appropriate; invalid values prevent startup. `registries` and `webhooks` are
currently configured through YAML only.

Available environment variables:

```
YACU_DATABASE_PATH
YACU_LOGGING_CONSOLE_LEVEL
YACU_LOGGING_FILE_DIRECTORY
YACU_LOGGING_FILE_LEVEL
YACU_SCANNER_INTERVAL
YACU_SCANNER_IMAGE_AGE
YACU_SCANNER_CHECK_INTERVAL
YACU_SCANNER_RUN_ON_START
YACU_SCANNER_SCAN_ALL
YACU_SCANNER_SCAN_STOPPED
YACU_SCANNER_FAIL_ON_ERROR
YACU_UPDATER_STOP_TIMEOUT
YACU_UPDATER_REMOVE_VOLUMES
YACU_UPDATER_REMOVE_IMAGES
```

For example, `YACU_SCANNER_IMAGE_AGE=14` overrides `scanner.image_age: 7`.

---
### Database
`path` — path to the sqlite database storing what the registry was last found to have for each image (default `data.db`)

```
database:
  path: data.db
```

### Logging
`console` — console logging configuration  
* `level` — logging level [`debug`, `info`, `warn`, `error`, `fatal`] (default `info`)

`file` — file logging configuration
* `directory` —  directory containing up to four 10 MB log files, leave empty if no file logging is desired (default `logs`)
* `level` — logging level [`debug`, `info`, `warn`, `error`, `fatal`] (default `debug`)

```
logging:
  console:
    level: info
  file:
    directory:  logs
    level:      debug
```

### Scanner
`interval` — an interval using cron format (default `@weekly`)  
`image_age` — how old an image should be in days before pulling and updating container (default `7`)  
`check_interval` — hours during which the last registry check of an image is relied on instead of querying the registry again, `0` queries it on every run (default `24`). Keeps frequent runs within registry rate limits, e.g. Docker Hub counts each check as a pull. An update found is always confirmed with the registry first  
`run_on_start` — also check for updates right when yacu starts instead of only on `interval` (default `false`)  
`scan_all` — scan all containers on device unless explicitly disabled using `yacu.enable` label (default `false`)  
`scan_stopped` — scan an eligible container even if it is not running (default `false`)  
`fail_on_error` — skip applying any updates in a run where checking a container failed, instead of updating the containers that were checked successfully (default `false`)

```
scanner:
  interval:       "@weekly"
  image_age:      7
  check_interval: 24
  run_on_start:   false
  scan_all:       false
  scan_stopped:   false
  fail_on_error:  false
```

### Updater
`stop_timeout` — amount of time in seconds to wait on a container to stop before forcefully killing (default `30`)  
`remove_volumes` — remove the previous container's anonymous volumes that its replacement no longer uses, e.g. for a `VOLUME` the new image dropped (default `false`). Anonymous volumes are otherwise carried over to the replacement like docker compose does, so their data survives the update  
`remove_images` — remove previous image if it is unused after an update (default `false`)

```
updater:
  stop_timeout:     30
  remove_volumes:   false
  remove_images:    false
```

### Registry authentication
An array of registry authentication data, with each containing the following:  

`domain` — registry domain that needs authentication, e.g. `ghcr.io`. Docker Hub can be given as `docker.io`, `index.docker.io`, `registry-1.docker.io` or `https://index.docker.io/v1/`  
`username` — username for auth  
`password` — password (or access token) for the above username on registry  
`password_file` — file to read the password from instead, e.g. a docker secret like `/run/secrets/ghcr_token`  
`password_env` — environment variable to read the password from instead  
`insecure` — authenticate insecurely in case of local registries not using HTTPS (default `false`). Pulls are done by the Docker daemon, which needs the registry in its own `insecure-registries` as well

Only one of `password`, `password_file` and `password_env` can be given per registry.

```
registries:
  - domain:     docker.io
    username:   123
    password:   123
  - domain:         ghcr.io
    username:       owner
    password_file:  /run/secrets/ghcr_token
  - domain:     custom_registry.tld
    username:   321
    password_env: CUSTOM_REGISTRY_PASSWORD
    insecure:   true
```

Registries without credentials here use the ones stored by `docker login`, read from the Docker config file. In the container that is `/root/.docker/config.json`, or `config.json` in the directory set in `DOCKER_CONFIG`, e.g. by mounting the host's `~/.docker/config.json` read-only. Credentials kept by a credential helper (`credsStore`/`credHelpers` in that file) need the helper's `docker-credential-*` program, which the yacu image does not include.

### Webhooks
A way to send notifications on each successful or failed update  
**NOTE: only discord webhooks are currently implemented**

`url` — webhook url  
`kind` — type of data to send (default for all `true`)
* `errors` — errors that occur during updates
* `image_success` — successful image pull
* `container_success` — successful container recreation with new image
---
Extra data depending on webhook type

#### Discord
`author` — author info, not required
* `name` — author name
* `url` — hyperlink when clicking the author name, e.g. server webui url
* `icon_url` — custom author image url

```
webhooks:
  discord:
    url: webhook_url
    author:
      name:     server_name
      url:      webui_url
      icon_url: author_icon_url
    kind:
      errors:             true
      image_success:      true
      container_success:  true
```

Notifications of an updated container link to its web UI when it has Unraid's `net.unraid.docker.webui` label set to a full URL. Unraid's templates like `http://[IP]:[PORT:8080]/` are left out, as Discord rejects them.

## Containers depending on others
When a container is updated, the running containers that depend on it through compose's `depends_on` are stopped first and started again once it is updated, following their condition:

* `service_started` — right after it starts
* `service_healthy` — once it is healthy, or right away without a healthcheck. A container still not healthy after 5 minutes, or becoming unhealthy, is left stopped with a warning
* `service_completed_successfully` — once it exits, with a warning if it failed. A container still waiting after 5 minutes is left stopped with a warning

Dependencies declared with `restart: false` are not stopped. The containers are found through the `com.docker.compose.depends_on` label, which compose sets. Outside of compose it can be set by hand, with container names in place of services, e.g. `db:service_started` (a missing condition means `service_healthy`).

## Containers sharing a network
Containers joining the network (or IPC/PID) namespace of an updated container, e.g. apps behind a VPN container with `network_mode: "service:gluetun"` in compose or `--network container:gluetun`, are stopped with it and afterwards join its replacement. Compose references the container by ID, so those containers are recreated from the image they run. If their image tag meanwhile refers to a different image, they are left stopped with a warning instead, so that they are not updated along with it; recreate them yourself, e.g. with `docker compose up -d`.

Containers that share only the IPC or PID namespace are found through compose's `depends_on`, which compose adds for them. Those created with `docker run --ipc`/`--pid container:<name>` are not handled.

## Labels

`yacu.enable` — allow/disallow yacu from scanning the container, bypasses `scanner.scan_all` [`true`, `false`]  
`yacu.image_age` — minimum time in days that an image should be released for before pulling and recreating the container, used to bypass `scanner.image_age`  
`yacu.stop_timeout` — amount of time in seconds to wait for a container to stop before forcefully killing it, used to bypass `updater.stop_timeout` 

## Stopping
When stopped (`SIGTERM`/`SIGINT`), yacu stops scanning and pulling right away, but a container update that is already in progress is always completed or rolled back first. Remaining updates are skipped until the next run. Sending the signal a second time exits immediately.

Containers that depend on the updated one (compose `depends_on` with `service_healthy` or `service_completed_successfully`) and are still waiting for that condition at shutdown are left stopped rather than started early, and a warning is sent. Start them manually once the dependency is ready.

Docker only waits 10 seconds before killing a container, which may not be enough to stop, recreate and start the container being updated. Give yacu more time, e.g. `stop_grace_period: 2m` in compose or `docker run --stop-timeout 120`.
