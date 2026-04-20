from __future__ import annotations

import os
import subprocess
import sys
import types
from pathlib import Path

import pytest

from .config import CONTAINER_HOME, CONTAINER_WORKDIR, Config
from .container import _build_context_tar

SCRIPT = str(Path(__file__).parent.parent.parent / "csb")


def _runtime_available(cli: str) -> bool:
    try:
        return subprocess.run([cli, "info"], capture_output=True).returncode == 0
    except FileNotFoundError:
        return False


_available_runtimes = {cli for cli in ("docker", "podman") if _runtime_available(cli)}

docker = pytest.mark.docker
podman = pytest.mark.podman


@pytest.fixture(
    scope="class",
    params=[
        pytest.param("docker", marks=pytest.mark.docker),
        pytest.param("podman", marks=pytest.mark.podman),
    ],
)
def container_env(request, tmp_path_factory):
    """Persistent test workspace — shared across all tests in a class.

    Pre-builds the image so individual tests only run containers.
    Parametrized over docker and podman; skips whichever isn't available.
    """
    runtime = request.param
    if runtime not in _available_runtimes:
        pytest.skip(f"{runtime} not available")

    root = tmp_path_factory.mktemp("containertest")
    home = root / "home"
    workspace = home / "dev" / "test-csb"
    workspace.mkdir(parents=True)

    csb_home = home / ".config" / "csb" / "home"
    csb_home.mkdir(parents=True)
    (csb_home / ".claude").mkdir()
    (csb_home / ".claude.json").touch()

    # Rootless Podman inside Docker: the backing fs is overlayfs. Native
    # overlay-on-overlay doesn't work, and even fuse-overlayfs fails to exec
    # container processes inside a fresh storage root. VFS avoids both issues
    # by copying layers rather than using overlay mounts.
    if runtime == "podman":
        podman_cfg = home / ".config" / "containers"
        podman_cfg.mkdir(parents=True, exist_ok=True)
        (podman_cfg / "storage.conf").write_text('[storage]\ndriver = "vfs"\n')

    img = f"csb:test-{runtime}"
    home_vol = f"csb-home-test-{runtime}"
    cfg = Config(cwd=workspace, home=home)
    context = _build_context_tar(cfg)

    env = {**os.environ, "HOME": str(home)}
    build_cmd = [runtime, "build", "-t", img]
    if runtime == "podman":
        # Required when building inside Docker: Docker's seccomp profile blocks
        # clone() syscalls that Podman needs to exec RUN steps.
        build_cmd += ["--security-opt", "seccomp=unconfined"]
    build_cmd += ["-"]
    subprocess.run(build_cmd, input=context, check=True, env=env)
    subprocess.run(
        [runtime, "volume", "create", home_vol], capture_output=True, env=env
    )

    yield types.SimpleNamespace(
        home=home,
        workspace=workspace,
        csb_home=csb_home,
        image=img,
        home_volume=home_vol,
        runtime=runtime,
    )

    subprocess.run([runtime, "rmi", "-f", img], capture_output=True, env=env)
    subprocess.run(
        [runtime, "volume", "rm", "-f", home_vol], capture_output=True, env=env
    )
    if runtime == "podman":
        # Containers created by rootless podman may create files owned by a
        # remapped uid inside the user namespace. Use 'podman unshare' to
        # remove them from within that namespace so pytest's tmp_path cleanup
        # doesn't hit PermissionError when trying to delete the temp home.
        storage = home / ".local" / "share" / "containers" / "storage"
        subprocess.run(
            [runtime, "unshare", "rm", "-rf", str(storage)],
            capture_output=True,
            env=env,
        )


class TestContainerRuntime:
    """Tests that call csb as a subprocess, actually building and running the
    container. Validates entrypoint, profile.d, shim resolution, and home
    mounting against the real mise runtime. Runs for each available runtime."""

    def _run(self, container_env, args, timeout=120):
        env = os.environ.copy()
        env["HOME"] = str(container_env.home)
        env["CSB_IMAGE"] = container_env.image
        env["CSB_HOME_VOLUME"] = container_env.home_volume
        env["CSB_RUNTIME"] = container_env.runtime
        env.pop("CSB_ENV", None)
        return subprocess.run(
            [sys.executable, SCRIPT, "--no-tmux", "--no-tty", *args],
            cwd=container_env.workspace,
            env=env,
            capture_output=True,
            text=True,
            timeout=timeout,
        )

    def test_home_correct_in_login_shell(self, container_env):
        r = self._run(container_env, ["--", "bash", "-lc", "echo $HOME"])
        assert r.returncode == 0
        assert r.stdout.strip() == CONTAINER_HOME

    def test_home_correct_via_entrypoint(self, container_env):
        r = self._run(container_env, ["--", "bash", "-c", "echo $HOME"])
        assert r.returncode == 0
        assert r.stdout.strip() == CONTAINER_HOME

    def test_mise_shims_in_path(self, container_env):
        r = self._run(container_env, ["--", "bash", "-lc", "echo $PATH"])
        assert "mise/shims" in r.stdout

    def test_no_permission_errors(self, container_env):
        r = self._run(container_env, ["--", "bash", "-c", "id"])
        assert "Permission denied" not in r.stderr

    def test_exit_code_propagation(self, container_env):
        """Non-zero exit code from the command should propagate through the container."""
        r = self._run(container_env, ["--", "bash", "-c", "exit 42"])
        assert r.returncode == 42

    def test_no_workspace_workspace_writable(self, container_env):
        """--no-workspace should land in a writable /workspace."""
        r = self._run(
            container_env,
            [
                "--no-workspace",
                "--",
                "bash",
                "-c",
                f"date > {CONTAINER_WORKDIR}/test && cat {CONTAINER_WORKDIR}/test",
            ],
        )
        assert r.returncode == 0
        assert r.stdout.strip() != ""
