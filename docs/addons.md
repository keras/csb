# Addons

← [README](../README.md)

Addons are how you customize the image beyond editing the Dockerfile
directly — installing tools, enabling a desktop environment, or adding
nested-container support. Each addon is a self-contained directory of
files copied into the build context and run once as part of the image
build.

## Using addons

Enable addons by name in `config.yaml` or on the CLI:

```yaml
addons: [mise, sudo, packages git nano]
```

```sh
csb --addon mise --addon sudo --addon "packages git nano"
```

Some addons take arguments, whitespace-split (no quoting support) and
passed straight to the addon's install script — `--addon "packages git
nano"` runs `packages`' `install.sh git nano`. Duplicate addon names are
deduped, keeping the first occurrence; enabled addons are installed in
alphabetical order regardless of the order you list them, so the image
hash (and therefore build caching) doesn't depend on list order.

Enabling an addon changes the [image content hash](configuration.md#images-and-rebuilds),
so the next `csb run` rebuilds automatically.

### Shipped addons

| Addon | What it does |
|---|---|
| `mise` | Installs [mise](https://mise.jdx.dev), activated in every shell. Tool installs land under `~/.local/share/mise`, so they persist via the home volume. Enabled by default. |
| `sudo` | Passwordless root inside the sandbox (`sandbox ALL=(ALL) NOPASSWD:ALL`). Packages installed this way land on the container's root filesystem and are **not** persistent — prefer `mise` or `packages` for anything you want to keep. Enabled by default. |
| `packages` | `apt-get install`s its arguments at build time: `--addon "packages git nano"`. No-op with zero args. |
| `gui` | Lightweight VNC desktop (Xvnc + openbox) over noVNC. `gui-start` / `gui-stop`. Listens on port 6080 inside the sandbox — publish it to reach it from the host (`--publish 6080`); `gui-start` prints the resolved URL via `$CSB_PUBLISH_6080`. Resolution via `CSB_GUI_GEOMETRY` (default `1280x800x24`). Requires `--shm-size=512m`, added automatically. |
| `selkies` | Full desktop over WebRTC (Selkies-GStreamer) with synchronized audio — Xorg dummy + openbox + PulseAudio, resolution follows the browser window up to 4K. `selkies-start` / `selkies-stop`, port 8080, publish and read the URL the same way as `gui`. A bundled coturn TURN relay handles WebRTC media so it works regardless of network topology; no public STUN/TURN needed. TURN creds via `CSB_SELKIES_TURN_USER`/`CSB_SELKIES_TURN_PASSWORD` (not a security boundary — the localhost-only publish is). Initial resolution via `CSB_SELKIES_GEOMETRY`. Requires `--shm-size=1g` and a fixed `-p 127.0.0.1:3478:3478/tcp` for the TURN relay, added automatically. |
| `podman` | Rootful nested Podman. The sandbox shell is non-root, so `podman` is a sudo-elevating wrapper around the real `/usr/bin/podman`; images and layers persist under the home volume. Because rootful nested containers need cgroup delegation and a private `/proc` that an unprivileged container can't provide, this addon runs the **sandbox container itself `--privileged`** — see [Scope of isolation](isolation.md). With the `systemd` addon also enabled, the packaged Podman API comes up automatically on `/run/podman/podman.sock`; add `docker-sock` (`--addon "podman docker-sock"`) to also publish it at `/var/run/docker.sock` for Docker-API clients that expect it there. |
| `podman-rootless` | Rootless nested Podman — runs as the sandbox user with slirp4netns networking and uid mapping. More isolated than `podman` (no `--privileged`), at the cost of some rootless limitations (some images/mounts/ports). Requires `--device /dev/fuse`, `--device /dev/net/tun`, unconfined seccomp/apparmor, and `SYS_ADMIN`/`NET_ADMIN` capabilities, all added automatically. |
| `systemd` | Boots systemd as PID 1 instead of the default single-process launch. The interactive shell becomes a real logind session (entered via `login(1)`), so `systemctl --user`, `loginctl`, and `journalctl` behave normally. Lets other addons register real supervised services instead of hand-backgrounding daemons from an entrypoint hook. Needs a shared cgroup namespace, a writable cgroup mount, and (under Podman specifically) a private PID namespace — added automatically; some hosts may still require `--privileged`. |

Addons that request extra container privileges (`podman`, `podman-rootless`,
`systemd`) are covered in more detail in [Scope of isolation](isolation.md).

Local-only addons — ones you write yourself and don't want shipped by the
binary — go directly under `~/.config/csb/addons/<name>/`; `csb config
status` reports them as `local-only` rather than flagging them as missing.

## Writing addons

An addon is a directory `cmd/csb/files/addons/<name>/` (or, for a
local-only addon, `~/.config/csb/addons/<name>/`) containing:

- **`install.sh`** (required, executable) — runs once during the image
  build, `cd`'d into the addon's own directory so it can reference sibling
  files by relative path (`install -D -m 0644 policy.json /etc/...`). All
  enabled addons share a single `apt-get update` before any `install.sh`
  runs, and errors are wrapped to name the failing addon rather than
  surfacing an opaque script failure.
- Arbitrary bundled resource files — config files, helper scripts,
  systemd units — installed by `install.sh` however it likes.
- **`test.sh`** (optional, dev-only) — never shipped, never copied into
  the image, and excluded from the image content hash. `go test -tags
  addons ./internal/addons/...` runs each addon's `test.sh` inside a
  container built with only that addon enabled.
- **`help`** (optional) — a plain-text doc your `install.sh` copies to
  `/etc/csb/help.d/<name>` (e.g. `install -D -m 0644 help
  /etc/csb/help.d/<name>`); csb doesn't place it for you. Once there, it's
  surfaced by `csb-help` inside the sandbox alongside the built-in topics.

### `# csb:run-arg` directives

Comment lines in the *leading comment block* of `install.sh` (scanning
stops at the first non-comment line) add extra arguments to the
`docker/podman run` command used to start the sandbox — capabilities,
devices, namespace flags, published ports, anything that has to be set at
container-start time rather than baked into the image:

```sh
#!/bin/bash
# csb:run-arg --shm-size=512m
# csb:run-arg[runtime=podman] --pid=private
# csb:run-arg[runtime=docker|podman] --something
# csb:run-arg[runtime=podman,arch=arm64] --something-else
# csb:run-arg[runtime!=docker] --baz
```

The optional bracketed condition is matched against the current *facts*
(`runtime`, `arch`) and can combine terms with `,` (AND) inside one
bracket, and alternatives with `|` inside one term (`key=a|b` matches
either). `!=` negates. Unconditional directives always apply. Duplicate
resulting tokens across addons are deduped.

### Runtime hooks (`entrypoint.d`)

Drop an executable script into `/etc/csb/entrypoint.d/*.sh` (installed by
`install.sh`) to run code at container start — exporting env vars, setting
up mounts, anything that needs to happen per-boot rather than per-build.
Every `*.sh` there is sourced (not executed) by `entrypoint.sh`, in
filename order, before the main workload launches.

### Providing an init system

By default, `entrypoint.sh`'s `csb_launch` function just execs the user's
command directly (the single-process sandbox). An addon that provides an
init system — like `systemd` — overrides `csb_launch` from an
`entrypoint.d` hook to exec that init as PID 1 instead, and must call
`csb_register_launcher <name>` when it does. Two addons both claiming the
launcher role is a hard failure at container start (`addons 'a' and 'b'
both provide an init system; enable only one`), so only one boot-mode addon
can be enabled at a time.

`_run_user exec|noexec` is available to launcher hooks (and exported for
child processes) for dropping from root to the sandbox uid via `gosu`,
matching what the default `csb_launch` does.

### Conventions

- Bundle any static resource file next to `install.sh` rather than
  heredoc-generating it in the script, unless it's genuinely tiny (like
  `gui`'s generated `gui-start`/`gui-stop`) — it keeps `install.sh`
  readable and lets `install -D` handle directory creation.
  ("Genuinely tiny" is not a hard rule — several shipped addons do
  generate small scripts inline where a separate file would be more
  ceremony than content.)
- Files are content-hashed by relative path plus contents, so renaming a
  bundled file changes the image even if nothing else does — keep names
  stable once shipped.
- File modes are **not** carried through from the repo for shipped
  addons: the config-dir seeder normalizes every bundled file to `0644`
  except `install.sh` itself (always `0755`), regardless of what's
  executable on disk. If `install.sh` needs to place an executable helper
  (like `selkies-start`), set the mode explicitly: `install -D -m 0755
  helper-script /usr/local/bin/helper-script`. (Local-only addons authored
  directly under `~/.config/csb/addons/` skip the seeder and do keep
  their on-disk mode.)
