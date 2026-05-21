"""csb — Run commands in an isolated container."""

from __future__ import annotations

import os
import shlex
import subprocess
import sys

from .config import (
    Config,
    _PACKAGED_MISE_SH,
    _addons_dir,
    _init_config_dir,
    _render_template,
    _workdir_config_path,
    parse_args,
)
from .container import (
    build_run_command,
    image_labels,
    image_name,
    resolve_env,
    resolve_mounts,
    volume_labels,
    _build_context_tar,
)
from .runtime import Runtime, start_host_exec


def _clean(cfg: Config, runtime: Runtime) -> None:
    """Remove all csb:* images and all labeled csb volumes."""
    image_ids = runtime.list_csb_image_ids()
    volumes = runtime.list_csb_volumes()
    # Always include the current home volume in case it predates labels.
    if cfg.home_volume not in volumes:
        volumes.append(cfg.home_volume)

    if not image_ids and not volumes:
        print("Nothing to remove.")
        return

    if image_ids:
        print(f"Images ({len(image_ids)}):")
        for iid in image_ids:
            print(f"  {iid}")
    if volumes:
        print(f"Volumes ({len(volumes)}):")
        for vol in volumes:
            print(f"  {vol}")

    try:
        answer = input("\nRemove all of the above? [y/N] ").strip().lower()
    except (EOFError, KeyboardInterrupt):
        print()
        sys.exit(1)
    if answer != "y":
        print("Aborted.")
        sys.exit(1)

    if image_ids:
        print(f"Removing {len(image_ids)} image(s)...")
        runtime.remove_images(image_ids)
    for vol in volumes:
        print(f"Removing volume {vol}...")
        runtime.remove_volume(vol)


def _config_edit(cfg: Config) -> None:
    """Open the target config file in the user's editor, creating it if absent."""
    header = ""
    if cfg.config_edit_target == "workdir":
        if cfg.workspace is None:
            print(
                "csb config-edit workdir requires a workspace (not --no-workspace)",
                file=sys.stderr,
            )
            sys.exit(1)
        path = _workdir_config_path(cfg.config_dir, cfg.workspace)
        header = f"# workdir: {cfg.workspace}\n"
    else:
        path = cfg.config_dir / "config.yaml"

    if not path.exists():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(header + _render_template())

    editor = os.environ.get("VISUAL") or os.environ.get("EDITOR") or "vi"
    result = subprocess.run([editor, str(path)])
    sys.exit(result.returncode)


def main(args) -> None:
    cfg = parse_args(args)
    _init_config_dir(cfg.config_dir)
    addons = _addons_dir(cfg.config_dir)

    if cfg.subcommand == "run":
        for name in cfg.addons:
            if not (_addons_dir(cfg.config_dir) / f"{name}.sh").exists():
                print(f"csb: error: addon not found: {name}", file=sys.stderr)
                sys.exit(2)

    runtime = Runtime(cfg.container_cli)

    if cfg.subcommand == "config_edit":
        _config_edit(cfg)
        return  # unreachable; _config_edit calls sys.exit

    if cfg.subcommand == "clean":
        _clean(cfg, runtime)
        return

    if cfg.reset_home:
        print(f"Removing home volume {cfg.home_volume}...", file=sys.stderr)
        runtime.remove_volume(cfg.home_volume)

    if cfg.rebuild or not runtime.image_exists(image_name(cfg)):
        runtime.build_image(
            image_name(cfg),
            _build_context_tar(cfg),
            image_labels(cfg),
            quiet=not cfg.verbose,
        )

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
