# Host-exec bridge

← [README](../README.md)

The host-exec bridge lets code running inside the sandbox invoke an
allowlisted set of commands on the host, with arguments passed through.
This is useful for commands that need to interact with the host
environment or for leveraging host-only resources (e.g. a GPU).

```sh
csb --host-exec \
    --host-exec-allow "make run" \
    --host-exec-allow "./cmd **" \
    -- my-agent

csb-host-run make run
csb-host-run ./cmd "done"
echo "hello" | csb-host-run ./cmd ...
```

`csb-host-run` connects over WebSocket to a host-side broker that csb
starts before launching the container (csb runs as its own broker via an
internal mode). The broker enforces the allowlist and scrubs the
environment before spawning any process — env vars injected into the
sandbox (e.g. `GIT_SSH_COMMAND`) are not forwarded to the host process.

## Enabling host-exec

Per-invocation:

```sh
csb --host-exec \
    --host-exec-allow "make run" \
    --host-exec-allow "./cmd **" \
    -- my-agent
```

Or as defaults in `~/.config/csb/config.yaml`:

```yaml
host_exec_enabled: true
host_exec_allow:
  - make run
  - ./cmd **
```

See [Configuration](configuration.md) for the full flag/env/yaml table
(`--host-exec`, `--host-exec-allow`, `host_exec_bind`).

## Allowlist pattern syntax

Each rule is a string: the command name followed by zero or more argument
patterns, separated by spaces.

```
open *          # open with exactly one arg (e.g. a URL)
say **          # say with any number of args (including none)
git status      # git status with no extra args
git log **      # git log with any trailing args
```

The command name itself can never be wildcarded — only argument positions
accept `*` (exactly one arg) or `**` (zero or more trailing args, valid
only as the last token).

## `csb-host-run` usage

```
csb-host-run [-t|-T] [-C DIR|--no-cwd] [--] <cmd> [args...]
```

| Flag | Effect |
|---|---|
| `-t` | force a PTY on the host process |
| `-T` | never allocate a PTY |
| `-C DIR` | run in `DIR` (relative to your current sandbox directory, or absolute) instead of mirroring the current directory; the resolved path must still fall inside the workspace |
| `--no-cwd` | disable cwd mirroring entirely; run in the broker's own directory |
| `--` | end of csb-host-run's own flags — remaining args are the command |

A PTY is allocated automatically when both stdin and stdout are terminals;
`-t`/`-T` override that (e.g. for rich CLIs or colored output going
through a pipe).

Reads `CSB_HOST_EXEC_URL`, `CSB_HOST_EXEC_TOKEN` (injected by csb when
`--host-exec` is on), and `CSB_WORKSPACE_DIR` (used to translate the
sandbox cwd into a workspace-relative path).

### Working directory

The host process runs in the directory that mirrors your sandbox cwd, so
running `csb-host-run` from a subdirectory of the workspace does the
expected thing on the host:

```sh
cd $CSB_WORKSPACE_DIR/internal/broker
csb-host-run go test .        # runs in <workspace>/internal/broker on the host
```

Use `-C DIR` to pick a different directory (relative to where you're
standing, or absolute), or `--no-cwd` to keep the old behaviour (the
directory csb itself was started from):

```sh
csb-host-run -C internal/broker go test .
csb-host-run --no-cwd make run
```

`-C` outside the workspace is rejected client-side before any request is
sent (exit **1**). Otherwise, `csb-host-run` converts the resolved
directory to a workspace-relative path and sends that; the broker
re-resolves it against the host workspace root, rejecting `..` traversal
and symlinks that point outside the workspace — a sandbox cannot steer
host commands into arbitrary directories via a crafted path. Outside the
workspace with no explicit `-C` (e.g. from `$HOME`), mirroring is skipped
silently.

### Exit codes

`csb-host-run` exits **1** if an explicit `-C` resolves outside the
workspace (checked client-side), the broker's own **125** if it rejects
the working directory for the same reason, **126** if the command is not
in the allowlist, and **127** if the command isn't found on the host *or*
fails to execute (e.g. a permissions error) — otherwise it propagates the
host command's own exit code.

## Security properties

- On Linux, when csb can detect a bindable container-bridge gateway
  address, the broker binds only to that interface rather than all host
  interfaces, limiting exposure to the local container bridge network.
  When it can't (e.g. rootless Podman with slirp4netns) — and always on
  macOS, where Docker Desktop's networking doesn't expose one — it falls
  back to `host_exec_bind` (default `0.0.0.0:0`, i.e. all interfaces on a
  random port). Set `host_exec_bind` explicitly if binding to every
  interface is not acceptable on your network.
- Access requires a per-session 32-byte random token injected via env var
  — each `csb` invocation gets a fresh token.
- The working directory a client may request is confined to the mounted
  workspace, checked after symlink resolution on the host.
- The host process runs with a scrubbed environment: only `PATH`, `HOME`,
  `USER`, `LANG`, and `TERM` from the broker's own env are forwarded.
- The command name itself is never wildcarded — only argument positions
  can use `*`/`**`.
- Host-exec is opt-in; the default configuration does not enable it.
