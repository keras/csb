"""Hatch build hook: optionally compile Go binaries for host-exec support.

If Go is not available or the build fails, the install succeeds without
the binaries — host-exec is simply unavailable at runtime.
"""

from __future__ import annotations

import os
import platform
import shutil
import subprocess
import sys
from pathlib import Path

from hatchling.builders.hooks.plugin.interface import BuildHookInterface


class CustomBuildHook(BuildHookInterface):
    def initialize(self, version: str, build_data: dict) -> None:
        root = Path(self.root)
        bin_dir = root / "src" / "csb" / "bin"

        if not self._go_available():
            self._warn("Go not found — skipping host-exec binary build (host-exec will be unavailable)")
            return

        bin_dir.mkdir(parents=True, exist_ok=True)

        self._build_broker(root, bin_dir)
        self._build_client(root, bin_dir)

    # ------------------------------------------------------------------

    def _go_available(self) -> bool:
        return shutil.which("go") is not None

    def _warn(self, msg: str) -> None:
        print(f"[csb build] WARNING: {msg}", file=sys.stderr)

    def _build(self, root: Path, output: Path, pkg: str, goos: str, goarch: str) -> bool:
        env = {**os.environ, "CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch}
        result = subprocess.run(
            ["go", "build", "-ldflags=-s -w", "-trimpath", "-o", str(output), pkg],
            cwd=str(root),
            env=env,
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            self._warn(
                f"go build failed for {pkg} ({goos}/{goarch}): {result.stderr.strip()}"
            )
            return False
        return True

    def _build_broker(self, root: Path, out_dir: Path) -> None:
        """Build broker for the current host platform only."""
        goos = {"darwin": "darwin", "linux": "linux", "win32": "windows"}.get(
            sys.platform, sys.platform
        )
        machine = platform.machine().lower()
        goarch = "arm64" if machine in ("arm64", "aarch64") else "amd64"

        suffix = ".exe" if goos == "windows" else ""
        out = out_dir / f"csb-host-broker{suffix}"
        if self._build(root, out, "./cmd/csb-host-broker", goos, goarch):
            print(f"[csb build] Built {out}")
        else:
            self._warn("csb-host-broker build failed — host-exec will be unavailable")

    def _build_client(self, root: Path, out_dir: Path) -> None:
        """Build sandbox client for linux/amd64 and linux/arm64 (runs in containers)."""
        built = False
        for goarch in ("amd64", "arm64"):
            out = out_dir / f"csb-host-run.{goarch}"
            if self._build(root, out, "./cmd/csb-host-run", "linux", goarch):
                print(f"[csb build] Built {out}")
                built = True

        if not built:
            self._warn("csb-host-run build failed for all targets — host-exec will be unavailable")
            return

        # Symlink/copy the native-arch binary as the default name so that
        # existing code (container.py, Makefile) can reference src/csb/bin/csb-host-run.
        machine = platform.machine().lower()
        native_arch = "arm64" if machine in ("arm64", "aarch64") else "amd64"
        native = out_dir / f"csb-host-run.{native_arch}"
        default = out_dir / "csb-host-run"
        if native.exists():
            if default.exists() or default.is_symlink():
                default.unlink()
            default.symlink_to(native.name)
