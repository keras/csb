# Networking

← [README](../README.md)

By default the container gets whatever network the runtime gives new
containers (a bridge, typically) with nothing published to the host.

## `--publish`

Publish a container port to the host, Docker-`-p`-style:

```sh
csb --publish 8080                       # dynamic host port, bound to 127.0.0.1
csb --publish 127.0.0.1:5432:5432        # fixed host port
csb --publish 9000/udp                   # protocol suffix
```

Spec format: `[[host_ip:]host_port:]container_port[/tcp|udp|sctp]`. Also
settable via `publish:` in `config.yaml` (a list) or `CSB_PUBLISH`
(space-separated).

### Dynamic host ports

A **bare container port** (no host IP or host port given, e.g. `--publish
8080`) is special-cased: csb allocates an ephemeral host port itself
(binding to `127.0.0.1:0` and immediately releasing it), publishes that
specific port instead of letting the runtime pick, and injects
**`CSB_PUBLISH_<container_port>=<allocated_port>`** into the container's
environment so processes inside can discover the host-side port they were
actually exposed on.

This is what the `gui` and `selkies` addons rely on: `gui-start` /
`selkies-start` read `$CSB_PUBLISH_6080` / `$CSB_PUBLISH_8080` to print the
correct URL, whether you published a fixed port or a dynamic one. A spec
that already contains a host port or host IP passes through unchanged with
no env var injected — only the "figure out a port for me" form needs one.

## `--host-network`

Use the host's network namespace directly instead of the runtime's default
bridge (`--network host`). Mutually exclusive with `--publish` — an image
using host networking already has direct access to every host port, so
publishing is meaningless and csb rejects the combination with an error
rather than silently ignoring one of them.

Also settable via `host_network:` in `config.yaml` or `CSB_HOST_NETWORK`.
