"""Smoke tests for csb end-to-end behaviour.

Run with:  pytest -m smoke -v
Skip with: pytest -m 'not smoke and not docker and not podman'

These tests run real containers and are hermetic: each session gets a unique
image tag (csb-smoke:<id>), a unique home volume (csb-home-smoke-<id>), and a
tmp directory for HOME and CSB_CONFIG_DIR.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
import types
from pathlib import Path
from uuid import uuid4

import pytest

SCRIPT = str(Path(__file__).parent.parent.parent / "csb")
CONTAINER_HOME = "/home/sandbox"
smoke = pytest.mark.smoke


def _broker_available() -> bool:
    if shutil.which("csb-host-broker"):
        return True
    return (Path(__file__).parent.parent.parent / "bin" / "csb-host-broker").exists()


_requires_broker = pytest.mark.skipif(
    not _broker_available(), reason="csb-host-broker binary not built"
)


def host_exec(fn):
    """Decorator: applies pytest.mark.host_exec and skips if broker binary not built."""
    return _requires_broker(pytest.mark.host_exec(fn))


# ---------------------------------------------------------------------------
# Runtime detection
# ---------------------------------------------------------------------------


def _detect_runtime() -> str | None:
    for cli in ("docker", "podman"):
        try:
            if subprocess.run([cli, "info"], capture_output=True).returncode == 0:
                return cli
        except FileNotFoundError:
            pass
    return None


_RUNTIME = _detect_runtime()


# ---------------------------------------------------------------------------
# Session-scoped fixture
# ---------------------------------------------------------------------------


@pytest.fixture(scope="session")
def smoke_env(tmp_path_factory):
    """Set up an isolated environment; yield a run() helper; clean up afterwards.

    The image is built by the first csb invocation (same as real usage).
    Docker creates the named volume automatically on first container start.
    """
    if _RUNTIME is None:
        pytest.skip("no container runtime (docker/podman) available")

    uid = uuid4().hex[:8]
    image = f"csb-smoke:{uid}"
    home_vol = f"csb-home-smoke-{uid}"

    root = tmp_path_factory.mktemp("smoke")
    home = root / "home"
    config_dir = home / ".config" / "csb"
    workspace = home / "dev" / "smoke"
    workspace.mkdir(parents=True)

    base_env = {
        **os.environ,
        "HOME": str(home),
        "CSB_CONFIG_DIR": str(config_dir),
        "CSB_IMAGE": image,
        "CSB_HOME_VOLUME": home_vol,
        "CSB_RUNTIME": _RUNTIME,
    }
    base_env.pop("CSB_ENV", None)

    # Rootless Podman inside Docker: override storage driver to vfs so that
    # overlay-on-overlay issues don't break RUN steps in the image build.
    if _RUNTIME == "podman":
        containers_cfg = home / ".config" / "containers"
        containers_cfg.mkdir(parents=True, exist_ok=True)
        (containers_cfg / "storage.conf").write_text('[storage]\ndriver = "vfs"\n')

    def run(*args: str, cwd: Path | None = None, timeout: int = 120, **extra_env: str):
        env = {**base_env, **extra_env}
        return subprocess.run(
            [sys.executable, SCRIPT, "--no-tmux", "--no-tty", *args],
            cwd=str(cwd or workspace),
            env=env,
            capture_output=True,
            text=True,
            timeout=timeout,
        )

    # Pre-build the image before tests start; the initial apt-based build can
    # take several minutes — well over the 120 s per-test timeout.
    _build = run("--", "true", timeout=600)
    if _build.returncode != 0:
        pytest.skip(f"Failed to build smoke image: {_build.stderr[:300]}")

    yield types.SimpleNamespace(
        home=home,
        config_dir=config_dir,
        workspace=workspace,
        image=image,
        home_vol=home_vol,
        runtime=_RUNTIME,
        run=run,
        base_env=base_env,
    )

    subprocess.run([_RUNTIME, "rmi", "-f", image], capture_output=True, env=base_env)
    subprocess.run(
        [_RUNTIME, "volume", "rm", "-f", home_vol], capture_output=True, env=base_env
    )
    if _RUNTIME == "podman":
        storage = home / ".local" / "share" / "containers" / "storage"
        subprocess.run(
            [_RUNTIME, "unshare", "rm", "-rf", str(storage)],
            capture_output=True,
            env=base_env,
        )


# ---------------------------------------------------------------------------
# Smoke scenarios
# ---------------------------------------------------------------------------


@smoke
def test_build_and_echo(smoke_env):
    r = smoke_env.run("--", "echo", "smoke-ok")
    assert r.returncode == 0, r.stderr
    assert "smoke-ok" in r.stdout


@smoke
def test_home_volume_persistence(smoke_env):
    r1 = smoke_env.run("--", "sh", "-c", "echo persistent > ~/marker.txt")
    assert r1.returncode == 0, r1.stderr

    r2 = smoke_env.run("--", "cat", f"{CONTAINER_HOME}/marker.txt")
    assert r2.returncode == 0, r2.stderr
    assert "persistent" in r2.stdout


@smoke
def test_workspace_mount_and_uid(smoke_env, tmp_path):
    r = smoke_env.run("--", "sh", "-c", "echo hi > out.txt", cwd=tmp_path)
    assert r.returncode == 0, r.stderr

    out = tmp_path / "out.txt"
    assert out.exists(), "out.txt was not written to the workspace dir"
    assert out.stat().st_uid == os.getuid(), "file is not owned by the host user"


@smoke
def test_mise_addon_on_path(smoke_env):
    r = smoke_env.run("--", "bash", "-lc", "command -v mise")
    assert (
        r.returncode == 0
    ), f"mise not found on PATH\nstdout:{r.stdout}\nstderr:{r.stderr}"


@smoke
def test_csb_persist_promotes_to_host_overlay(smoke_env):
    host_overlay = smoke_env.config_dir / "home" / "notes.txt"

    # Phase A: write file — must NOT appear in host overlay yet
    r = smoke_env.run("--", "sh", "-c", "echo v1 > ~/notes.txt")
    assert r.returncode == 0, r.stderr
    assert (
        not host_overlay.exists()
    ), "notes.txt already in host overlay before csb-persist"

    # Phase B: promote via csb-persist — must appear in host overlay
    r = smoke_env.run(
        "--", "sh", "-c", "echo v1 > ~/notes.txt && csb-persist ~/notes.txt"
    )
    assert (
        r.returncode == 0
    ), f"csb-persist failed\nstdout:{r.stdout}\nstderr:{r.stderr}"
    assert host_overlay.exists(), "notes.txt not in host overlay after csb-persist"
    assert "v1" in host_overlay.read_text()

    # Phase C: new csb session — ~/notes.txt must be a symlink to /mnt/csb-home/notes.txt
    r = smoke_env.run("--", "sh", "-c", "readlink ~/notes.txt")
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == "/mnt/csb-home/notes.txt"


@smoke
@host_exec
def test_host_exec_echo(smoke_env):
    """csb-host-run forwards args and returns stdout end-to-end."""
    r = smoke_env.run(
        "--host-exec", "--host-exec-allow", "echo **",
        "--", "csb-host-run", "echo", "hello", "from", "host",
    )
    assert r.returncode == 0, r.stderr
    assert "hello from host" in r.stdout


@smoke
@host_exec
def test_host_exec_exit_code(smoke_env):
    """Non-zero exit code propagates from the host process back through csb."""
    r = smoke_env.run(
        "--host-exec", "--host-exec-allow", "sh -c *",
        "--", "csb-host-run", "sh", "-c", "exit 7",
    )
    assert r.returncode == 7, f"expected 7, got {r.returncode}\nstderr: {r.stderr}"


@smoke
@host_exec
def test_host_exec_stdin(smoke_env):
    """Stdin piped inside the container reaches the host process via the broker."""
    r = smoke_env.run(
        "--host-exec", "--host-exec-allow", "cat",
        "--", "sh", "-c", "printf 'ping' | csb-host-run cat",
    )
    assert r.returncode == 0, r.stderr
    assert "ping" in r.stdout


@smoke
@host_exec
def test_host_exec_denial(smoke_env):
    """Commands not in the allowlist are denied with exit code 126."""
    r = smoke_env.run(
        "--host-exec", "--host-exec-allow", "echo **",
        "--", "csb-host-run", "date", "--iso-8601",
    )
    assert r.returncode == 126, f"expected 126, got {r.returncode}"


@smoke
@host_exec
def test_host_exec_env_scrubbed(smoke_env):
    """Env vars injected into the sandbox do not leak to the host process."""
    r = smoke_env.run(
        "--host-exec", "--host-exec-allow", "sh -c *",
        "--env", "GIT_SSH_COMMAND=evil",
        "--", "csb-host-run", "sh", "-c", 'echo "${GIT_SSH_COMMAND:-empty}"',
    )
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == "empty", (
        f"GIT_SSH_COMMAND leaked to host process: {r.stdout.strip()!r}"
    )


@smoke
def test_nested_podman_in_csb(smoke_env):
    """Run container inside csb using nested podman.  Skipped when the outer runtime
    cannot support nested containers (rootless podman-in-Docker, no /dev/fuse,
    etc.)."""
    uid = uuid4().hex[:8]
    outer_image = f"csb-smoke-nested:{uid}"
    outer_vol = f"csb-home-smoke-nested-{uid}"

    def run(*args: str, timeout: int = 300):
        return smoke_env.run(
            "--nested-podman",
            *args,
            timeout=timeout,
            CSB_IMAGE=outer_image,
            CSB_HOME_VOLUME=outer_vol,
        )

    try:
        probe = run("--", "podman", "info", timeout=120)
        if probe.returncode != 0:
            pytest.skip(
                f"nested podman not functional in this environment: {probe.stderr[:300]}"
            )

        r = run("--", "podman", "run", "--rm", "hello-world")
        assert (
            r.returncode == 0
        ), f"podman-in-csb failed\nstdout:{r.stdout}\nstderr:{r.stderr}"
        assert "Hello Podman World" in r.stdout
    finally:
        runtime = smoke_env.runtime
        subprocess.run(
            [runtime, "rmi", "-f", outer_image],
            capture_output=True,
            env=smoke_env.base_env,
        )
        subprocess.run(
            [runtime, "volume", "rm", "-f", outer_vol],
            capture_output=True,
            env=smoke_env.base_env,
        )
