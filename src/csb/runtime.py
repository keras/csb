from __future__ import annotations

import os
import subprocess


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
