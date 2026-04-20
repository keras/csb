from __future__ import annotations

from pathlib import Path

import pytest

from .config import (
    CSB_DEFAULT_FILES,
    Config,
    OPTIONS,
    _format_help_full,
    _init_config_dir,
    parse_args,
)


class TestParseArgs:
    def test_all_flags(self):
        args = [
            "--rebuild",
            "--no-tmux",
            "-v",
            "--",
            "echo",
            "hello",
        ]
        cfg = parse_args(args)
        assert cfg.rebuild is True
        assert cfg.use_tmux is False
        assert cfg.verbose is True
        assert cfg.passthrough_args == ["echo", "hello"]

    def test_config_yaml_sets_tmux_tty_defaults(self, tmp_path, monkeypatch):
        home = tmp_path / "home"
        config_dir = home / ".config" / "csb"
        config_dir.mkdir(parents=True)
        (config_dir / "config.yaml").write_text("tmux: false\ntty: false\n")
        monkeypatch.setattr(Path, "home", staticmethod(lambda: home))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: home))

        cfg = parse_args([])
        assert cfg.use_tmux is False
        assert cfg.use_tty is False

    def test_cli_flag_overrides_config_yaml(self, tmp_path, monkeypatch):
        home = tmp_path / "home"
        config_dir = home / ".config" / "csb"
        config_dir.mkdir(parents=True)
        (config_dir / "config.yaml").write_text("tmux: false\ntty: false\n")
        monkeypatch.setattr(Path, "home", staticmethod(lambda: home))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: home))

        cfg = parse_args(["--tmux", "--tty"])
        assert cfg.use_tmux is True
        assert cfg.use_tty is True


class TestInitConfigDir:
    def test_creates_config_dir_when_absent(self, tmp_path):
        config_dir = tmp_path / "csb"
        _init_config_dir(config_dir)
        assert config_dir.is_dir()

    def test_creates_all_default_files(self, tmp_path):
        config_dir = tmp_path / "csb"
        _init_config_dir(config_dir)
        for rel in CSB_DEFAULT_FILES:
            assert (config_dir / rel).exists(), f"missing: {rel}"

    def test_config_yaml_is_all_comments(self, tmp_path):
        config_dir = tmp_path / "csb"
        _init_config_dir(config_dir)
        text = (config_dir / "config.yaml").read_text()
        non_comment = [
            line for line in text.splitlines() if line and not line.startswith("#")
        ]
        assert non_comment == [], f"unexpected non-comment lines: {non_comment}"

    def test_does_not_overwrite_existing_config_dir(self, tmp_path):
        config_dir = tmp_path / "csb"
        config_dir.mkdir(parents=True)
        sentinel = config_dir / "config.yaml"
        sentinel.write_text("tmux: true\n")
        _init_config_dir(config_dir)
        assert sentinel.read_text() == "tmux: true\n"


class TestConfigDirOverride:
    """config_dir can be set via --config-dir flag or CSB_CONFIG_DIR env var."""

    def test_flag_overrides_default(self, tmp_path, monkeypatch):
        monkeypatch.setattr(Path, "home", staticmethod(lambda: tmp_path / "home"))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        alt = tmp_path / "alt-config"
        cfg = parse_args(
            ["--config-dir", str(alt), "--no-workspace", "--no-tmux", "--no-tty"]
        )
        assert cfg.config_dir == alt.resolve()

    def test_env_var_overrides_default(self, tmp_path, monkeypatch):
        monkeypatch.setattr(Path, "home", staticmethod(lambda: tmp_path / "home"))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        alt = tmp_path / "env-config"
        monkeypatch.setenv("CSB_CONFIG_DIR", str(alt))
        cfg = parse_args(["--no-workspace", "--no-tmux", "--no-tty"])
        assert cfg.config_dir == alt.resolve()

    def test_flag_takes_precedence_over_env(self, tmp_path, monkeypatch):
        monkeypatch.setattr(Path, "home", staticmethod(lambda: tmp_path / "home"))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        flag_dir = tmp_path / "flag-config"
        env_dir = tmp_path / "env-config"
        monkeypatch.setenv("CSB_CONFIG_DIR", str(env_dir))
        cfg = parse_args(
            ["--config-dir", str(flag_dir), "--no-workspace", "--no-tmux", "--no-tty"]
        )
        assert cfg.config_dir == flag_dir.resolve()

    def test_csb_home_uses_config_dir(self, tmp_path, monkeypatch):
        monkeypatch.setattr(Path, "home", staticmethod(lambda: tmp_path / "home"))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        alt = tmp_path / "alt-config"
        cfg = parse_args(
            ["--config-dir", str(alt), "--no-workspace", "--no-tmux", "--no-tty"]
        )
        assert cfg.csb_home == alt.resolve() / "home"

    def test_default_is_home_dot_config_csb(self, tmp_path, monkeypatch):
        home = tmp_path / "home"
        monkeypatch.setattr(Path, "home", staticmethod(lambda: home))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        monkeypatch.delenv("CSB_CONFIG_DIR", raising=False)
        cfg = parse_args(["--no-workspace", "--no-tmux", "--no-tty"])
        assert cfg.config_dir == home / ".config" / "csb"


class TestHomeVolumeConfig:
    """home_volume can be set via config.yaml or overridden by CSB_HOME_VOLUME."""

    def test_default_is_csb_home(self, tmp_path, monkeypatch):
        monkeypatch.setattr(Path, "home", staticmethod(lambda: tmp_path / "home"))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        monkeypatch.delenv("CSB_HOME_VOLUME", raising=False)
        cfg = parse_args(["--no-workspace", "--no-tmux", "--no-tty"])
        assert cfg.home_volume == "csb-home"

    def test_yaml_sets_home_volume(self, tmp_path, monkeypatch):
        home = tmp_path / "home"
        monkeypatch.setattr(Path, "home", staticmethod(lambda: home))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        monkeypatch.delenv("CSB_HOME_VOLUME", raising=False)
        config_dir = home / ".config" / "csb"
        config_dir.mkdir(parents=True)
        (config_dir / "config.yaml").write_text("home_volume: my-vol\n")
        cfg = parse_args(["--no-workspace", "--no-tmux", "--no-tty"])
        assert cfg.home_volume == "my-vol"

    def test_env_overrides_yaml(self, tmp_path, monkeypatch):
        home = tmp_path / "home"
        monkeypatch.setattr(Path, "home", staticmethod(lambda: home))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        monkeypatch.setenv("CSB_HOME_VOLUME", "env-vol")
        config_dir = home / ".config" / "csb"
        config_dir.mkdir(parents=True)
        (config_dir / "config.yaml").write_text("home_volume: yaml-vol\n")
        cfg = parse_args(["--no-workspace", "--no-tmux", "--no-tty"])
        assert cfg.home_volume == "env-vol"


class TestEnvInjectConfig:
    def test_env_inject_via_flag(self, tmp_path, monkeypatch):
        monkeypatch.setattr(Path, "home", staticmethod(lambda: tmp_path / "home"))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        cfg = parse_args(["--no-workspace", "--no-tmux", "--no-tty", "--env", "FOO=bar"])
        assert "FOO=bar" in cfg.env_inject

    def test_env_inject_via_csb_env(self, tmp_path, monkeypatch):
        monkeypatch.setattr(Path, "home", staticmethod(lambda: tmp_path / "home"))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        monkeypatch.setenv("CSB_ENV", "MY_VAR=hello")
        cfg = parse_args(["--no-workspace", "--no-tmux", "--no-tty"])
        assert "MY_VAR=hello" in cfg.env_inject

    def test_env_inject_requires_equals(self, tmp_path, monkeypatch):
        monkeypatch.setattr(Path, "home", staticmethod(lambda: tmp_path / "home"))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        with pytest.raises(SystemExit):
            parse_args(["--no-workspace", "--no-tmux", "--no-tty", "--env", "NOEQUALS"])


class TestHelpFull:
    def test_help_full_exits(self, tmp_path, monkeypatch):
        monkeypatch.setattr(Path, "home", staticmethod(lambda: tmp_path / "home"))
        monkeypatch.setattr(Path, "cwd", staticmethod(lambda: tmp_path))
        with pytest.raises(SystemExit) as exc_info:
            parse_args(["--help-full"])
        assert exc_info.value.code == 0

    def test_format_help_full_contains_all_options(self):
        output = _format_help_full()
        for spec in OPTIONS:
            if spec.flag:
                assert spec.flag in output
            if spec.env:
                assert spec.env in output
            if spec.yaml_key:
                assert ".".join(spec.yaml_key) in output

    def test_format_help_full_contains_yaml_example(self):
        output = _format_help_full()
        assert "EXAMPLE config.yaml" in output
        assert "# csb configuration" in output
