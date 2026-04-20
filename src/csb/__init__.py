"""csb — Run commands in an isolated container."""

from __future__ import annotations

import shlex
import sys
from pathlib import Path

from .config import Config, Mount, _init_config_dir, parse_args
from .container import build_run_command, image_name, resolve_env, resolve_mounts, _build_context_tar
from .runtime import Runtime


def _clean(cfg: Config, runtime: Runtime) -> None:
    """Remove all csb:* images and the home volume."""
    image_ids = runtime.list_csb_image_ids()
    if image_ids:
        print(f"Removing {len(image_ids)} csb image(s)...")
        runtime.remove_images(image_ids)
    else:
        print("No csb images found.")

    print(f"Removing home volume {cfg.home_volume}...")
    runtime.remove_volume(cfg.home_volume)


def main(args) -> None:
    cfg = parse_args(args)
    _init_config_dir(cfg.config_dir)
    runtime = Runtime(cfg.container_cli)

    if cfg.clean:
        _clean(cfg, runtime)
        return

    if cfg.reset_home:
        print(f"Removing home volume {cfg.home_volume}...")
        runtime.remove_volume(cfg.home_volume)

    if cfg.rebuild or not runtime.image_exists(image_name(cfg)):
        runtime.build_image(image_name(cfg), _build_context_tar(cfg), quiet=not cfg.verbose)

    mounts = resolve_mounts(cfg)
    env = resolve_env(cfg)
    cmd = build_run_command(cfg, mounts, env)

    if cfg.verbose:
        print(shlex.join(cmd), file=sys.stderr)

    runtime.exec_run(cmd)


def main_entry() -> None:
    """Entry point for both `uv tool install` and `python -m csb`."""
    main(sys.argv[1:])
