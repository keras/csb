from __future__ import annotations

import hashlib
import io
import shlex
import tarfile
import textwrap
from pathlib import Path

from .config import CONTAINER_HOME, CONTAINER_WORKDIR, Config, Mount
from .runtime import Runtime


ENTRYPOINT_SH = """\
#!/bin/bash

_verbose_run() {
    t0=${EPOCHREALTIME/./}
    "$@"
    rc=$?
    if [ -n "${CSB_VERBOSE}" ]; then
        dt=$(( ${EPOCHREALTIME/./} - t0 ))
        printf '[csb][%s]: %d.%03ds\\n' "$*" "$((dt / 1000000))" "$(( (dt % 1000000) / 1000 ))" >&2
    fi
    return $rc
}

HOST_UID="${HOST_UID:-0}"
HOST_GID="${HOST_GID:-0}"
cp /etc/passwd /tmp/passwd
echo "sandbox:x:${HOST_UID}:${HOST_GID}::$CSB_HOME:/bin/bash" >> /tmp/passwd
echo "sandbox:x:${HOST_UID}:${HOST_GID}::$CSB_HOME:/bin/bash" >> /etc/passwd
# PAM account validation requires a shadow entry; ! = locked password (fine for NOPASSWD sudo)
echo "sandbox:!:19000:0:99999:7:::" >> /etc/shadow
export NSS_WRAPPER_PASSWD=/tmp/passwd
export NSS_WRAPPER_GROUP=/etc/group
export LD_PRELOAD=$(find /usr/lib -name 'libnss_wrapper.so' | head -1)
export HOME="$CSB_HOME"
export PATH="$HOME/.local/bin:$HOME/bin:$PATH"

# Fix ownership of home dir
chown "${HOST_UID}:${HOST_GID}" $HOME

# Symlink entries from /mnt/csb-home into $HOME.
# /mnt/csb-home is a bind mount of ~/.config/csb/home/ on the host;
# symlinking rather than bind-mounting lets csb-persist add new entries
# without a container restart.
if [ -d /mnt/csb-home ]; then
    _prev_opts=$(shopt -p dotglob nullglob)
    shopt -s dotglob nullglob
    for _entry in /mnt/csb-home/*; do
        _name=$(basename "$_entry")
        _target="$HOME/$_name"
        if [ -L "$_target" ] && [ "$(readlink "$_target")" = "$_entry" ]; then
            : # already correctly symlinked
        elif [ -e "$_target" ] || [ -L "$_target" ]; then
            printf '[csb] warning: %s exists in home volume and shadows ~/.config/csb/home/%s — delete the volume copy to use the host version\\n' "$_name" "$_name" >&2
        else
            ln -s "$_entry" "$_target"
            chown -h "${HOST_UID}:${HOST_GID}" "$_target"
        fi
    done
    eval "$_prev_opts"
fi

if [ -n "${CSB_NESTED_PODMAN}" ]; then
    # Promote root mount to shared propagation so Podman can propagate mounts
    # across its own mount namespaces (Docker defaults to private propagation).
    mount --make-rshared /
    # subuid/subgid ranges required by rootless Podman for uid mapping
    echo "sandbox:100000:65536" >> /etc/subuid
    echo "sandbox:100000:65536" >> /etc/subgid
    # XDG_RUNTIME_DIR is required by rootless Podman for its socket
    export XDG_RUNTIME_DIR="/run/user/${HOST_UID}"
    mkdir -p "${XDG_RUNTIME_DIR}"
    chmod 700 "${XDG_RUNTIME_DIR}"
    chown "${HOST_UID}:${HOST_GID}" "${XDG_RUNTIME_DIR}"
    # Make /proc/sys writable so nested container runtimes can configure
    # network namespaces. Requires SYS_ADMIN + seccomp=unconfined.
    mount -o remount,rw /proc/sys 2>/dev/null || true
fi

for script in /etc/csb/entrypoint.d/*.sh; do
    [ -x "$script" ] && source "$script"
done

# gosu rewrites HOME/USER/LOGNAME from the target uid's passwd entry. When
# HOST_UID matches the uid we're already running as, skip gosu entirely —
# there's no privilege to drop, and skipping avoids the env rewrite.
if [ "${HOST_UID}:${HOST_GID}" = "$(id -u):$(id -g)" ]; then
    exec "$@"
fi
exec gosu "${HOST_UID}:${HOST_GID}" "$@"
"""


CSB_PERSIST_SH = """\
#!/bin/bash
set -euo pipefail

CSB_HOST_HOME=/mnt/csb-home

usage() {
    printf 'Usage: csb-persist <path>\\n\\n' >&2
    printf '  Move <path> to %s and symlink it back,\\n' "$CSB_HOST_HOME" >&2
    printf '  making it persistent in ~/.config/csb/home/ on the host.\\n' >&2
    exit 1
}

[ $# -ne 1 ] && usage

src=$(realpath -sm "$1")
name=$(basename "$src")
dst="$CSB_HOST_HOME/$name"

[ ! -d "$CSB_HOST_HOME" ] && { printf '%s is not mounted\\n' "$CSB_HOST_HOME" >&2; exit 1; }
[ ! -e "$src" ] && { printf 'no such file or directory: %s\\n' "$1" >&2; exit 1; }
[ "$(dirname "$src")" != "$HOME" ] && { printf 'path must be directly under $HOME: %s\\n' "$src" >&2; exit 1; }

if [ -L "$src" ] && [ "$(readlink "$src")" = "$dst" ]; then
    printf '%s is already persisted\\n' "$name" >&2
    exit 0
fi
[ -L "$src" ] && { printf '%s is already a symlink\\n' "$src" >&2; exit 1; }
[ -e "$dst" ] && { printf '%s already exists in %s\\n' "$name" "$CSB_HOST_HOME" >&2; exit 1; }

mv "$src" "$dst"
ln -s "$dst" "$src"
printf 'Persisted: %s -> %s\\n' "$src" "$dst"
"""


_ADDONS_DIR = Path(__file__).parent / "addons"


_BASE_PACKAGES = frozenset(
    {
        "sudo",
        "git",
        "curl",
        "gpg",
        "tmux",
        "zsh",
        "gosu",
        "libnss-wrapper",
        "bash-completion",
        "nano",
        "build-essential",
        "pkg-config",
        "libssl-dev",
    }
)

_X11_PACKAGES = frozenset(
    {
        "libx11-6",
        "libxext6",
        "libxrender1",
        "libxtst6",
        "libxi6",
        "libxcursor1",
        "libx11-xcb1",
        "libxkbcommon-x11-0",
    }
)

_PODMAN_PACKAGES = frozenset({"podman", "fuse-overlayfs", "uidmap"})

_BASE_ENV = {
    "LANG": "C.UTF-8",
    "LC_ALL": "C.UTF-8",
    "EDITOR": "nano",
    "CSB_HOME": CONTAINER_HOME,
}


def _apt_packages(nested_podman: bool) -> list[str]:
    pkgs = _BASE_PACKAGES | _X11_PACKAGES
    if nested_podman:
        pkgs = pkgs | _PODMAN_PACKAGES
    return sorted(pkgs)


def _podman_snippets(nested_podman: bool) -> dict[str, str]:
    if not nested_podman:
        return {"alias": "&& true", "config_copy": ""}
    return {
        "alias": r"&& printf '\nalias docker=podman\n' >> /etc/bash.bashrc",
        "config_copy": "\nCOPY containers /etc/containers\n",
    }


def _host_run_path() -> Path | None:
    """Return the path to the csb-host-run binary for the container arch, or None."""
    import platform
    bin_dir = Path(__file__).parent / "bin"
    machine = platform.machine().lower()
    arch = "arm64" if machine in ("arm64", "aarch64") else "amd64"
    # prefer arch-specific binary (built by hatch hook); fall back to plain name (Makefile)
    for name in (f"csb-host-run.{arch}", "csb-host-run"):
        p = bin_dir / name
        if p.exists() and not p.is_symlink():
            return p
        if p.is_symlink() and p.resolve().exists():
            return p.resolve()
    return None


def _host_run_hash() -> str | None:
    """Return the SHA-256 of the bundled csb-host-run binary, or None if absent."""
    p = _host_run_path()
    if p is None:
        return None
    return hashlib.sha256(p.read_bytes()).hexdigest()


def _make_dockerfile(base_image: str, nested_podman: bool, host_run_hash: str | None = None) -> str:
    """Generate a clean Dockerfile for the given configuration."""
    packages = _apt_packages(nested_podman)
    podman = _podman_snippets(nested_podman)

    # rf-string: raw keeps Dockerfile \ continuations and shell \n sequences
    # literal; f-string splices in the pre-computed conditional variables.
    out = rf"""
FROM {base_image}

RUN apt-get update && apt-get install -y \
    {" ".join(packages)} \
    && rm -rf /var/lib/apt/lists/* \
    && echo "sandbox ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/sandbox \
    && chmod 0440 /etc/sudoers.d/sandbox

{podman["config_copy"]}

# Shell setup
RUN printf '\n[ -f /usr/share/bash-completion/bash_completion ] && . /usr/share/bash-completion/bash_completion\n' \
    >> /etc/bash.bashrc \
    {podman["alias"]}

RUN mkdir -p /etc/csb/entrypoint.d

COPY csb/build.d /tmp/build.d
RUN for script in /tmp/build.d/*.sh; do \
        [ -x "$script" ] && "$script"; \
    done && rm -rf /tmp/build.d

ENV {" ".join(f"{k}={shlex.quote(v)}" for k, v in _BASE_ENV.items())}

RUN mkdir -p $CSB_HOME {CONTAINER_WORKDIR} /mnt/csb-home && chmod 777 {CONTAINER_WORKDIR} /mnt/csb-home

COPY csb/csb-persist /usr/local/bin/csb-persist
RUN chmod +x /usr/local/bin/csb-persist
"""
    if host_run_hash:
        out += f"""
# csb-host-run sha256:{host_run_hash}
COPY csb/csb-host-run /usr/local/bin/csb-host-run
RUN chmod +x /usr/local/bin/csb-host-run
"""
    out += """
# Entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
CMD ["bash"]
"""
    return textwrap.dedent(out).strip()


def _addon_scripts(cfg: Config) -> list[Path]:
    """Return addon scripts from addons/ that are enabled by cfg.addons, in sorted order."""
    return sorted(p for p in _ADDONS_DIR.glob("*.sh") if p.stem in cfg.addons)


def image_name(cfg: Config) -> str:
    """Return the image name to use for this configuration.

    Defaults to csb:<12-char SHA-256 of the build context>, so each distinct
    Dockerfile + entrypoint combination gets its own cached image automatically.
    Override by setting cfg.image (CSB_IMAGE env or `image:` in config.yaml).
    """
    if cfg.image:
        return cfg.image
    addon_content = "".join(p.read_text() for p in _addon_scripts(cfg))
    digest = hashlib.sha256(
        (
            _make_dockerfile(cfg.base_image, cfg.nested_podman, _host_run_hash())
            + ENTRYPOINT_SH
            + CSB_PERSIST_SH
            + addon_content
        ).encode()
    ).hexdigest()
    return f"csb:{digest[:12]}"


_context_files = {
    "entrypoint.sh": ENTRYPOINT_SH,
    "csb/csb-persist": CSB_PERSIST_SH,
    "containers/policy.json": r'{"default":[{"type":"insecureAcceptAnything"}]}',
    "containers/registries.conf": textwrap.dedent(
        r"""
        [registries.search]
        registries = ["docker.io"]
        """
    ),
    "containers/storage.conf": textwrap.dedent(
        r"""
        [storage]
        driver = "overlay"
        [storage.options]
        mount_program = "/usr/bin/fuse-overlayfs"
        """
    ),
    "containers/containers.conf": textwrap.dedent(
        r"""
        [containers]
        # Docker bind-mounts /proc/sys read-only so crun cannot set sysctls.
        default_sysctls = []
        # Sharing the outer PID namespace avoids crun needing to mount a new proc
        # inside a nested user+mount namespace, which Docker prevents.
        pidns = "host"
        # slirp4netns sets accept_dad before the inner mount namespace is active,
        # hitting the outer read-only /proc/sys; disabling IPv6 skips that sysctl.
        network_cmd_options = ["enable_ipv6=false"]
        """
    ),
}


def _build_context_tar(cfg: Config) -> bytes:
    """Create an in-memory tar archive with the Dockerfile and entrypoint script."""
    hrh = _host_run_hash()
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w") as tar:
        for name, content in {
            "Dockerfile": _make_dockerfile(cfg.base_image, cfg.nested_podman, hrh),
            **_context_files,
        }.items():
            data = content.encode()
            info = tarfile.TarInfo(name)
            info.size = len(data)
            tar.addfile(info, io.BytesIO(data))

        dir_info = tarfile.TarInfo("csb/build.d")
        dir_info.type = tarfile.DIRTYPE
        dir_info.mode = 0o755
        tar.addfile(dir_info)

        for addon_path in _addon_scripts(cfg):
            data = addon_path.read_bytes()
            info = tarfile.TarInfo(f"csb/build.d/{addon_path.name}")
            info.size = len(data)
            info.mode = 0o755
            tar.addfile(info, io.BytesIO(data))

        if hrh is not None:
            bin_path = _host_run_path()
            data = bin_path.read_bytes()
            info = tarfile.TarInfo("csb/csb-host-run")
            info.size = len(data)
            info.mode = 0o755
            tar.addfile(info, io.BytesIO(data))

    return buf.getvalue()


def resolve_mounts(cfg: Config) -> list[Mount]:
    """Build the list of bind mounts for the container."""
    mounts: list[Mount] = []

    # Workspace directory (--no-workspace skips this)
    if cfg.workspace is not None:
        mounts.append(Mount(str(cfg.workspace), cfg.workdir, readonly=False))

    # Mount csb_home to /mnt/csb-home; the entrypoint symlinks each entry into
    # $HOME so that csb-persist can add new entries live without a restart.
    if cfg.csb_home.is_dir():
        mounts.append(Mount(str(cfg.csb_home), "/mnt/csb-home", readonly=False))

    # Explicit mounts from config.yaml (parsed in config.py via _parse_mount).
    mounts.extend(cfg.mount)

    return mounts


def resolve_env(
    cfg: Config,
    broker_url: str | None = None,
    broker_token: str | None = None,
) -> list[tuple[str, str]]:
    """Collect environment variables to pass into the container."""
    import os
    import sys

    # In rootless Podman the host user (uid N) maps to uid 0 inside the
    # container's user namespace.  Bind-mount paths owned by host uid N
    # therefore appear as uid 0 in the container.  Passing HOST_UID=N and
    # then calling gosu N would switch to an unmapped uid, losing write
    # access.  We stay as uid 0 instead (which is the host user externally).
    if cfg.container_cli == "podman" and os.getuid() != 0:
        host_uid, host_gid = "0", "0"
    else:
        host_uid, host_gid = str(os.getuid()), str(os.getgid())

    env: list[tuple[str, str]] = [
        ("HOST_UID", host_uid),
        ("HOST_GID", host_gid),
        ("HOME", CONTAINER_HOME),
        ("SHELL", "/bin/bash"),
        ("TERM", os.environ.get("TERM", "xterm-256color")),
        ("COLORTERM", os.environ.get("COLORTERM", "")),
    ]

    # X11 forwarding: on macOS, container runtimes run in a VM so the host X11
    # socket isn't reachable. Always point at the host via TCP so XQuartz
    # works after: xhost + localhost  (run once in XQuartz terminal)
    host_display = os.environ.get("DISPLAY", "")
    if sys.platform == "darwin" or not host_display or host_display.startswith("/"):
        gateway = (
            "host.containers.internal"
            if cfg.container_cli == "podman"
            else "host.docker.internal"
        )
        display = f"{gateway}:0"
    else:
        display = host_display
    env.append(("DISPLAY", display))

    if cfg.nested_podman:
        env.append(("CSB_NESTED_PODMAN", "1"))
    if cfg.verbose:
        env.append(("CSB_VERBOSE", "1"))

    # Forward user-specified env vars (cfg.env_forward — from CSB_ENV_FORWARD or config.yaml)
    for name in cfg.env_forward:
        if name in os.environ:
            env.append((name, os.environ[name]))

    # Inject new KEY=VALUE pairs (cfg.env_inject — from CSB_ENV or config.yaml)
    for pair in cfg.env_inject:
        key, _, val = pair.partition("=")
        env.append((key, val))

    if broker_url and broker_token:
        env.append(("CSB_HOST_EXEC_URL", broker_url))
        env.append(("CSB_HOST_EXEC_TOKEN", broker_token))

    return env


def _resolve_container_cmd(cfg: Config) -> list[str]:
    """Determine the command the container should run.

    Applies the same routing as the old entrypoint:
      - No args          -> bash
      - Otherwise        -> passthrough as-is

    When tmux is enabled, wraps the cmd in a tmux session.
    """
    args = cfg.passthrough_args

    if not args:
        inner = ["bash"]
    else:
        inner = args

    if cfg.use_tmux:
        post_command = "exec bash" if inner[0] not in ("bash", "zsh") else ""
        quoted = " ".join(shlex.quote(a) for a in inner)
        return ["tmux", "new-session", "-s", "main", f"{quoted}; {post_command}"]

    return inner


def build_run_command(
    cfg: Config, mounts: list[Mount], env: list[tuple[str, str]]
) -> list[str]:
    """Assemble the full container run command."""
    cmd: list[str] = [cfg.container_cli, "run", "-i"]
    if cfg.use_tty:
        cmd.append("-t")
    cmd.append("--rm")
    if cfg.nested_podman:
        # Required for rootless Podman inside the container:
        #   /dev/fuse          — fuse-overlayfs storage driver
        #   /dev/net/tun       — slirp4netns user-mode network helper
        #   seccomp=unconfined — allows clone(CLONE_NEWUSER) and related syscalls
        #   apparmor=unconfined — Docker's AppArmor profile blocks mount(2) even
        #                         with SYS_ADMIN; crun needs to mount proc inside
        #                         the inner container's mount namespace
        #   SYS_ADMIN          — lets setuid newuidmap write to /proc/<pid>/uid_map
        #                         in the parent user namespace (CAP_SETUID alone is
        #                         not sufficient when nested inside Docker)
        #   NET_ADMIN          — allows writing to /proc/sys/net/* in network
        #                         namespaces that podman creates for inner containers
        cmd.extend(
            [
                "--device",
                "/dev/fuse",
                "--device",
                "/dev/net/tun",
                "--security-opt",
                "seccomp=unconfined",
                "--security-opt",
                "apparmor=unconfined",
                "--cap-add",
                "SYS_ADMIN",
                "--cap-add",
                "NET_ADMIN",
            ]
        )

    if cfg.host_network:
        cmd.extend(["--network", "host"])

    # Named volume as home base — tool state (.cargo, .rustup, .local, …)
    # stays in the runtime's storage, off the host filesystem.
    cmd.extend(["-v", f"{cfg.home_volume}:{CONTAINER_HOME}"])

    for mount in mounts:
        cmd.extend(mount.to_args())

    cmd.extend(["-w", cfg.workdir])

    for key, val in env:
        cmd.extend(["-e", f"{key}={val}"])

    cmd.append(image_name(cfg))

    cmd.extend(_resolve_container_cmd(cfg))
    return cmd
