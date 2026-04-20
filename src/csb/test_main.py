from __future__ import annotations

import types
from pathlib import Path

import pytest

from .config import CONTAINER_HOME, CONTAINER_WORKDIR
from . import main


class FakeRuntime:
    """Recorded stand-in for Runtime used in unit/integration tests."""

    def __init__(self, cli: str = "docker") -> None:
        self.cli = cli
        self.calls: list[tuple] = []
        self._image_exists = True

    def image_exists(self, name: str) -> bool:
        self.calls.append(("image_exists", name))
        return self._image_exists

    def build_image(self, name: str, context: bytes, quiet: bool) -> None:
        self.calls.append(("build_image", name, quiet))

    def list_csb_image_ids(self) -> list[str]:
        self.calls.append(("list_csb_image_ids",))
        return ["abc123"]

    def remove_images(self, ids: list[str]) -> None:
        self.calls.append(("remove_images", ids))

    def remove_volume(self, name: str) -> None:
        self.calls.append(("remove_volume", name))

    def exec_run(self, argv: list[str]) -> None:
        self.calls.append(("exec_run", argv))


@pytest.fixture
def fake_runtime():
    return FakeRuntime()


@pytest.fixture
def sandbox(tmp_path, monkeypatch, fake_runtime):
    """Set up a fake home + workspace directory tree and inject FakeRuntime."""
    home = tmp_path / "home"
    workspace = home / "dev" / "myproject"
    workspace.mkdir(parents=True)

    csb_home = home / ".config" / "csb" / "home"
    csb_home.mkdir(parents=True)

    monkeypatch.setattr(Path, "cwd", staticmethod(lambda: workspace))
    monkeypatch.setattr(Path, "home", staticmethod(lambda: home))
    monkeypatch.setattr("os.getuid", lambda: 1000)
    monkeypatch.setattr("os.getgid", lambda: 1000)
    monkeypatch.setenv("CSB_RUNTIME", "docker")

    monkeypatch.setattr("csb.Runtime", lambda cli: fake_runtime)

    return types.SimpleNamespace(
        home=home, workspace=workspace, csb_home=csb_home, runtime=fake_runtime
    )


class TestMainIntegration:
    """Full integration tests that call main() against a temp directory."""

    def test_home_volume_mounted(self, sandbox):
        main(["--no-tmux", "--no-tty"])

        cmd = " ".join(sandbox.runtime.calls[-1][1])
        assert f"csb-home:{CONTAINER_HOME}" in cmd

    def test_csb_home_mounted_to_mnt(self, sandbox):
        (sandbox.csb_home / ".gitconfig").write_text("[user]\n  name = Test\n")
        (sandbox.csb_home / ".claude").mkdir()

        main(["--no-tmux", "--no-tty"])

        cmd = " ".join(sandbox.runtime.calls[-1][1])
        assert f"{sandbox.csb_home}:/mnt/csb-home" in cmd

    def test_workspace_dir_mounted(self, sandbox):
        main(["--no-tmux", "--no-tty"])

        cmd = " ".join(sandbox.runtime.calls[-1][1])
        assert f"{sandbox.workspace}:{CONTAINER_WORKDIR}/dev/myproject" in cmd

    def test_passthrough_args_appended(self, sandbox):
        main(["--no-tmux", "--no-tty", "--", "bash", "-c", "echo hi"])

        cmd = sandbox.runtime.calls[-1][1]
        assert cmd[-3:] == ["bash", "-c", "echo hi"]

    def test_shared_image_name(self, sandbox):
        main(["--no-tmux", "--no-tty"])

        cmd = sandbox.runtime.calls[-1][1]
        assert any(arg.startswith("csb:") and len(arg) == 16 for arg in cmd)

    def test_env_vars_forwarded(self, sandbox, monkeypatch):
        monkeypatch.setenv("CSB_ENV_FORWARD", "MY_TOKEN")
        monkeypatch.setenv("MY_TOKEN", "secret123")

        main(["--no-tmux", "--no-tty"])

        cmd = sandbox.runtime.calls[-1][1]
        assert "MY_TOKEN=secret123" in cmd

    def test_env_inject_via_env_var(self, sandbox, monkeypatch):
        monkeypatch.setenv("CSB_ENV", "MY_VAR=hello")

        main(["--no-tmux", "--no-tty"])

        cmd = sandbox.runtime.calls[-1][1]
        assert "MY_VAR=hello" in cmd

    def test_env_inject_via_flag(self, sandbox):
        main(["--no-tmux", "--no-tty", "--env", "FOO=bar"])

        cmd = sandbox.runtime.calls[-1][1]
        assert "FOO=bar" in cmd

    def test_no_workspace_no_workspace_mount(self, sandbox):
        main(["--no-tmux", "--no-tty", "--no-workspace"])

        cmd = " ".join(sandbox.runtime.calls[-1][1])
        assert f"{CONTAINER_WORKDIR}/" not in cmd
        assert f"-w {CONTAINER_WORKDIR}" in cmd

    def test_workspace_flag_overrides_cwd(self, sandbox):
        other = sandbox.home / "dev" / "other-repo"
        other.mkdir(parents=True)

        main(["--no-tmux", "--no-tty", "--workspace", str(other)])

        cmd = " ".join(sandbox.runtime.calls[-1][1])
        assert f"{other}:{CONTAINER_WORKDIR}/dev/other-repo" in cmd

    def test_rebuild_triggers_image_build(self, sandbox):
        sandbox.runtime._image_exists = False

        main(["--rebuild", "--no-tmux", "--no-tty"])

        builds = [c for c in sandbox.runtime.calls if c[0] == "build_image"]
        assert len(builds) == 1

    def test_reset_home_removes_volume(self, sandbox):
        main(["--reset-home", "--no-tmux", "--no-tty"])

        rm_calls = [c for c in sandbox.runtime.calls if c[0] == "remove_volume"]
        assert len(rm_calls) == 1
        assert rm_calls[0][1] == "csb-home"
