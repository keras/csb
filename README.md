# csb — Container Sandbox

Run commands in an isolated container with a persistent home.

## Features

- Auto-detects Docker or Podman
- Persistent home volume (`csb-home`) — tool state and caches survives across runs
- Host config overlay: files in `~/.config/csb/home/` are symlinked into the container home
- `csb-persist` — promote a home directory entry to the host config overlay without restarting
- Optional addon system (mise by default) for installing various tools
- Optional nested Podman support (rootful or rootless)
- Optional host-exec bridge — sandbox can call whitelisted host commands via `csb-host-run`
- Image cached by content hash — rebuilds only when config changes

## Install

```sh
mise use -g github:keras/csb
```

Or download a prebuilt binary from the releases page and place it on your `PATH`.

Or clone and build (requires go):

```sh
make build
cp bin/csb /usr/local/bin
```

## Quick start

```sh
csb                      # open bash in container, CWD mounted
csb --no-workspace       # ephemeral /workspace, no host directory mounted
csb -- python script.py  # run a command directly
```

## Configuration

**`~/.config/csb/config.yaml`** — persisted defaults, created on first run with a commented-out template:

```yaml
# tmux: true
# tty: true          # default: auto-detect from stdin
# addons: [mise, sudo]
# home_volume: csb-home
# mount:
#   - ~/.ssh:~/.ssh
```

Run `csb --help` for the full list of flags and their defaults.

**`~/.config/csb/home/`** — files symlinked into the container home on every run. Drop `.gitconfig`, `.ssh/`, `.config/`, `.claude/` etc. here to have them available in every container.

The directory is bind-mounted to `/mnt/csb-home` inside the container. At startup the entrypoint symlinks each entry into `$HOME`. Anything written to the container home that is _not_ backed by an entry here is ephemeral — it survives across runs via the named volume (`csb-home`) but is not reflected on the host.

Use `--config-dir PATH` (or `CSB_CONFIG_DIR`) to point csb at a different config directory — useful for isolating separate tool profiles.

### Keeping shipped resources in sync

The `Dockerfile` and addon install scripts under `~/.config/csb/` are seeded from the csb
binary on first run and are yours to edit. Because the seeder never overwrites an existing
file, an updated binary may ship newer versions while your config dir keeps using the old
ones. Two subcommands make this visible and fixable:

```bash
csb config status   # list Dockerfile + addons and whether they match the embedded versions
csb config update   # interactively pick which to overwrite with the shipped version
```

### csb-persist

To promote a home directory entry to the host config overlay from inside a running container, use `csb-persist`:

```sh
csb-persist ~/.gitconfig   # move to /mnt/csb-home and symlink back
csb-persist ~/.claude      # works for directories too
```

This moves the path into `/mnt/csb-home` (which is `~/.config/csb/home/` on the host) and replaces it with a symlink. The change is visible immediately on the host and will be picked up as a symlink on the next container start.

## Host-exec bridge

The host-exec bridge lets code running inside the sandbox invoke a allowlisted set of commands on the host, with arguments passed through. This is useful for running commands that need to interact with the host environment or for leveraging host-only resources (e.g. GPU).

```sh
csb --host-exec \
    --host-exec-allow "make run" \
    --host-exec-allow "./cmd **" \
    -- my-agent

csb-host-run make run
csb-host-run ./cmd "done"
echo "hello" | csb-host-run ./cmd ...
```

`csb-host-run` connects over WebSocket to a host-side broker that csb starts before launching the container (csb runs as its own broker via an internal mode). The broker enforces an allowlist and scrubs the environment before spawning any process — env vars injected into the sandbox (e.g. `GIT_SSH_COMMAND`) are not forwarded to the host process.

### Enabling host-exec

Enable per-invocation with `--host-exec` and specify allowed commands with one or more `--host-exec-allow` flags:

```sh
csb --host-exec \
    --host-exec-allow "make run" \
    --host-exec-allow "./cmd **" \
    -- my-agent
```

Or set defaults in `~/.config/csb/config.yaml`:

```yaml
host_exec_enabled: true
host_exec_allow:
  - make run
  - cmd **
```

### Allowlist pattern syntax

Each rule is a string: the command name followed by zero or more argument patterns, separated by spaces.

Examples:

```
open *          # open with exactly one arg (e.g. a URL)
say **          # say with any number of args (including none)
git status      # git status with no extra args
git log **      # git log with any trailing args
```

`csb-host-run` exits **126** if the command is not in the allowlist, **127** if the binary is not found on the host, and propagates the actual exit code otherwise.

### Security properties

- The broker binds only to the container network interface (not all host interfaces) on Linux, limiting exposure to the local container bridge network.
- Access requires a per-session 32-byte random token injected via env var — each `csb` invocation gets a fresh token.
- The host process runs with a scrubbed environment: only `PATH`, `HOME`, `USER`, `LANG`, and `TERM` from the broker's own env are forwarded.
- The command name itself is never wildcarded — only argument positions can use `*`/`**`.
- Host-exec is opt-in; the default configuration does not enable it.

## Scope of isolation

csb is an isolation tool, not a hard security boundary. Its goal is to contain accidental damage from scripts, build tools, and AI agents run inside — a misbehaving script should be able to trash the container without affecting the host. Classic cases this catches:

- `rm -rf "/$path"` where `$path` is unset or empty
- `find / -name ... -delete` with a typo that widens the match
- An agent that "cleans up temp files" but resolves the wrong parent
- A package postinstall hook that rewrites `~/.gitconfig`, `~/.ssh/config`, or shell rc files
- A `git clean -fdx` run from the wrong directory

When any of these happen inside csb, the container and what's mounted there (the workspace dir and home volume) is what gets wiped; the host stays intact. Reset with `csb --reset-home` and continue.

csb is **not** a hardened boundary against deliberately malicious code. The `podman-rootless` addon enables the following to support nested rootless Podman inside the container:

| Flag                                 | Why                                                                                                                                 |
| ------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------- |
| `--cap-add SYS_ADMIN`                | For `newuidmap`/`newgidmap` to write UID maps in the parent user namespace, and `mount --make-rshared` for nested mount propagation |
| `--cap-add NET_ADMIN`                | Writing to `/proc/sys/net/*` in network namespaces Podman creates for inner containers                                              |
| `--security-opt seccomp=unconfined`  | Allows `clone(CLONE_NEWUSER)` and related namespace syscalls                                                                        |
| `--security-opt apparmor=unconfined` | Docker's default AppArmor profile blocks `mount(2)` even with `SYS_ADMIN`                                                           |
| `--device /dev/fuse`                 | fuse-overlayfs storage driver for nested containers                                                                                 |
| `--device /dev/net/tun`              | slirp4netns user-mode networking for nested containers                                                                              |

The `podman` (rootful) addon goes further and runs the container `--privileged`, because rootful nested containers need cgroup delegation, a mountable `/proc`, and native overlay storage that the flags above don't provide.

With either, a kernel vulnerability in the exposed syscall surface is reachable from inside the container. Do not run untrusted code here — if you need tighter isolation, use a different tool.

## Environment variables

| Variable            | Description                                                                              |
| ------------------- | ---------------------------------------------------------------------------------------- |
| `CSB_IMAGE`         | Override the image name/tag                                                              |
| `CSB_RUNTIME`       | Override runtime (`auto`, `docker`, `podman`)                                            |
| `CSB_HOME_VOLUME`   | Override home volume name (overrides `home_volume:` in config.yaml, default: `csb-home`) |
| `CSB_CONFIG_DIR`    | Override config directory path (default: `~/.config/csb`)                                |
| `CSB_ENV_FORWARD`   | Space-separated list of host env var names to forward into the container                 |

## Addons / mise

The default addon is mise. Install tools interactively — changes persist to the home volume:

```sh
csb                            # enter the sandbox
mise use -g node@lts           # now inside
mise use -g python opencode
```

Or declare them upfront in `~/.config/csb/home/.config/mise/conf.d/tools.toml`:

```toml
[tools]
node = "lts"
python = "latest"
opencode = "latest"
```

### Nested Podman

Enable nested containers with one of two addons:

```yaml
addons: [mise, sudo, podman]           # rootful
addons: [mise, sudo, podman-rootless]  # rootless
```

- **`podman`** (rootful) — inner containers run as real root in their own
  namespaces, with no uid mapping. This matches how containers behave on a
  normal host and avoids the subuid/`newuidmap` limitations that make some
  images, mounts, and ports fail under rootless. The sandbox shell is
  non-root, so `podman` is a wrapper that elevates via sudo (the real binary
  is `/usr/bin/podman`). Images persist across runs under the home volume.
  Rootful podman nested in an unprivileged container can't get cgroup v2
  delegation, mount a private `/proc`, or use fuse-overlayfs (its rootfs fails
  to exec on some kernels), so this addon runs the **sandbox container itself
  as `--privileged`** (the `docker:dind` recipe) — a large privilege
  relaxation. Use `podman-rootless` if you don't need rootful behaviour.
- **`podman-rootless`** — `podman` runs as the sandbox user with slirp4netns
  networking and uid mapping. More isolated (no `--privileged`), but some
  features are unsupported.

Both addons pull in extra container privileges (see
[Scope of isolation](#scope-of-isolation)); `podman` additionally runs
`--privileged`.
