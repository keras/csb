from __future__ import annotations

import argparse
import os
import re
import shutil
import sys
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml


CONTAINER_HOME = "/home/sandbox"
CONTAINER_WORKDIR = "/workspace"


def _resolve_runtime(runtime: str) -> str:
    """Return the container CLI to use ('docker' or 'podman').

    'auto' probes PATH and picks docker first, then podman.
    An explicit value is returned as-is.
    """
    if runtime != "auto":
        return runtime
    for candidate in ("docker", "podman"):
        if shutil.which(candidate):
            return candidate
    return "docker"  # let it fail naturally with a clear error


# Sentinels -----------------------------------------------------------------

_UNSET: Any = object()  # CLI flag not supplied
_MISSING: Any = object()  # yaml key not present


# Mount ---------------------------------------------------------------------


@dataclass
class Mount:
    """A single container bind mount."""

    src: str
    dst: str
    readonly: bool = True

    def to_args(self) -> list[str]:
        spec = f"{self.src}:{self.dst}"
        if self.readonly:
            spec += ":ro"
        return ["-v", spec]


# Validators ----------------------------------------------------------------

_ADDONS_DIR = Path(__file__).parent / "addons"


def _parse_mount(entry: str) -> Mount:
    """Parse a 'src:dst[:mode]' mount string. ~ expands to host home (src) or CONTAINER_HOME (dst).
    mode is 'ro' (default) or 'rw'."""
    parts = entry.split(":")
    if len(parts) == 3 and parts[-1] in ("ro", "rw"):
        src_raw, dst_raw, mode = parts
        readonly = mode == "ro"
    elif len(parts) >= 2:
        src_raw = parts[0]
        dst_raw = ":".join(parts[1:])
        readonly = True
    else:
        raise ValueError(f"mount entry must be 'src:dst[:mode]', got {entry!r}")
    src = src_raw.strip()
    dst = dst_raw.strip()
    if not src or not dst:
        raise ValueError(f"mount entry has empty src or dst: {entry!r}")
    src = str(Path(src).expanduser())
    if dst.startswith("~/"):
        dst = f"{CONTAINER_HOME}/{dst[2:]}"
    elif dst == "~":
        dst = CONTAINER_HOME
    return Mount(src, dst, readonly=readonly)


def _check_addon(name: str) -> str:
    """Verify that an addon script exists in the packaged addons directory."""
    path = _ADDONS_DIR / f"{name}.sh"
    if not path.exists():
        raise ValueError(f"addon not found: {name}")
    return name


def regex_validator(pattern: str, error_template: str) -> Callable[[str], str]:
    """Return a validator that raises ValueError when value doesn't fullmatch pattern."""
    rx = re.compile(pattern)

    def _validate(value: str) -> str:
        if not rx.fullmatch(value):
            raise ValueError(error_template.format(value=value))
        return value

    return _validate


_validate_volume_name = regex_validator(
    r"[a-zA-Z0-9][a-zA-Z0-9_.\-]*",
    "invalid docker volume name: {value!r}",
)


# OptionSpec & registry -----------------------------------------------------


@dataclass(frozen=True)
class OptionSpec:
    """Declarative spec for a configuration option.

    Drives argparse construction, env var reading, yaml lookup, and validation.
    Precedence at resolve time is CLI > env > yaml > default.
    """

    name: str  # Config attribute name
    type: Any  # bool | str | list
    default: Any  # value or zero-arg callable
    flag: str | None = None  # e.g. "--runtime"; None = no CLI surface
    env: str | None = None  # e.g. "CSB_RUNTIME"; None = no env surface
    yaml_key: tuple[str, ...] | None = None  # key path into the YAML dict
    choices: tuple[str, ...] | None = None
    help: str = ""
    validator: Callable[[Any], Any] | None = (
        None  # scalar: called on value; list: per-item
    )
    yaml_example: str | None = None  # rendered after "# <key>:" in generated template


def _is_list(spec: OptionSpec) -> bool:
    return spec.type is list


def _validate_env_pair(val: str) -> str:
    if "=" not in val:
        raise ValueError(f"env entry must be KEY=VALUE, got {val!r}")
    return val


def _bool_from_env(raw: str) -> bool:
    return raw.strip().lower() not in ("0", "false", "no", "")


OPTIONS: list[OptionSpec] = [
    OptionSpec(
        name="use_tmux",
        type=bool,
        default=False,
        flag="--tmux",
        yaml_key=("tmux",),
        help="run inside tmux",
        yaml_example="true",
    ),
    OptionSpec(
        name="use_tty",
        type=bool,
        default=sys.stdin.isatty,  # callable, evaluated lazily
        flag="--tty",
        yaml_key=("tty",),
        help="allocate a TTY (default: auto-detect from stdin)",
        yaml_example="true          # default: auto-detect from stdin",
    ),
    OptionSpec(
        name="mount",
        type=list,
        default=[],
        flag="--mount",
        yaml_key=("mount",),
        help="extra bind mounts (format: src:dst[:mode])",
        validator=_parse_mount,
        yaml_example="\n#   - ~/.ssh:~/.ssh:ro",
    ),
    OptionSpec(
        name="runtime",
        type=str,
        default="auto",
        flag="--runtime",
        env="CSB_RUNTIME",
        yaml_key=("runtime",),
        choices=("auto", "docker", "podman"),
        help="container runtime to use: docker, podman, or auto (default: auto)",
    ),
    OptionSpec(
        name="base_image",
        type=str,
        default="debian:stable-slim",
        flag="--base-image",
        env="CSB_BASE_IMAGE",
        yaml_key=("base_image",),
        help="base image for the container (default: debian:stable-slim)",
        yaml_example="debian:stable-slim",
    ),
    OptionSpec(
        name="nested_podman",
        type=bool,
        default=False,
        flag="--nested-podman",
        env="CSB_NESTED_PODMAN",
        yaml_key=("nested_podman",),
        help="install and configure podman inside the container (default: false)",
        yaml_example="false",
    ),
    OptionSpec(
        name="addons",
        type=list,
        default=["mise"],
        flag="--addon",
        yaml_key=("addons",),
        help="addon to install (repeatable; default: mise)",
        validator=_check_addon,
        yaml_example="[mise]",
    ),
    OptionSpec(
        name="home_volume",
        type=str,
        default="csb-home",
        env="CSB_HOME_VOLUME",
        yaml_key=("home_volume",),
        help="named volume for the container home (default: csb-home)",
        validator=_validate_volume_name,
        yaml_example="csb-home",
    ),
    OptionSpec(
        name="image",
        type=str,
        default=None,
        env="CSB_IMAGE",
        yaml_key=("image",),
        help="override the image name/tag",
        yaml_example="my-custom:latest",
    ),
    OptionSpec(
        name="env_forward",
        type=list,
        default=[],
        flag="--env-forward",
        env="CSB_ENV_FORWARD",
        yaml_key=("env_forward",),
        help="host env var names to forward into the container",
        yaml_example="[MY_TOKEN, OTHER_VAR]",
    ),
    OptionSpec(
        name="env_inject",
        type=list,
        default=[],
        flag="--env",
        env="CSB_ENV",
        yaml_key=("env",),
        help="KEY=VALUE pairs to inject into the container environment",
        yaml_example="[MY_VAR=hello, DEBUG=1]",
        validator=_validate_env_pair,
    ),
    OptionSpec(
        name="host_network",
        type=bool,
        default=False,
        flag="--host-network",
        env="CSB_HOST_NETWORK",
        yaml_key=("host_network",),
        help="use host networking instead of isolated container network (default: false)",
        yaml_example="false",
    ),
]


# Config --------------------------------------------------------------------


@dataclass
class Config:
    """Parsed CLI options and derived paths."""

    cwd: Path
    home: Path
    config_dir: Path | None = None
    workspace: Path | None = None  # None = ephemeral, no workspace mount
    rebuild: bool = False
    reset_home: bool = False
    clean: bool = False
    verbose: bool = False
    passthrough_args: list[str] = field(default_factory=list)

    # Registered options — defaults mirror OPTIONS for direct Config() construction
    # (e.g. in tests). parse_args populates these via the unified resolver.
    use_tmux: bool = False
    use_tty: bool = True
    mount: list[Mount] = field(default_factory=list)
    runtime: str = "auto"
    base_image: str = "debian:stable-slim"
    nested_podman: bool = False
    addons: list[str] = field(default_factory=lambda: ["mise"])
    home_volume: str = "csb-home"
    image: str | None = None
    env_forward: list[str] = field(default_factory=list)
    env_inject: list[str] = field(default_factory=list)
    host_network: bool = False

    def __post_init__(self) -> None:
        if self.config_dir is None:
            self.config_dir = self.home / ".config" / "csb"

    @property
    def container_cli(self) -> str:
        """Resolved container runtime executable ('docker' or 'podman')."""
        return _resolve_runtime(self.runtime)

    @property
    def workdir(self) -> str:
        """Container working directory — /workspace/… for workspaces, /workspace for ephemeral."""
        if self.workspace is None:
            return CONTAINER_WORKDIR
        try:
            rel = self.workspace.relative_to(self.home)
            return f"{CONTAINER_WORKDIR}/{rel}"
        except ValueError:
            parts = self.workspace.parts
            return f"{CONTAINER_WORKDIR}/{'/'.join(parts[-2:])}"

    @property
    def csb_home(self) -> Path:
        """Dedicated container home directory on the host."""
        return self.config_dir / "home"  # type: ignore[operator]


# YAML template generation --------------------------------------------------


def _render_template() -> str:
    """Build the commented config.yaml template from OPTIONS."""
    lines = [
        "# csb configuration — uncomment and edit as needed.",
        "# See: csb --help",
        "#",
    ]
    for spec in OPTIONS:
        if not spec.yaml_key or spec.yaml_example is None:
            continue
        key = ".".join(spec.yaml_key)
        example = spec.yaml_example
        if example.startswith("\n"):
            # Multi-line (mount list)
            lines.append(f"# {key}:")
            for line in example.lstrip("\n").splitlines():
                lines.append(line if line.startswith("#") else f"# {line}")
        else:
            lines.append(f"# {key}: {example}")
        lines.append("#")
    if lines[-1] == "#":
        lines.pop()
    return "\n".join(lines) + "\n"


def _format_help_full() -> str:
    """Render the full reference: all options with flags/env/yaml, then example YAML."""
    lines: list[str] = ["csb — full configuration reference", ""]
    lines.append("OPTIONS")
    lines.append("")

    for spec in OPTIONS:
        if spec.flag:
            if spec.type is bool:
                no_flag = "--no-" + spec.flag[2:]
                lines.append(f"  {spec.flag} / {no_flag}")
            elif _is_list(spec):
                lines.append(f"  {spec.flag} VALUE  (repeatable)")
            else:
                metavar = (
                    "{" + "|".join(spec.choices) + "}"
                    if spec.choices
                    else spec.name.upper()
                )
                lines.append(f"  {spec.flag} {metavar}")
        else:
            lines.append("  (no CLI flag)")

        env_str = (
            spec.env + ("  (space-separated)" if _is_list(spec) else "")
            if spec.env
            else "(none)"
        )
        lines.append(f"    env : {env_str}")

        yaml_str = ".".join(spec.yaml_key) if spec.yaml_key else "(none)"
        lines.append(f"    yaml: {yaml_str}")

        if spec.help:
            lines.append(f"    {spec.help}")

        lines.append("")

    lines.append("EXAMPLE config.yaml")
    lines.append("")
    for line in _render_template().splitlines():
        lines.append(f"  {line}")
    lines.append("")

    return "\n".join(lines)


Directory = object()

# Relative paths (from ~/.config/csb) → default file contents.
CSB_DEFAULT_FILES: dict[str, Any] = {
    "config.yaml": _render_template(),
    "home": Directory,  # empty dir for bind-mounting into container home
}


# Config loading & resolution -----------------------------------------------


def _load_csb_config(config_dir: Path) -> dict:
    """Load config.yaml from config_dir; return empty dict if absent or empty."""
    path = config_dir / "config.yaml"
    if not path.exists():
        return {}
    with path.open() as f:
        return yaml.safe_load(f) or {}


def _init_config_dir(config_dir: Path) -> None:
    """Create config_dir with default template files if it does not exist."""
    if config_dir.exists():
        return

    print(f"Initialising default config at {config_dir} …")

    for rel, content in CSB_DEFAULT_FILES.items():
        path = config_dir / rel
        print(f"Creating {path} …")
        if content is Directory:
            path.mkdir(parents=True, exist_ok=True)
        else:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)


def _yaml_lookup(yaml_cfg: dict, path: tuple[str, ...]) -> Any:
    """Walk a dotted key path through a loaded YAML dict; return _MISSING if absent."""
    d: Any = yaml_cfg
    for k in path[:-1]:
        if not isinstance(d, dict) or k not in d:
            return _MISSING
        d = d[k]
    if not isinstance(d, dict) or path[-1] not in d:
        return _MISSING
    return d[path[-1]]


def _coerce_env(spec: OptionSpec, raw: str) -> Any:
    if spec.type is bool:
        return _bool_from_env(raw)
    if _is_list(spec):
        return raw.split()
    return raw


def _validate(spec: OptionSpec, value: Any) -> Any:
    """Apply choices + validator, coercing list-typed specs per-item."""
    if spec.choices is not None and value not in spec.choices:
        raise ValueError(f"{spec.name}: {value!r} not in {list(spec.choices)}")
    if spec.validator is None:
        return value
    if _is_list(spec):
        return [spec.validator(item) for item in value]
    return spec.validator(value)


def _resolve(spec: OptionSpec, cli_val: Any, yaml_cfg: dict) -> Any:
    """Resolve an option's value with CLI > env > yaml > default precedence."""
    # 1. CLI (None means BooleanOptionalAction not set; _UNSET means store default)
    if cli_val is not _UNSET and cli_val is not None:
        return _validate(spec, cli_val)
    # 2. env
    if spec.env:
        raw = os.environ.get(spec.env)
        if raw is not None and raw != "":
            return _validate(spec, _coerce_env(spec, raw))
    # 3. yaml
    if spec.yaml_key:
        v = _yaml_lookup(yaml_cfg, spec.yaml_key)
        if v is not _MISSING:
            return _validate(spec, v)
    # 4. default
    d = spec.default
    return d() if callable(d) else d


# argparse building ---------------------------------------------------------


def _add_option_args(parser: argparse.ArgumentParser) -> None:
    """Register each OptionSpec flag on the parser."""
    for spec in OPTIONS:
        if spec.flag is None:
            continue
        if spec.type is bool:
            parser.add_argument(
                spec.flag,
                dest=spec.name,
                action=argparse.BooleanOptionalAction,
                default=None,
                help=spec.help,
            )
        elif _is_list(spec):
            parser.add_argument(
                spec.flag,
                dest=spec.name,
                action="append",
                default=None,
                metavar="NAME",
                help=spec.help,
            )
        else:
            kwargs: dict[str, Any] = dict(
                dest=spec.name,
                default=_UNSET,
                metavar=spec.name.upper(),
                help=spec.help,
            )
            if spec.choices is not None:
                kwargs["choices"] = list(spec.choices)
            parser.add_argument(spec.flag, **kwargs)


def parse_args(argv: list[str]) -> Config:
    """Parse CLI arguments with unified CLI > env > yaml > default precedence."""
    # Pre-pass: --config-dir must be resolved before loading yaml.
    _pre = argparse.ArgumentParser(add_help=False)
    _pre.add_argument("--config-dir", default=None)
    _pre_ns, _ = _pre.parse_known_args(argv)
    config_dir = (
        Path(
            _pre_ns.config_dir
            or os.environ.get("CSB_CONFIG_DIR")
            or (Path.home() / ".config" / "csb")
        )
        .expanduser()
        .resolve()
    )
    yaml_cfg = _load_csb_config(config_dir)

    parser = argparse.ArgumentParser(
        prog="csb",
        description="Run commands in an isolated container.",
    )
    workspace_group = parser.add_mutually_exclusive_group()
    workspace_group.add_argument(
        "--workspace",
        default=None,
        metavar="PATH",
        help="host directory to mount as the workspace (default: CWD)",
    )
    workspace_group.add_argument(
        "--no-workspace",
        action="store_true",
        help="ephemeral workspace, no host directory mounted",
    )
    parser.add_argument(
        "--clean",
        action="store_true",
        help="remove all csb images and the home volume, then exit",
    )
    parser.add_argument(
        "--rebuild",
        action="store_true",
        help="force a full image rebuild and recreate volumes",
    )
    parser.add_argument(
        "--reset-home",
        action="store_true",
        help="remove and recreate the home volume (wipes all tool state)",
    )
    parser.add_argument(
        "-v",
        "--verbose",
        action="store_true",
        help="print the run command before executing",
    )
    parser.add_argument(
        "--config-dir",
        default=str(config_dir),
        metavar="PATH",
        help="host directory for csb config (default: ~/.config/csb)",
    )
    parser.add_argument(
        "--help-full",
        action="store_true",
        help="show all config options, env vars, and example YAML, then exit",
    )

    _add_option_args(parser)

    parser.add_argument(
        "rest",
        nargs=argparse.REMAINDER,
        help="arguments passed to the container CMD (use -- to separate)",
    )

    ns = parser.parse_args(argv)

    if ns.help_full:
        print(_format_help_full())
        raise SystemExit(0)

    # REMAINDER captures the '--' separator if present; strip it
    passthrough = ns.rest
    if passthrough and passthrough[0] == "--":
        passthrough = passthrough[1:]

    if ns.no_workspace:
        workspace = None
    elif ns.workspace:
        workspace = Path(ns.workspace).resolve()
    else:
        workspace = Path.cwd()

    # Resolve registered options through the unified precedence pipeline.
    resolved: dict[str, Any] = {}
    try:
        for spec in OPTIONS:
            cli_val = getattr(ns, spec.name, _UNSET)
            resolved[spec.name] = _resolve(spec, cli_val, yaml_cfg)
    except ValueError as e:
        parser.error(str(e))

    return Config(
        cwd=Path.cwd(),
        home=Path.home(),
        config_dir=config_dir,
        workspace=workspace,
        rebuild=ns.rebuild,
        reset_home=ns.reset_home,
        clean=ns.clean,
        verbose=ns.verbose,
        passthrough_args=passthrough,
        **resolved,
    )
