# Configuration

← [README](../README.md)

## Config directory layout

Everything lives under `~/.config/csb` by default (`--config-dir` /
`CSB_CONFIG_DIR` to change it — useful for isolating separate tool
profiles):

```
~/.config/csb/
├── config.yaml          # user defaults, commented template on first run
├── projects/
│   └── <hash>.yaml       # workdir config, one per mounted directory
├── home/                # host-backed overlay, symlinked into $HOME — see persistence.md
├── Dockerfile            # user-editable base image recipe
└── addons/<name>/…       # seeded copies of the shipped addons; add your own here too
```

All of it is seeded on first run and never overwritten automatically —
see [Keeping shipped resources in sync](#keeping-shipped-resources-in-sync)
for how drift between your copies and a newer binary's shipped versions is
surfaced.

This per-project overlay (referred to below as the **workdir config**)
lives at `projects/<sha256(workspace)[:16]>.yaml`, keyed by the absolute
host path of the workspace directory. Edit it with `csb config edit
workdir`.

## Precedence

For each option, the first source that sets it wins:

```
CLI flag  >  environment variable  >  workdir config  >  user config  >  built-in default
```

Boot flags (`--config-dir`, `--workspace`, `--no-workspace`) are the
exception — they resolve from CLI > env > default only, before any YAML is
read (see [CLI reference](cli.md)).

### List options

Options that are lists (`addons`, `mount`, `env`, `env_forward`, `publish`,
`host_exec_allow`) fold **bottom-up** across the source chain rather than
having one source win outright:

- Within a single source, entries are either all plain (**replace** every
  lower-precedence value for that key) or all prefixed with `+` (**append**
  to whatever lower sources already contributed). Mixing plain and `+`
  entries in the same source is an error, and a bare `+` with no value is an
  error.
- `addons` additionally dedups by name, keeping the first occurrence
  (`dedup:first`) — so a workdir override can't silently double-install an
  addon already enabled by the user config.

Example: user config sets `addons: [mise, sudo]`; a workdir config sets
`addons: [+podman]` to add nested Podman for one project without repeating
the base list.

## Options reference

| Option | Flag | Env | YAML key | Default | Notes |
|---|---|---|---|---|---|
| tmux | `--tmux` / `--no-tmux` | — | `tmux` | `false` | wrap the session in `tmux new-session -s main` |
| TTY | `--tty` / `--no-tty` | — | `tty` | auto-detect (is stdin a terminal) | passes `-t` to the runtime |
| shell | `--shell NAME` | — | `default_shell` | `bash` | `$SHELL`, new tmux windows, and the default startup command |
| startup command | — (positional/`--`) | — | `default_cmd` | `<shell> -l` | overridden by any command given on the CLI |
| addons | `--addon "NAME [ARGS…]"` (repeatable) | — | `addons` | `mise sudo` | see [Addons](addons.md) |
| arch | `--arch ARCH` | `CSB_ARCH` | `arch` | host arch | `amd64` or `arm64`; needs QEMU/binfmt on the host when it differs from the host's own arch |
| timezone | `--timezone TZ` | `CSB_TIMEZONE` | `timezone` | host timezone | IANA zone name (e.g. `America/New_York`); mirrors the host by default |
| mount | `--mount SRC:DST[:MODE]` (repeatable) | — | `mount` | — | `~` expands host-side to `$HOME`, container-side to `/home/sandbox`; **mode defaults to `ro` when omitted** |
| env-forward | `--env-forward NAME` (repeatable) | `CSB_ENV_FORWARD` (space-separated) | `env_forward` | — | forward these host env var *names* (and current values) into the container |
| env | `--env KEY=VALUE` (repeatable) | `CSB_ENV` (space-separated) | `env` | — | inject literal `KEY=VALUE` pairs |
| publish | `--publish SPEC` (repeatable) | `CSB_PUBLISH` (space-separated) | `publish` | — | see [Networking](networking.md) |
| home volume | — | `CSB_HOME_VOLUME` | `home_volume` | `csb-home` | named volume backing `/home/sandbox`; **no CLI flag** |
| runtime | `--runtime auto\|docker\|podman` | `CSB_RUNTIME` | `runtime` | `auto` | `auto` picks `docker` if found on `PATH`, else `podman` |
| motd | `--motd` / `--no-motd` | `CSB_MOTD` | `motd` | `true` | welcome box on interactive login |
| host network | `--host-network` / `--no-host-network` | `CSB_HOST_NETWORK` | `host_network` | `false` | mutually exclusive with `--publish` |
| host-exec enabled | `--host-exec` / `--no-host-exec` | `CSB_HOST_EXEC` | `host_exec_enabled` | `false` | see [Host-exec bridge](host-exec.md) |
| host-exec allow | `--host-exec-allow RULE` (repeatable) | — | `host_exec_allow` | — | see [Host-exec bridge](host-exec.md) |
| host-exec bind | — | — | `host_exec_bind` | `0.0.0.0:0` | broker listen address; **no CLI flag**; only used as a fallback when csb can't auto-detect a bindable container-bridge address — see [Host-exec → security properties](host-exec.md#security-properties) |

Every boolean flag also has a `--no-<name>` partner. This table is meant for
the *semantics* the generated reference can't show (precedence, list
folding, the `ro` mount default); for the exhaustive machine-generated list
run `csb --help-config` — it stays in sync with the code by construction.

## Environment variables

| Variable | Effect |
|---|---|
| `CSB_CONFIG_DIR` | config directory (default `~/.config/csb`) |
| `CSB_WORKSPACE` | host directory to mount (default: CWD) |
| `CSB_ARCH` | container architecture |
| `CSB_TIMEZONE` | container timezone (default: mirror the host's) |
| `CSB_ENV_FORWARD` | space-separated host env var names to forward |
| `CSB_ENV` | space-separated `KEY=VALUE` pairs to inject |
| `CSB_PUBLISH` | space-separated publish specs |
| `CSB_HOME_VOLUME` | home volume name |
| `CSB_RUNTIME` | `auto`, `docker`, or `podman` |
| `CSB_MOTD` | show the welcome box (`0`/`false`/`no` to disable) |
| `CSB_HOST_NETWORK` | use host networking |
| `CSB_HOST_EXEC` | start the host-exec broker |

`VISUAL` / `EDITOR` (fallback `vi`) select the editor for `csb config edit`.

## Images and rebuilds

The image is named `csb:<12 hex chars>` — a SHA-256 hash over the
Dockerfile, `entrypoint.sh`, `csb-persist`, `csb-help`, every enabled
addon's bundled files (relative path + contents, so renaming a file changes
the hash even if its contents don't), the generated addon-install script,
and the `csb-host-run` binary for the target arch. Two invocations with
identical inputs get the identical image name and skip the build; changing
anything in that list — editing the Dockerfile, adding an addon, upgrading
csb itself — produces a new name and triggers a rebuild automatically. Force
one anyway with `--rebuild`.

Images, containers, and the home volume all carry a `csb.managed=true`
label plus a `csb.config-dir=<path>` label recording which config
directory produced them. `csb clean` lists **every** csb-managed resource
on the machine regardless of `--config-dir` — the config-dir label is
shown per row in the picker so you can tell them apart, not used to
filter the list.

`~/.config/csb/Dockerfile` is read from disk on every build, so you can edit
it directly (add packages, change the base image, etc.) — it is not
regenerated. `csb config show context` lets you inspect exactly what would
be sent to the build without actually building.

### Keeping shipped resources in sync

The Dockerfile and addon install scripts are seeded from the csb binary the
first time it sees a given config directory, and the seeder never
overwrites an existing file. That means an updated binary can ship newer
versions of these files while your config directory keeps using the old
ones — silently, unless you check:

```sh
csb config status   # list Dockerfile + addons and whether they match the embedded versions
csb config update   # interactively pick which to overwrite with the shipped version
```

With `-v`/`--verbose`, `csb run` also logs a one-line nudge
(`shipped resources  differ=N  hint=csb config status`) when anything is
out of date. `config update` backs up any file it overwrites to
`<file>.bak` first, so local edits are never lost outright.
