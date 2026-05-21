"""Integration tests — black-box CLI contract for csb.

These verify the full feature surface via subprocess invocation so the same
assertions can validate a future Go rewrite without modification.

Marks:
  integration      — parent mark for all tests in this file
  integration_live — subset that runs real containers (slow)

Run fast-only (no containers):
  pytest -m 'integration and not integration_live' -v

Run all integration tests:
  pytest -m integration -v
"""

from __future__ import annotations

import os
import subprocess
import sys
import types
from pathlib import Path
from uuid import uuid4

import pytest

SCRIPT = str(Path(__file__).parent.parent.parent / "csb")
CONTAINER_HOME = "/home/sandbox"
CONTAINER_WORKDIR = "/workspace"


def _setup_podman_vfs(home: Path) -> None:
    """Write the vfs storage.conf so rootless podman works inside Docker."""
    if _RUNTIME == "podman":
        cfg_dir = home / ".config" / "containers"
        cfg_dir.mkdir(parents=True, exist_ok=True)
        (cfg_dir / "storage.conf").write_text('[storage]\ndriver = "vfs"\n')

integration = pytest.mark.integration
live = pytest.mark.integration_live


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
# Session fixture — shared pre-built image for all live container tests
# ---------------------------------------------------------------------------


@pytest.fixture(scope="session")
def integ_env(tmp_path_factory):
    """Isolated environment with a pre-built csb image; shared across live tests."""
    if _RUNTIME is None:
        pytest.skip("no container runtime (docker/podman) available")

    uid = uuid4().hex[:8]
    image = f"csb-integ:{uid}"
    home_vol = f"csb-home-integ-{uid}"

    root = tmp_path_factory.mktemp("integ")
    home = root / "home"
    config_dir = home / ".config" / "csb"
    workspace = home / "dev" / "integ"
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

    if _RUNTIME == "podman":
        containers_cfg = home / ".config" / "containers"
        containers_cfg.mkdir(parents=True, exist_ok=True)
        (containers_cfg / "storage.conf").write_text('[storage]\ndriver = "vfs"\n')

    def run(
        *args: str,
        cwd: Path | None = None,
        timeout: int = 120,
        stdin: str | None = None,
        **extra_env: str,
    ):
        env = {**base_env, **extra_env}
        return subprocess.run(
            [sys.executable, SCRIPT, "--no-tmux", "--no-tty", *args],
            cwd=str(cwd or workspace),
            env=env,
            capture_output=True,
            text=True,
            timeout=timeout,
            input=stdin,
        )

    _build = run("--", "true", timeout=600)
    if _build.returncode != 0:
        pytest.skip(f"Failed to build integration image: {_build.stderr[:300]}")

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
    subprocess.run([_RUNTIME, "volume", "rm", "-f", home_vol], capture_output=True, env=base_env)
    if _RUNTIME == "podman":
        storage = home / ".local" / "share" / "containers" / "storage"
        subprocess.run(
            [_RUNTIME, "unshare", "rm", "-rf", str(storage)],
            capture_output=True,
            env=base_env,
        )


# ---------------------------------------------------------------------------
# Function fixture — fresh isolated env for non-container / subcommand tests
# ---------------------------------------------------------------------------


@pytest.fixture
def fresh_env(tmp_path):
    """Per-test isolated environment; no image pre-built."""
    if _RUNTIME is None:
        pytest.skip("no container runtime (docker/podman) available")

    home = tmp_path / "home"
    workspace = home / "dev" / "project"
    workspace.mkdir(parents=True)
    config_dir = home / ".config" / "csb"

    base_env = {
        **os.environ,
        "HOME": str(home),
        "CSB_CONFIG_DIR": str(config_dir),
        "CSB_RUNTIME": _RUNTIME,
    }
    base_env.pop("CSB_ENV", None)
    base_env.pop("CSB_IMAGE", None)

    if _RUNTIME == "podman":
        containers_cfg = home / ".config" / "containers"
        containers_cfg.mkdir(parents=True, exist_ok=True)
        (containers_cfg / "storage.conf").write_text('[storage]\ndriver = "vfs"\n')

    def run(
        *args: str,
        cwd: Path | None = None,
        timeout: int = 30,
        stdin: str | None = None,
        **extra_env: str,
    ):
        env = {**base_env, **extra_env}
        return subprocess.run(
            [sys.executable, SCRIPT, *args],
            cwd=str(cwd or workspace),
            env=env,
            capture_output=True,
            text=True,
            timeout=timeout,
            input=stdin,
        )

    yield types.SimpleNamespace(
        home=home,
        config_dir=config_dir,
        workspace=workspace,
        runtime=_RUNTIME,
        run=run,
        base_env=base_env,
    )


# ---------------------------------------------------------------------------
# A. Env injection and forwarding
# ---------------------------------------------------------------------------


@integration
@live
def test_env_inject_via_flag(integ_env):
    """--env KEY=VAL injects the variable into the container environment."""
    r = integ_env.run("--env", "CSB_INTEG_VAR=hello_flag", "--", "sh", "-c", "echo $CSB_INTEG_VAR")
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == "hello_flag"


@integration
@live
def test_env_inject_via_csb_env(integ_env):
    """CSB_ENV=KEY=VAL injects the variable into the container environment."""
    r = integ_env.run(
        "--", "sh", "-c", "echo $CSB_INTEG_VAR",
        CSB_ENV="CSB_INTEG_VAR=hello_env",
    )
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == "hello_env"


@integration
@live
def test_env_forward_via_flag(integ_env):
    """--env-forward NAME passes the named host env var value into the container."""
    r = integ_env.run(
        "--env-forward", "CSB_INTEG_TOKEN",
        "--", "sh", "-c", "echo $CSB_INTEG_TOKEN",
        CSB_INTEG_TOKEN="forwarded_secret",
    )
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == "forwarded_secret"


@integration
@live
def test_env_not_forwarded_without_flag(integ_env):
    """A host env var not listed in --env-forward is absent in the container."""
    r = integ_env.run(
        "--", "sh", "-c", 'echo "${CSB_INTEG_UNLISTED:-absent}"',
        CSB_INTEG_UNLISTED="should_not_appear",
    )
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == "absent"


@integration
@live
def test_env_inject_multiple_pairs(integ_env):
    """Multiple --env flags inject all pairs into the container."""
    r = integ_env.run(
        "--env", "CSB_INTEG_A=alpha",
        "--env", "CSB_INTEG_B=beta",
        "--", "sh", "-c", "echo $CSB_INTEG_A $CSB_INTEG_B",
    )
    assert r.returncode == 0, r.stderr
    assert "alpha" in r.stdout
    assert "beta" in r.stdout


@integration
@live
def test_env_inject_via_yaml(integ_env, tmp_path):
    """env: key in config.yaml injects vars into the container."""
    # Use the session HOME so podman storage is shared (pre-built image is reused).
    config_dir = tmp_path / "csb-config"
    config_dir.mkdir(parents=True)
    (config_dir / "config.yaml").write_text("env:\n  - CSB_INTEG_YAML_VAR=from_yaml\n")

    r = integ_env.run(
        "--", "sh", "-c", "echo $CSB_INTEG_YAML_VAR",
        CSB_CONFIG_DIR=str(config_dir),
    )
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == "from_yaml"


# ---------------------------------------------------------------------------
# B. Bind mounts
# ---------------------------------------------------------------------------


@integration
@live
def test_extra_mount_file_visible_in_container(integ_env, tmp_path):
    """--mount src:dst makes host files accessible inside the container."""
    src = tmp_path / "shared"
    src.mkdir()
    (src / "data.txt").write_text("mount-content")

    r = integ_env.run("--mount", f"{src}:/mnt/shared", "--", "cat", "/mnt/shared/data.txt")
    assert r.returncode == 0, r.stderr
    assert "mount-content" in r.stdout


@integration
@live
def test_extra_mount_rw_writes_appear_on_host(integ_env, tmp_path):
    """--mount src:dst:rw allows writing from inside the container to the host."""
    src = tmp_path / "writable"
    src.mkdir()

    r = integ_env.run(
        "--mount", f"{src}:/mnt/writable:rw",
        "--", "sh", "-c", "echo written > /mnt/writable/out.txt",
    )
    assert r.returncode == 0, r.stderr
    assert (src / "out.txt").read_text().strip() == "written"


@integration
@live
def test_workspace_flag_mounts_specified_dir(integ_env, tmp_path):
    """--workspace /other/path mounts that path as the container working directory."""
    other = tmp_path / "other-project"
    other.mkdir()
    (other / "marker.txt").write_text("other-project-content")

    r = integ_env.run("--workspace", str(other), "--", "cat", "marker.txt")
    assert r.returncode == 0, r.stderr
    assert "other-project-content" in r.stdout


# ---------------------------------------------------------------------------
# C. Workspace behavior
# ---------------------------------------------------------------------------


@integration
@live
def test_no_workspace_writes_are_ephemeral(integ_env, tmp_path):
    """--no-workspace: writes to /workspace inside the container don't appear on host."""
    sentinel = tmp_path / "should_not_exist.txt"
    r = integ_env.run(
        "--no-workspace",
        "--", "sh", "-c", f"echo ephemeral > {CONTAINER_WORKDIR}/should_not_exist.txt",
        cwd=tmp_path,
    )
    assert r.returncode == 0, r.stderr
    assert not sentinel.exists(), "File written in --no-workspace container appeared on host"


@integration
@live
def test_workspace_dir_is_cwd_inside_container(integ_env):
    """The host workspace directory is the container's working directory."""
    r = integ_env.run("--", "pwd")
    assert r.returncode == 0, r.stderr
    # pwd output should be the mirrored container path of the host workspace.
    assert CONTAINER_WORKDIR in r.stdout.strip()


@integration
@live
def test_exit_code_propagation(integ_env):
    """Non-zero exit codes from container commands propagate back to the caller."""
    for code in (1, 2, 13, 42, 127):
        r = integ_env.run("--", "sh", "-c", f"exit {code}")
        assert r.returncode == code, f"Expected exit {code}, got {r.returncode}"


# ---------------------------------------------------------------------------
# D. Home volume
# ---------------------------------------------------------------------------


@integration
@live
def test_reset_home_clears_previous_data(integ_env):
    """--reset-home wipes the home volume so prior session data is gone."""
    uid = uuid4().hex[:8]
    reset_vol = f"csb-home-reset-{uid}"
    try:
        r = integ_env.run(
            "--", "sh", "-c", "echo sentinel > ~/reset_marker.txt",
            CSB_HOME_VOLUME=reset_vol,
        )
        assert r.returncode == 0, r.stderr

        r = integ_env.run(
            "--reset-home",
            "--", "sh", "-c", 'test -f ~/reset_marker.txt && echo found || echo gone',
            CSB_HOME_VOLUME=reset_vol,
        )
        assert r.returncode == 0, r.stderr
        assert r.stdout.strip() == "gone", f"Home data survived reset: {r.stdout!r}"
    finally:
        subprocess.run(
            [_RUNTIME, "volume", "rm", "-f", reset_vol],
            capture_output=True,
            env=integ_env.base_env,
        )


# ---------------------------------------------------------------------------
# E. Image caching
# ---------------------------------------------------------------------------


@integration
@live
def test_image_not_rebuilt_on_second_run(integ_env):
    """Running the same config twice does not trigger a rebuild."""
    r = integ_env.run("--verbose", "--", "true")
    assert r.returncode == 0, r.stderr
    assert "Building" not in r.stderr, f"Unexpected rebuild:\n{r.stderr[:500]}"


@integration
@live
def test_rebuild_flag_forces_rebuild(integ_env):
    """--rebuild always triggers an image build even when the image already exists."""
    r = integ_env.run("--rebuild", "--verbose", "--", "true")
    assert r.returncode == 0, r.stderr
    # "Building <image>..." is printed to stdout by the Python runtime
    output = r.stdout + r.stderr
    assert "Building" in output, "Expected 'Building ...' in output after --rebuild"


@integration
@live
def test_different_base_image_uses_different_image_name(integ_env):
    """Changing --base-image results in a different content-addressed image name."""
    uid = uuid4().hex[:8]
    alt_image = f"csb-integ-alt:{uid}"
    try:
        r = integ_env.run(
            "--base-image", "ubuntu:24.04",
            "--", "cat", "/etc/os-release",
            CSB_IMAGE=alt_image,
            timeout=600,
        )
        assert r.returncode == 0, r.stderr
        assert "Ubuntu" in r.stdout, "Expected Ubuntu in /etc/os-release for ubuntu:24.04"
    finally:
        subprocess.run([_RUNTIME, "rmi", "-f", alt_image], capture_output=True, env=integ_env.base_env)


# ---------------------------------------------------------------------------
# F. csb clean subcommand
# ---------------------------------------------------------------------------


@integration
def test_clean_lists_home_volume_even_when_unlabeled(fresh_env):
    """csb clean always lists the configured home volume even if it carries no label."""
    uid = uuid4().hex[:8]
    vol = f"csb-home-nonexistent-{uid}"
    r = fresh_env.run("clean", stdin="n\n", CSB_HOME_VOLUME=vol)
    output = r.stdout + r.stderr
    # The current home_volume is always appended to the removal list so it appears
    # in the prompt output regardless of whether it was created by csb.
    assert vol in output, f"Expected volume name '{vol}' in clean output, got:\n{output}"


@integration
@live
def test_clean_prompts_and_lists_items(integ_env):
    """csb clean lists images and volumes before prompting for confirmation."""
    r = subprocess.run(
        [sys.executable, SCRIPT, "clean"],
        cwd=str(integ_env.workspace),
        env=integ_env.base_env,
        capture_output=True,
        text=True,
        timeout=30,
        input="n\n",
    )
    output = r.stdout + r.stderr
    assert "Remove all" in output or "Images" in output or "Volumes" in output, (
        f"Expected prompt listing items, got:\n{output}"
    )


@integration
@live
def test_clean_aborts_on_no(integ_env):
    """csb clean does not remove the image when user answers 'n'."""
    r = subprocess.run(
        [sys.executable, SCRIPT, "clean"],
        cwd=str(integ_env.workspace),
        env=integ_env.base_env,
        capture_output=True,
        text=True,
        timeout=30,
        input="n\n",
    )
    assert "Aborted" in r.stdout or r.returncode != 0, (
        f"Expected 'Aborted' or non-zero exit, got: stdout={r.stdout!r} rc={r.returncode}"
    )
    # Verify the pre-built session image was not removed.
    check = subprocess.run(
        [_RUNTIME, "image", "inspect", integ_env.image],
        capture_output=True,
        env=integ_env.base_env,
    )
    assert check.returncode == 0, f"Session image was removed despite answering 'n'"


# ---------------------------------------------------------------------------
# G. csb config-edit subcommand
# ---------------------------------------------------------------------------


@integration
def test_config_edit_user_creates_config_file(fresh_env):
    """csb config-edit user creates the user config.yaml when it does not exist."""
    config_yaml = fresh_env.config_dir / "config.yaml"
    assert not config_yaml.exists()

    r = fresh_env.run("config-edit", "--no-workspace", "user", EDITOR="true", VISUAL="true")
    assert r.returncode == 0, r.stderr
    assert config_yaml.exists(), "config.yaml not created by 'config-edit user'"


@integration
def test_config_edit_user_does_not_overwrite_existing(fresh_env):
    """csb config-edit user does not overwrite an existing config.yaml."""
    config_dir = fresh_env.config_dir
    config_dir.mkdir(parents=True, exist_ok=True)
    config_yaml = config_dir / "config.yaml"
    config_yaml.write_text("# my custom config\ntmux: false\n")

    r = fresh_env.run("config-edit", "--no-workspace", "user", EDITOR="true", VISUAL="true")
    assert r.returncode == 0, r.stderr
    assert config_yaml.read_text() == "# my custom config\ntmux: false\n", (
        "Existing config.yaml was overwritten by 'config-edit user'"
    )


@integration
def test_config_edit_workdir_creates_project_yaml(fresh_env):
    """csb config-edit workdir creates a per-workdir yaml file under projects/."""
    r = fresh_env.run("config-edit", "workdir", EDITOR="true", VISUAL="true")
    assert r.returncode == 0, r.stderr

    proj_dir = fresh_env.config_dir / "projects"
    yaml_files = list(proj_dir.glob("*.yaml")) if proj_dir.exists() else []
    assert len(yaml_files) == 1, f"Expected 1 workdir config, found: {yaml_files}"


@integration
def test_config_edit_workdir_stable_path(fresh_env):
    """Running config-edit workdir twice for the same workdir touches the same file."""
    r1 = fresh_env.run("config-edit", "workdir", EDITOR="true", VISUAL="true")
    r2 = fresh_env.run("config-edit", "workdir", EDITOR="true", VISUAL="true")
    assert r1.returncode == 0, r1.stderr
    assert r2.returncode == 0, r2.stderr

    proj_dir = fresh_env.config_dir / "projects"
    yaml_files = list(proj_dir.glob("*.yaml")) if proj_dir.exists() else []
    assert len(yaml_files) == 1, f"Expected 1 workdir config (stable path), found: {yaml_files}"


@integration
def test_config_edit_workdir_requires_workspace(fresh_env):
    """csb config-edit workdir fails with --no-workspace since there is no workdir."""
    r = fresh_env.run(
        "config-edit", "--no-workspace", "workdir",
        EDITOR="true", VISUAL="true",
    )
    assert r.returncode != 0, "Expected non-zero exit for 'config-edit --no-workspace workdir'"


# ---------------------------------------------------------------------------
# H. Config layer precedence via subprocess
# ---------------------------------------------------------------------------


@integration
@live
def test_cli_flag_overrides_yaml_for_env(integ_env, tmp_path):
    """CLI --env flag replaces the YAML env list (CLI wins, no merging)."""
    config_dir = tmp_path / "csb-config"
    config_dir.mkdir(parents=True)
    (config_dir / "config.yaml").write_text("env:\n  - CSB_LAYER=from_yaml\n")

    r = integ_env.run(
        "--env", "CSB_LAYER=from_flag",
        "--", "sh", "-c", "echo $CSB_LAYER",
        CSB_CONFIG_DIR=str(config_dir),
    )
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == "from_flag", (
        f"Expected CLI --env to override YAML env, got: {r.stdout.strip()!r}"
    )


@integration
@live
def test_csb_env_var_beats_yaml_for_home_volume(integ_env, tmp_path):
    """CSB_HOME_VOLUME env var takes precedence over home_volume: in config.yaml."""
    uid = uuid4().hex[:8]
    config_dir = tmp_path / "csb-config"
    config_dir.mkdir(parents=True)
    override_vol = f"csb-home-override-{uid}"
    (config_dir / "config.yaml").write_text("home_volume: csb-home-should-not-use\n")

    try:
        r = integ_env.run(
            "--", "true",
            CSB_CONFIG_DIR=str(config_dir),
            CSB_HOME_VOLUME=override_vol,
        )
        assert r.returncode == 0, r.stderr
        check = subprocess.run(
            [_RUNTIME, "volume", "inspect", override_vol],
            capture_output=True,
            env=integ_env.base_env,
        )
        assert check.returncode == 0, f"Expected volume '{override_vol}' to be created by CSB_HOME_VOLUME override"
    finally:
        subprocess.run([_RUNTIME, "volume", "rm", "-f", override_vol], capture_output=True, env=integ_env.base_env)
        subprocess.run([_RUNTIME, "volume", "rm", "-f", "csb-home-should-not-use"], capture_output=True, env=integ_env.base_env)


@integration
@live
def test_workdir_yaml_overrides_user_yaml_in_container(integ_env, tmp_path):
    """Per-workdir config.yaml overrides user config.yaml for the matching key."""
    from .config import _workdir_config_path

    workspace = tmp_path / "myproject"
    workspace.mkdir(parents=True)
    config_dir = tmp_path / "csb-config"
    config_dir.mkdir(parents=True)
    (config_dir / "config.yaml").write_text("env:\n  - CSB_LAYER_SOURCE=user_yaml\n")

    workdir_cfg = _workdir_config_path(config_dir, workspace)
    workdir_cfg.parent.mkdir(parents=True, exist_ok=True)
    workdir_cfg.write_text("env:\n  - CSB_LAYER_SOURCE=workdir_yaml\n")

    r = integ_env.run(
        "--", "sh", "-c", "echo $CSB_LAYER_SOURCE",
        cwd=workspace,
        CSB_CONFIG_DIR=str(config_dir),
    )
    assert r.returncode == 0, r.stderr
    assert r.stdout.strip() == "workdir_yaml", (
        f"Expected workdir_yaml to override user_yaml, got: {r.stdout.strip()!r}"
    )
