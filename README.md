# yacu — Yet Another Container Updater

### **WARNING**: Still in really early development with not enough testing done. Recommended only for non-critical systems.

A simple program written in Go and available in the form of a docker container capable of updating docker containers based on the age of the latest image.

Put simply. A chosen container will only be updated if it's remote image is older than a set amount of days in order to ensure somewhat stable releases while giving the ability of automatic updates.

## Configuration
A config file is optional but highly recommended.  

YACU searches for a `yacu.yaml` config file in current working directory or uses a path passed via `--config` command line parameter.

In case of the docker container, `yacu.yaml` should be mounted in `/data` path of the container

File examples can be viewed in `examples/config` folder in this repository.

---
### Database
`path` — path to sqlite database where creation and last check dates for each container are stored (default `data.db`)

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
`scan_all` — scan all containers on device unless explicitly disabled using `yacu.enable` label (default `false`)  
`scan_stopped` — scan an eligible container even if it is not running (default `false`)

```
scanner:
  interval:     "@weekly"
  image_age:    7
  scan_all:     false
  scan_stopped: false
```

### Updater
`stop_timeout` — amount of time in seconds to wait on a container to stop before forcefully killing (default `30`)  
`remove_volumes` — remove volumes when recreating a container (default `false`)  
`remove_images` — remove previous image if it is unused after an update (default `false`)

```
updater:
  stop_timeout:     30
  remove_volumes:   false
  remove_images:    false
```

### Registry authentication
An array of registry authentication data, with each containing the following:  

`domain` — registry domain that needs authentication  
`username` — username for auth  
`password` — password for the above username on registry  
`insecure` — authenticate insecurely in case of local registries not using HTTPS (default `false`)

```
registries:
  - domain:     docker.io
    username:   123
    password:   123
  - domain:     custom_registry.tld
    username:   321
    password:   321
    insecure:   true
```

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
      critical_errors:      true
      container_fail:       true
      container_success:    true
```

## Labels

`yacu.enable` — allow/disallow yacu from scanning the container, bypasses `scanner.scan_all` [`true`, `false`]  
`yacu.image_age` — minimum time in days that an image should be released for before pulling and recreating the container, used to bypass `scanner.image_age`  
`yacu.stop_timeout` — amount of time in seconds to wait for a container to stop before forcefully killing it, used to bypass `updater.stop_timeout` 
