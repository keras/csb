"""csb — Run commands in an isolated container."""

from __future__ import annotations

import shlex
import subprocess
import sys
from pathlib import Path

from .config import Config, Mount, _init_config_dir, parse_args
from .container import build_run_command, container_labels, image_name, resolve_env, resolve_mounts, volume_labels, _build_context_tar
from .runtime import Runtime, start_host_exec


def _clean(cfg: Config, runtime: Runtime) -> None:
    """Remove all csb:* images and all labeled csb volumes."""
    image_ids = runtime.list_csb_image_ids()
    if image_ids:
        print(f"Removing {len(image_ids)} csb image(s)...")
        runtime.remove_images(image_ids)
    else:
        print("No csb images found.")

    volumes = runtime.list_csb_volumes()
    # Always include the current home volume in case it predates labels.
    if cfg.home_volume not in volumes:
        volumes.append(cfg.home_volume)
    for vol in volumes:
        print(f"Removing volume {vol}...")
        runtime.remove_volume(vol)


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

    runtime.ensure_volume(cfg.home_volume, volume_labels(cfg))

    broker_proc = None
    broker_url = None
    broker_token = None
    if cfg.host_exec_enabled:
        broker_proc, broker_url, broker_token = start_host_exec(
            cfg.host_exec_allow, cfg.host_exec_bind, cfg.container_cli
        )

    mounts = resolve_mounts(cfg)
    env = resolve_env(cfg, broker_url=broker_url, broker_token=broker_token)
    cmd = build_run_command(cfg, mounts, env)

    if cfg.verbose:
        print(shlex.join(cmd), file=sys.stderr)

    if broker_proc is not None:
        # Can't use os.execvp when we need to clean up the broker after container exits.
        try:
            result = subprocess.run(cmd)
        finally:
            broker_proc.terminate()
            broker_proc.wait()
        sys.exit(result.returncode)
    else:
        runtime.exec_run(cmd)


def main_entry() -> None:
    """Entry point for both `uv tool install` and `python -m csb`."""
    main(sys.argv[1:])
