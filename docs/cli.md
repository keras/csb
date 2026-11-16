# CLI reference

← [README](../README.md)

```
csb [flags] [-- cmd...]
csb attach [NAME|ID]
csb clean
csb config show [config|context]
csb config edit [user|workdir]
csb config status
csb config update
```

## Command detection

`csb` looks at the first non-flag argument to decide what to do:

- `attach`, `clean`, `config` (or `config-edit`), `run` select that subcommand.
- Anything else is treated as the start of a passthrough command, so
  `csb python script.py` runs `python script.py` in the container without
  needing `--`:

  ```sh
  csb python script.py     # same as: csb -- python script.py
  csb run python script.py # "run" is also accepted explicitly
  ```

- With no arguments at all, `csb` runs the default interactive shell.

## `csb` / `csb run` — build and run

Builds the image if needed (see [Configuration → images and
rebuilds](configuration.md#images-and-rebuilds)), ensures the home volume
exists, resolves mounts/env/addon run-args, and execs into
`docker run`/`podman run` — replacing the csb process, except when
`--host-exec` is on, where csb stays alive as the parent so it can stop
the broker once the container exits.

If an enabled addon has no `install.sh` on disk (e.g. a typo, or a config
directory seeded by an older binary that predates it), `csb` exits **2**
with `addon not found: <name>` before attempting a build.

**Boot flags** — resolved before any YAML config is read, since they decide
where the config lives and which workdir config applies. CLI > env >
default; never settable in YAML:

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--config-dir PATH` | `CSB_CONFIG_DIR` | `~/.config/csb` | host config directory |
| `--workspace PATH` | `CSB_WORKSPACE` | current directory | host directory to mount as the workspace |
| `--no-workspace` | — | off | ephemeral `/workspace`; nothing mounted |

**Run-control flags:**

| Flag | Meaning |
|---|---|
| `--rebuild` | force a full image rebuild even if a matching image already exists |
| `-v`, `--verbose` | verbose logging (mounts, image build reason, launch command) |
| `--help-config` | print every configuration option with its flag, env var, and yaml key, plus an example `config.yaml` |
| `-h`, `--help` | short flag/subcommand summary (`run` and `config` only — `csb clean -h` prints nothing) |

`run` errors on an unrecognized flag; `config` and `clean` silently ignore
one instead.

Everything else — `--tmux`, `--shell`, `--addon`, `--mount`, `--publish`,
`--arch`, `--timezone`, `--env`, `--env-forward`, `--runtime`, `--motd`,
`--host-network`, `--host-exec*`, and their env vars and YAML keys — is documented in
[Configuration](configuration.md), since the same flags can be set via
`config.yaml` or environment variables and follow the same precedence rules.

## Detachable sessions

By default the tmux client (`--tmux`) is the container's main process, so
detaching (`prefix d`) or closing the terminal stops and removes the
container. With `--detachable` (or `detachable: true`) the container runs in
the background and only your *connection* to it comes and goes:

```sh
csb --detachable        # start (or reattach to) the session for this directory
# ... prefix d, or just close the terminal ...
csb                     # with detachable: true in config; otherwise: csb --detachable
csb attach              # reattach to the session for this directory
csb attach NAME         # reattach to a specific one
```

- `--detachable` implies `--tmux` and needs a terminal (`--no-tty` is an error).
- The container keeps running until you end the tmux session — `exit` in the
  last window — or remove it with `csb clean` or `docker rm -f`. Ending the
  session stops and removes the container as usual.
- Sessions are named `csb-<workspace>-<hex>`. `csb attach` with no argument
  picks the one started from the current workspace and config dir; if there is
  none or several, it lists the running sessions and exits 2. It exits 1 when no
  detachable session is running at all.
- A bare `csb --detachable` (no command, no `--rebuild`) reattaches to that
  session instead of starting a second container. Giving a command
  (`csb --detachable -- vim`) or `--rebuild` always starts a fresh one.
- `--host-exec` is not yet supported across detach: the broker belongs to the
  `csb` process and stops when it exits, so host commands stop working once
  you detach, and reattaching does not restart it.
- With the systemd addon, run `csb config update` once so the shipped
  Dockerfile picks up the detachable-aware `csb-tmux.sh`.

## `csb attach`

`csb attach [NAME|ID]` reattaches to a running detachable session — see above.
`NAME` is the container name; an ID prefix also works. Takes the boot flags
(`--config-dir`, `--workspace`) and `--runtime`; `-h` prints a short summary.

## `csb clean`

Interactive checkbox picker (requires a TTY) over every container, image, and
volume `csb` has created on this machine — identified by the `csb.managed`
label, **regardless of `--config-dir`**; the owning config directory is
shown per row so you can tell entries from different profiles apart, but
it doesn't filter the list. Removes containers first (they hold references
to images), then images, then volumes. Use it to reclaim disk space or as
the reset path after a container has been trashed by something running
inside it (see [Persistence → resetting](persistence.md#resetting) and
[Scope of isolation](isolation.md)).

`clean` takes no Options flags (`--addon`, `--mount`, etc. are ignored —
only the boot flags and `-v` apply), and has no dedicated `-h`/`--help`
screen of its own.

## `csb config`

| Command | Effect |
|---|---|
| `csb config show` | Print the fully resolved `Options` (all sources merged) as YAML |
| `csb config show context` | List the docker build context that would be sent for an image build (human-readable on a TTY, raw tar when piped — e.g. `csb config show context \| tar tvf -`) |
| `csb config edit` (or `edit user`) | Open `~/.config/csb/config.yaml` in `$VISUAL`/`$EDITOR`/`vi`, creating it from the generated template first |
| `csb config edit workdir` | Same, for the workdir config; errors under `--no-workspace` |
| `csb config status` | Table of managed resources (`Dockerfile`, every addon file) and whether each is up to date, missing, differs from the shipped version, or local-only |
| `csb config update` | Interactively pick drifted/missing resources to overwrite with the shipped version; existing content is backed up to `<file>.bak` |

`csb config` with no action prints the same summary as `csb config --help`.

See [Configuration](configuration.md) for what these files contain and how
they combine.
