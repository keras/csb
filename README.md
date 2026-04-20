# csb — Container Sandbox

Run commands in an isolated container with a persistent home.

## Features

- Auto-detects Docker or Podman
- Persistent home volume (`csb-home`) — tool state and caches survives across runs
- Host config overlay: files in `~/.config/csb/home/` are symlinked into the container home
- `csb-persist` — promote a home directory entry to the host config overlay without restarting
- Optional addon system (mise by default) for installing various tools
- Optional nested Podman support
- Image cached by content hash — rebuilds only when config changes

## Install

```sh
uv tool install git+https://github.com/keras/csb
```

Or run directly without installing:

```sh
uvx --from git+https://github.com/keras/csb csb [args]
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
# base_image: debian:stable-slim
# nested_podman: false
# addons: [mise]
# home_volume: csb-home
# mount:
#   - ~/.ssh:~/.ssh
```

Run `csb --help` for the full list of flags and their defaults.

**`~/.config/csb/home/`** — files symlinked into the container home on every run. Drop `.gitconfig`, `.ssh/`, `.config/mise/`, `.claude/` etc. here to have them available in every container.

The directory is bind-mounted to `/mnt/csb-home` inside the container. At startup the entrypoint symlinks each entry into `$HOME`. Anything written to the container home that is _not_ backed by an entry here is ephemeral — it survives across runs via the named volume (`csb-home`) but is not reflected on the host.

Use `--config-dir PATH` (or `CSB_CONFIG_DIR`) to point csb at a different config directory — useful for isolating separate tool profiles.

### csb-persist

To promote a home directory entry to the host config overlay from inside a running container, use `csb-persist`:

```sh
csb-persist ~/.gitconfig   # move to /mnt/csb-home and symlink back
csb-persist ~/.claude      # works for directories too
```

This moves the path into `/mnt/csb-home` (which is `~/.config/csb/home/` on the host) and replaces it with a symlink. The change is visible immediately on the host and will be picked up as a symlink on the next container start.


## Scope of isolation

csb is an isolation tool, not a hard security boundary. Its goal is to contain accidental damage from scripts, build tools, and AI agents run inside — a misbehaving script should be able to trash the container without affecting the host. Classic cases this catches:

- `rm -rf "/$path"` where `$path` is unset or empty
- `find / -name ... -delete` with a typo that widens the match
- An agent that "cleans up temp files" but resolves the wrong parent
- A package postinstall hook that rewrites `~/.gitconfig`, `~/.ssh/config`, or shell rc files
- A `git clean -fdx` run from the wrong directory

When any of these happen inside csb, the container and what's mounted there (the workspace dir and home volume) is what gets wiped; the host stays intact. Reset with `csb --reset-home` and continue.

csb is **not** a hardened boundary against deliberately malicious code. When `nested_podman: true`, csb enables the following to support rootless Podman inside the container:

| Flag | Why |
|------|-----|
| `--cap-add SYS_ADMIN` | For `newuidmap`/`newgidmap` to write UID maps in the parent user namespace, and `mount --make-rshared` for nested mount propagation |
| `--cap-add NET_ADMIN` | Writing to `/proc/sys/net/*` in network namespaces Podman creates for inner containers |
| `--security-opt seccomp=unconfined` | Allows `clone(CLONE_NEWUSER)` and related namespace syscalls |
| `--security-opt apparmor=unconfined` | Docker's default AppArmor profile blocks `mount(2)` even with `SYS_ADMIN` |
| `--device /dev/fuse` | fuse-overlayfs storage driver for nested containers |
| `--device /dev/net/tun` | slirp4netns user-mode networking for nested containers |

With that combination, a kernel vulnerability in the exposed syscall surface is reachable from inside the container. Do not run untrusted code here — if you need tighter isolation, use a different tool.

## Environment variables

| Variable | Description |
|----------|-------------|
| `CSB_IMAGE` | Override the image name/tag |
| `CSB_RUNTIME` | Override runtime (`auto`, `docker`, `podman`) |
| `CSB_BASE_IMAGE` | Override base image |
| `CSB_NESTED_PODMAN` | Set to `0` to disable nested Podman |
| `CSB_HOME_VOLUME` | Override home volume name (overrides `home_volume:` in config.yaml, default: `csb-home`) |
| `CSB_CONFIG_DIR` | Override config directory path (default: `~/.config/csb`) |
| `CSB_ENV_FORWARD` | Space-separated list of host env var names to forward into the container |

## Addons / mise

The default addon is mise. Install tools interactively — changes persist to the home volume:

```sh
csb                            # enter the sandbox
mise use -g node@lts           # now inside
mise use -g python claude
```

Or declare them upfront in `~/.config/csb/home/.config/mise/conf.d/tools.toml`:

```toml
[tools]
node = "lts"
python = "latest"
claude = "latest"
```
