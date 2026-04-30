from __future__ import annotations

import json
import os
import select
import shutil
import subprocess
from pathlib import Path


class Runtime:
    def __init__(self, cli: str) -> None:
        self.cli = cli

    def image_exists(self, name: str) -> bool:
        return (
            subprocess.run(
                [self.cli, "image", "inspect", name],
                capture_output=True,
            ).returncode
            == 0
        )

    def build_image(self, name: str, context: bytes, quiet: bool) -> None:
        print(f"Building {name}...")
        run_kwargs: dict = {}
        if quiet:
            run_kwargs["stdout"] = subprocess.DEVNULL
            run_kwargs["stderr"] = subprocess.DEVNULL
        subprocess.run(
            [self.cli, "build", "-t", name, "-"],
            input=context,
            check=True,
            **run_kwargs,
        )

    def list_csb_image_ids(self) -> list[str]:
        result = subprocess.run(
            [self.cli, "images", "--filter", "reference=csb", "--format", "{{.ID}}"],
            capture_output=True,
            text=True,
        )
        return result.stdout.split() if result.returncode == 0 else []

    def remove_images(self, ids: list[str]) -> None:
        subprocess.run([self.cli, "rmi", "-f", *ids], check=False)

    def remove_volume(self, name: str) -> None:
        subprocess.run(
            [self.cli, "volume", "rm", "-f", name],
            check=False,
            capture_output=True,
        )

    def exec_run(self, argv: list[str]) -> None:
        os.execvp(argv[0], argv)


def _find_broker_bin() -> str | None:
    """Return path to csb-host-broker, or None if not available."""
    if found := shutil.which("csb-host-broker"):
        return found
    # installed wheel layout: src/csb/bin/csb-host-broker
    pkg_bin = Path(__file__).parent / "bin" / "csb-host-broker"
    if pkg_bin.exists():
        return str(pkg_bin)
    # dev layout: repo-root/bin/csb-host-broker (built by Makefile)
    dev_bin = Path(__file__).parent.parent.parent / "bin" / "csb-host-broker"
    if dev_bin.exists():
        return str(dev_bin)
    return None


def host_exec_available() -> bool:
    """Return True if the host-exec binaries are present and usable."""
    return _find_broker_bin() is not None


def _container_gateway_ip(container_cli: str) -> str | None:
    """Return the host IP on the container bridge network (Linux only), or None."""
    import sys
    if sys.platform == "darwin":
        return None  # Docker Desktop routes host.docker.internal through the VM
    try:
        if container_cli == "podman":
            result = subprocess.run(
                ["podman", "network", "inspect", "podman",
                 "--format", "{{range .Subnets}}{{.Gateway}}{{end}}"],
                capture_output=True, text=True, timeout=5,
            )
        else:
            result = subprocess.run(
                ["docker", "network", "inspect", "bridge",
                 "--format", "{{(index .IPAM.Config 0).Gateway}}"],
                capture_output=True, text=True, timeout=5,
            )
        ip = result.stdout.strip()
        if result.returncode == 0 and ip:
            return ip
    except Exception:
        pass
    return None


def start_host_exec(
    allow_rules: list[str], bind: str, container_cli: str = "docker"
) -> tuple[subprocess.Popen, str, str]:
    """Start csb-host-broker on the host and return (proc, ws_url, token).

    The broker prints {"port": N, "token": "..."} to stdout when ready.
    The caller is responsible for terminating proc when the container exits.
    """
    broker_bin = _find_broker_bin()
    if broker_bin is None:
        raise SystemExit(
            "error: --host-exec requires the csb-host-broker binary, which was not found.\n"
            "Install Go and run:  make build\n"
            "Then reinstall csb or keep the binaries on PATH."
        )

    gateway_ip = _container_gateway_ip(container_cli)
    if gateway_ip:
        # Bind only to the container network interface, not all host interfaces.
        port_part = bind.rsplit(":", 1)[-1]
        actual_bind = f"{gateway_ip}:{port_part}"
    else:
        actual_bind = bind

    cmd = [broker_bin, "--bind", actual_bind]
    for rule in allow_rules:
        cmd += ["--allow", rule]

    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        text=True,
    )

    ready, _, _ = select.select([proc.stdout], [], [], 5.0)
    if not ready:
        proc.kill()
        raise RuntimeError("csb-host-broker did not print ready signal within 5 s")
    ready_line = proc.stdout.readline()
    if not ready_line:
        proc.kill()
        raise RuntimeError("csb-host-broker exited before printing ready signal")

    info = json.loads(ready_line)
    port: int = info["port"]
    token: str = info["token"]

    if gateway_ip:
        url_host = gateway_ip
    elif container_cli == "podman":
        url_host = "host.containers.internal"
    else:
        url_host = "host.docker.internal"
    url = f"ws://{url_host}:{port}/run"

    return proc, url, token
