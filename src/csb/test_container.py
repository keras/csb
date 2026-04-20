from __future__ import annotations

import io
import tarfile
from pathlib import Path

from .config import CONTAINER_HOME, CONTAINER_WORKDIR, Config, _parse_mount
from .container import (
    _apt_packages,
    _BASE_PACKAGES,
    _build_context_tar,
    _make_dockerfile,
    _PODMAN_PACKAGES,
    _podman_snippets,
    image_name,
    resolve_mounts,
)


def _tar_members(cfg: Config) -> list[tarfile.TarInfo]:
    with tarfile.open(fileobj=io.BytesIO(_build_context_tar(cfg))) as t:
        return t.getmembers()


def _cfg(tmp_path: Path, **kwargs) -> Config:
    home = tmp_path / "home"
    home.mkdir(exist_ok=True)
    return Config(cwd=home, home=home, **kwargs)


class TestDockerfile:
    def test_has_entrypoint_d_mkdir(self):
        df = _make_dockerfile("debian:stable-slim", nested_podman=False)
        assert "mkdir -p /etc/csb/entrypoint.d" in df

    def test_has_build_d_run_step(self):
        df = _make_dockerfile("debian:stable-slim", nested_podman=False)
        assert "csb/build.d" in df

    def test_base_image_in_from(self):
        df = _make_dockerfile("ubuntu:24.04", nested_podman=False)
        assert df.startswith("FROM ubuntu:24.04")

    def test_no_direct_mise_references(self):
        df = _make_dockerfile("debian:stable-slim", nested_podman=False)
        assert "mise.run" not in df
        assert "MISE_DATA_DIR" not in df


class TestBuildContext:
    def test_build_d_dir_always_present(self, tmp_path):
        cfg = _cfg(tmp_path, addons=[])
        names = [m.name for m in _tar_members(cfg)]
        assert "csb/build.d" in names

    def test_mise_addon_in_tar_when_enabled(self, tmp_path):
        cfg = _cfg(tmp_path, addons=["mise"])
        names = [m.name for m in _tar_members(cfg)]
        assert "csb/build.d/mise.sh" in names

    def test_mise_addon_absent_when_disabled(self, tmp_path):
        cfg = _cfg(tmp_path, addons=[])
        names = [m.name for m in _tar_members(cfg)]
        assert "csb/build.d/mise.sh" not in names

    def test_mise_addon_is_executable(self, tmp_path):
        cfg = _cfg(tmp_path, addons=["mise"])
        members = {m.name: m for m in _tar_members(cfg)}
        assert members["csb/build.d/mise.sh"].mode & 0o111 != 0

    def test_entrypoint_sh_in_tar(self, tmp_path):
        cfg = _cfg(tmp_path, addons=[])
        names = [m.name for m in _tar_members(cfg)]
        assert "entrypoint.sh" in names

    def test_dockerfile_in_tar(self, tmp_path):
        cfg = _cfg(tmp_path, addons=[])
        names = [m.name for m in _tar_members(cfg)]
        assert "Dockerfile" in names


class TestImageName:
    def test_image_name_differs_with_addons(self, tmp_path):
        cfg_with = _cfg(tmp_path, addons=["mise"], nested_podman=False)
        cfg_without = _cfg(tmp_path, addons=[], nested_podman=False)
        assert image_name(cfg_with) != image_name(cfg_without)

    def test_image_name_format(self, tmp_path):
        cfg = _cfg(tmp_path, addons=[])
        name = image_name(cfg)
        assert name.startswith("csb:")
        assert len(name) == len("csb:") + 12

    def test_image_override(self, tmp_path):
        cfg = _cfg(tmp_path, addons=[], image="my-custom:latest")
        assert image_name(cfg) == "my-custom:latest"


class TestDockerfileHelpers:
    def test_apt_packages_base_always_present(self):
        pkgs = _apt_packages(nested_podman=False)
        assert all(p in pkgs for p in _BASE_PACKAGES)

    def test_apt_packages_includes_podman_when_enabled(self):
        pkgs = _apt_packages(nested_podman=True)
        assert all(p in pkgs for p in _PODMAN_PACKAGES)

    def test_apt_packages_excludes_podman_when_disabled(self):
        pkgs = _apt_packages(nested_podman=False)
        assert not any(p in pkgs for p in _PODMAN_PACKAGES)

    def test_apt_packages_sorted(self):
        pkgs = _apt_packages(nested_podman=True)
        assert pkgs == sorted(pkgs)

    def test_podman_snippets_populated_when_enabled(self):
        s = _podman_snippets(nested_podman=True)
        assert "docker=podman" in s["alias"]
        assert "COPY containers" in s["config_copy"]

    def test_podman_snippets_empty_when_disabled(self):
        s = _podman_snippets(nested_podman=False)
        assert s["alias"] == "&& true"
        assert s["config_copy"] == ""


class TestResolveMounts:
    def _cfg(self, tmp_path):
        home = tmp_path / "home"
        cwd = home / "dev" / "myproject"
        cwd.mkdir(parents=True)
        csb_home = home / ".config" / "csb" / "home"
        csb_home.mkdir(parents=True)
        return Config(cwd=cwd, home=home, workspace=cwd), csb_home

    def test_csb_home_mounted_to_mnt(self, tmp_path):
        cfg, csb_home = self._cfg(tmp_path)
        (csb_home / ".gitconfig").touch()
        (csb_home / ".claude").mkdir()

        mounts = resolve_mounts(cfg)
        dsts = [m.dst for m in mounts]
        assert "/mnt/csb-home" in dsts
        assert not any(d.startswith(f"{CONTAINER_HOME}/") for d in dsts)

    def test_empty_csb_home_still_mounts_mnt(self, tmp_path):
        # /mnt/csb-home is mounted even when csb_home is empty so csb-persist
        # works from a fresh container before any files have been promoted.
        cfg, _ = self._cfg(tmp_path)
        mounts = resolve_mounts(cfg)
        assert any(m.dst == "/mnt/csb-home" for m in mounts)

    def test_csb_home_not_mounted_under_container_home(self, tmp_path):
        cfg, _ = self._cfg(tmp_path)
        mounts = resolve_mounts(cfg)
        assert not any(m.dst.startswith(CONTAINER_HOME) for m in mounts)

    def test_no_workspace_skips_workspace_mount(self, tmp_path):
        home = tmp_path / "home"
        home.mkdir(parents=True)
        csb_home = home / ".config" / "csb" / "home"
        csb_home.mkdir(parents=True)
        cfg = Config(cwd=home, home=home, workspace=None)
        mounts = resolve_mounts(cfg)
        workdir_mounts = [m for m in mounts if m.dst.startswith(f"{CONTAINER_WORKDIR}/")]
        assert len(workdir_mounts) == 0

    def test_workspace_mount_present(self, tmp_path):
        cfg, _ = self._cfg(tmp_path)
        mounts = resolve_mounts(cfg)
        workspace_mounts = [m for m in mounts if m.dst == cfg.workdir]
        assert len(workspace_mounts) == 1
        assert workspace_mounts[0].src == str(cfg.workspace)
        assert workspace_mounts[0].readonly is False

    def test_config_yaml_mount_tilde_dst(self, tmp_path):
        cfg, _ = self._cfg(tmp_path)
        gitconfig = Path.home() / ".gitconfig"
        cfg.mount = [_parse_mount(f"{gitconfig}:~/.gitconfig")]

        mounts = resolve_mounts(cfg)
        explicit = [m for m in mounts if m.dst == f"{CONTAINER_HOME}/.gitconfig"]
        assert len(explicit) == 1
        assert explicit[0].src == str(gitconfig)
        assert explicit[0].readonly is True

    def test_config_yaml_mount_explicit_ro(self, tmp_path):
        cfg, _ = self._cfg(tmp_path)
        gitconfig = Path.home() / ".gitconfig"
        cfg.mount = [_parse_mount(f"{gitconfig}:~/.gitconfig:ro")]

        mounts = resolve_mounts(cfg)
        explicit = [m for m in mounts if m.dst == f"{CONTAINER_HOME}/.gitconfig"]
        assert len(explicit) == 1
        assert explicit[0].readonly is True

    def test_config_yaml_mount_explicit_rw(self, tmp_path):
        cfg, _ = self._cfg(tmp_path)
        gitconfig = Path.home() / ".gitconfig"
        cfg.mount = [_parse_mount(f"{gitconfig}:~/.gitconfig:rw")]

        mounts = resolve_mounts(cfg)
        explicit = [m for m in mounts if m.dst == f"{CONTAINER_HOME}/.gitconfig"]
        assert len(explicit) == 1
        assert explicit[0].readonly is False

    def test_config_yaml_mount_absolute_dst(self, tmp_path):
        cfg, _ = self._cfg(tmp_path)
        src = cfg.home / ".ssh"
        src.mkdir()
        cfg.mount = [_parse_mount(f"{src}:/home/sandbox/.ssh")]

        mounts = resolve_mounts(cfg)
        explicit = [m for m in mounts if m.dst == "/home/sandbox/.ssh"]
        assert len(explicit) == 1
        assert explicit[0].src == str(src)

    def test_no_extra_mounts_when_mount_empty(self, tmp_path):
        cfg, _ = self._cfg(tmp_path)
        mounts = resolve_mounts(cfg)
        explicit = [m for m in mounts if m.dst.startswith(CONTAINER_HOME + "/")]
        assert len(explicit) == 0

    def test_empty_mount_list_no_extra_mounts(self, tmp_path):
        cfg, _ = self._cfg(tmp_path)
        cfg.mount = []
        mounts = resolve_mounts(cfg)
        explicit = [m for m in mounts if m.dst.startswith(CONTAINER_HOME + "/")]
        assert len(explicit) == 0
