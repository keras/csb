# Scope of isolation

← [README](../README.md)

csb is an isolation tool, not a hard security boundary. Its goal is to
contain accidental damage from scripts, build tools, and AI agents run
inside — a misbehaving script should be able to trash the container
without affecting the host. Classic cases this catches:

- `rm -rf "/$path"` where `$path` is unset or empty
- `find / -name ... -delete` with a typo that widens the match
- An agent that "cleans up temp files" but resolves the wrong parent
- A package postinstall hook that rewrites `~/.gitconfig`, `~/.ssh/config`,
  or shell rc files
- A `git clean -fdx` run from the wrong directory

When any of these happen inside csb, the container and what's mounted
there (the workspace directory and home volume) is what gets wiped; the
host stays intact. Reset with `csb clean` and continue — see
[Persistence → resetting](persistence.md#resetting).

## Not a hardened boundary

csb is **not** a hardened boundary against deliberately malicious code.
Some addons trade away isolation for nested-container or boot-mode
functionality; know what you're enabling.

### `podman-rootless`

Enables rootless nested Podman with:

| Flag | Why |
|---|---|
| `--cap-add SYS_ADMIN` | For `newuidmap`/`newgidmap` to write UID maps in the parent user namespace, and `mount --make-rshared` for nested mount propagation |
| `--cap-add NET_ADMIN` | Writing to `/proc/sys/net/*` in the network namespaces Podman creates for inner containers |
| `--security-opt seccomp=unconfined` | Allows `clone(CLONE_NEWUSER)` and related namespace syscalls |
| `--security-opt apparmor=unconfined` | Docker's default AppArmor profile blocks `mount(2)` even with `SYS_ADMIN` |
| `--device /dev/fuse` | fuse-overlayfs storage driver for nested containers |
| `--device /dev/net/tun` | slirp4netns user-mode networking for nested containers |

### `podman` (rootful)

Goes further and runs the sandbox container itself `--privileged`, because
rootful nested containers need cgroup delegation, a mountable `/proc`, and
native overlay storage that the flags above don't provide. This is a large
privilege relaxation — prefer `podman-rootless` unless you specifically
need rootful behavior.

### `systemd`

Boots systemd as PID 1, needing a shared cgroup namespace and a writable
`/sys/fs/cgroup` mount (`--cgroupns=host`, `-v
/sys/fs/cgroup:/sys/fs/cgroup:rw`), plus (under Podman specifically) a
private PID namespace. Lighter than the nested-Podman addons in terms of
raw privilege, but still a heavier mode than the default single-process
sandbox — some hosts may additionally require `--privileged` to boot
systemd at all.

With any of these, a kernel vulnerability in the exposed syscall surface
is reachable from inside the container. Do not run untrusted code here —
if you need tighter isolation, use a different tool.
