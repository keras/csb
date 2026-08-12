# Persistence

← [README](../README.md)

Containers are throwaway — `csb run` always passes `--rm`, so nothing about
the container itself survives past the session. Three separate mechanisms
give you state that *does* survive:

## 1. Workspace bind mount

Your project directory (the workspace) is bind-mounted read-write into the
container, so anything you do there is just editing the host filesystem
directly — no copy-back step, nothing container-specific. It's mounted
under `/workspace`, at a path mirroring its location on the host (e.g.
`~/projects/my-app` on the host lands at `/workspace/projects/my-app` in
the sandbox, which is also the container's working directory) — not at
bare `/workspace`, which is reserved for `--no-workspace` runs that have
no host directory mounted at all.

## 2. The home volume

`/home/sandbox` is backed by a named Docker/Podman volume (`csb-home` by
default; override with `home_volume:` in `config.yaml` or `CSB_HOME_VOLUME`
— see [Configuration](configuration.md)). It gives the sandbox a real,
durable home directory — without exposing your actual host `$HOME` to
whatever runs inside. Tools that assume they can write to `$HOME` (caches,
installed runtimes, shell history, credentials) work normally and keep
their state across runs; that state just lives in runtime-managed storage
rather than on your host filesystem.

A volume rather than a bind mount also matters for speed on macOS and
Windows: there, the container runtime runs inside a VM, and a bind-mounted
host directory crosses that VM boundary (virtiofs / gRPC-FUSE) on every
access — noticeably slower for metadata-heavy work like package caches and
dependency trees. A named volume lives inside the VM's own filesystem and
runs at native speed. On Linux, bind mounts are native to begin with, so
this difference doesn't apply.

This is why sandbox tooling is pointed at `$HOME` rather than the
workspace for this kind of state: the `mise` addon sets
`MISE_DATA_DIR=$HOME/.local/share/mise` and
`MISE_CACHE_DIR=$HOME/.cache/mise`. As a rule of thumb: host-visible
config you want to edit or keep under host version control belongs in the
overlay below; bulk, regenerable state (caches, installed tool versions,
build artifacts) belongs in the home volume.

The volume is created on first use if it doesn't already exist, and is
**not** tied to a specific config directory beyond the label used for
cleanup — reusing the same volume name across different config
directories, or different projects, shares the same home. For an isolated
home per project, set `home_volume:` in that project's workdir config
(`csb config edit workdir`). It survives the container being removed
(`--rm`) and is only deleted via `csb clean`.

Writes to the container's root filesystem *outside* `$HOME` (e.g.
`apt-get install` run interactively, rather than via an addon at build
time) are not persisted anywhere — they vanish the moment the container
exits.

## 3. Host config overlay (`~/.config/csb/home/`)

Files placed under `~/.config/csb/home/` on the host are bind-mounted into
the container at `/mnt/csb-home`, and the entrypoint symlinks each entry
into `$HOME` at container start. This is the mechanism for config you want
under host version control or shared across every csb config profile:
`.gitconfig`, `.ssh/`, `.config/mise/conf.d/tools.toml`, `.claude/`, etc.

Symlinking (rather than bind-mounting the whole directory) means new
entries can be added without restarting the container — see `csb-persist`
below. If a name already exists in the home volume as a real file or
directory (not a symlink to the overlay entry), the entrypoint prints a
warning and leaves it alone rather than overwriting anything.

### `csb-persist`

Promote something already living in the home volume to the host overlay,
from inside a running container:

```sh
csb-persist ~/.gitconfig   # move to /mnt/csb-home and symlink back
csb-persist ~/.claude      # works for directories too
```

This moves the path into `/mnt/csb-home` (`~/.config/csb/home/` on the
host) and replaces the original with a symlink. The change is visible on
the host immediately, and future containers pick it up as a symlink
automatically. The target must be a direct child of `$HOME` — nested paths
aren't supported.

## Resetting

If something running inside the sandbox trashes the container's writable
layer or the home volume (see [Scope of isolation](isolation.md) for the
kinds of accidents this is meant to contain), there is no in-place repair —
remove the affected resources and let csb recreate them:

```sh
csb clean
```

This opens an interactive picker over every csb-managed container, image,
and volume (see [CLI reference](cli.md#csb-clean)); select the home volume
(and image, if you suspect it) to reset. The workspace bind mount and the
host config overlay are host-side and untouched by anything that happens
in the container, so they need no reset step.
