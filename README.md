# csb — Container Sandbox

Run commands in a throwaway container with a persistent home. `csb` mounts your
current directory as the workspace, builds a minimal Debian image on demand
(cached by content hash), and gives you a shell — or runs a command directly —
inside it. State you want to keep across runs lives in a named volume and an
optional host-backed config overlay; everything else disappears when the
container is removed.

## Install

```sh
mise use -g github:keras/csb
```

Or download a prebuilt binary from the [releases page](https://github.com/keras/csb/releases)
and place it on your `PATH`.

Or clone and build (requires Go):

```sh
make build
cp bin/csb /usr/local/bin
```

## Quick start

`cd` into a project and start a sandbox. The first run builds the image
(cached after that — later runs start in seconds); your current directory
is mounted live, so edits inside the sandbox hit the real files on disk.

```sh
$ cd ~/projects/my-app
$ csb
```

Inside, install whatever the project needs and work as usual — `mise` is
enabled by default and installs land under `$HOME`, which persists across
runs via a named volume:

```sh
sandbox$ csb-help                # orientation: what's mounted, what persists, what's enabled
sandbox$ mise use -g node@lts   # or python, go, rust, ...
sandbox$ npm install && npm test
sandbox$ exit
```

**Dotfiles.** Anything already on the host goes in the config overlay, and
is symlinked into every sandbox:

```sh
$ cp ~/.gitconfig ~/.config/csb/home/
```

For state _created inside_ the sandbox that you want to keep the same way,
promote it from within the session instead:

```sh
sandbox$ csb-persist ~/.npmrc
```

### Sandboxing a coding agent

The same loop works well for running an agent against a repo with the
container as a blast-radius boundary — see [Scope of
isolation](docs/isolation.md):

```sh
$ cd ~/projects/my-app
$ csb
sandbox$ mise use -g claude
sandbox$ claude
sandbox$ csb-persist ~/.claude   # keep auth/config for next time
sandbox$ csb-persist ~/.claude.json
```

### One-offs

```sh
$ csb -- python script.py  # run a command directly, no shell
$ csb --no-workspace       # ephemeral /workspace, no host directory mounted
$ csb clean                # interactively remove csb-managed containers/images/volumes
```

Run `csb --help` for the full flag list, or `csb --help-config` for every
configuration option, its env var and yaml key, and an example `config.yaml`.

## Documentation

| Topic                                   |                                                                                                 |
| --------------------------------------- | ----------------------------------------------------------------------------------------------- |
| [CLI reference](docs/cli.md)            | Subcommands (`run`, `clean`, `config`), flags, implicit-run passthrough                         |
| [Configuration](docs/configuration.md)  | `config.yaml`, per-workspace overlays, precedence, env vars, image rebuilds                     |
| [Persistence](docs/persistence.md)      | Home volume, host config overlay, `csb-persist`, resetting state                                |
| [Addons](docs/addons.md)                | Enabling shipped addons (`mise`, `gui`, `selkies`, `podman`, `systemd`, …) and writing your own |
| [Networking](docs/networking.md)        | `--publish`, dynamic host ports, `--host-network`                                               |
| [Host-exec bridge](docs/host-exec.md)   | Calling allowlisted host commands from inside the sandbox                                       |
| [Scope of isolation](docs/isolation.md) | What csb protects against, and what it doesn't                                                  |
