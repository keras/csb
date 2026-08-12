# CLI reference

← [README](../README.md)

```
csb [flags] [-- cmd...]
csb clean
csb config show [config|context]
csb config edit [user|workdir]
csb config status
csb config update
```

## Command detection

`csb` looks at the first non-flag argument to decide what to do:

- `clean`, `config` (or `config-edit`), `run` select that subcommand.
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
`--arch`, `--env`, `--env-forward`, `--runtime`, `--motd`, `--host-network`,
`--host-exec*`, and their env vars and YAML keys — is documented in
[Configuration](configuration.md), since the same flags can be set via
`config.yaml` or environment variables and follow the same precedence rules.

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
