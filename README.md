# heos-helper

A small HTTP service in front of a Denon [HEOS](https://www.denon.com/heos)
system. It keeps one connection to the HEOS CLI (TCP port 1255), tracks the
players' state from change events, and adds two behaviours HEOS does not have:

- **One-touch:** muting a player toggles play/pause instead, and unmutes it
  again. Useful on speakers whose only button is mute.
- **Sleep timer:** a playing player with no user action (volume or play state
  change) for the configured minutes fades out over ten seconds and pauses.

It runs on k3s at `heos-helper.home.arpa:8000` (`192.168.150.202`). Home
Assistant uses it for the "Kitchen Radio" REST switch.

## HTTP API

| Method | Path | Returns |
| --- | --- | --- |
| `GET` | `/api/player` | All players with state, volume, now playing and config |
| `GET` | `/api/player/{pid}` | One player |
| `GET` | `/api/player/{pid}/play_state` | `{"state": "play"}`, or `404` for an unknown player |
| `POST` | `/api/player/{pid}/play_state` | Body `{"state": "play"}` (`play`, `pause`, `stop`); waits up to five seconds for the player to report the new state and returns it |
| `GET` | `/api/source` | The HEOS music sources |

## Configuration

`HEOS_URI` is a comma-separated list of speaker addresses. The first one that
accepts the connection is used for the whole HEOS system:

```sh
HEOS_URI=192.168.151.178:1255,192.168.151.123:1255
```

`config.yaml` is read from the working directory and baked into the image.
Players are keyed by their HEOS `pid` (see `/api/player`):

```yaml
player:
  -2140325193:
    sleep_timer: 60          # minutes; 0 or missing disables it
  1588102935:
    disable_onetouch: true   # mute stays mute
```

When the connection to the speaker drops, the process exits and Kubernetes
restarts it, which reconnects. Only one instance may run at a time, since two
would both react to the same mute.

## Build and deploy

```sh
make deploy   # build, push registry.int.ebner.dev/heos-helper:<commit>-amd64, apply k8s/, wait for rollout
make status
make logs
```

`make deploy` refuses to build from uncommitted changes, because the image tag
is the commit. The manifest in `k8s/heos-helper.yaml` holds a `:latest`
placeholder that `make deploy` replaces before applying.

Locally, `./run` builds and starts the helper against the home speakers. It
connects to the real HEOS system, so one-touch and the sleep timer act on the
speakers while it runs alongside the cluster instance.
