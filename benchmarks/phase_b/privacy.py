"""Where the golden set may live (#441, "Data and privacy").

The golden set comes from a personal brain and must never be committed to
this public repository. Every Phase B tool that writes resolves its output
directory through check_output_dir, which refuses:

- any directory inside a git worktree (this repository or any other);
- any directory inside this repository's checkout, even without git;
- the brain's own data directory, ~/.local/share/mnemos, so a run can never
  write next to the brain it read.

Files are written owner-only (0600 in a 0700 directory), and each output
directory gets a .gitignore that ignores everything in it, so a directory
later moved into a repository still stays out of it.
"""

from __future__ import annotations

import json
import os
import subprocess
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_OUT = Path.home() / ".local" / "share" / "mnemos-eval" / "phase-b"
BRAIN_DATA_DIR = Path.home() / ".local" / "share" / "mnemos"


class UnsafeOutputDir(ValueError):
    """The output directory could let the golden set reach a repository."""


def _nearest_existing(path: Path) -> Path:
    p = path
    while not p.exists() and p != p.parent:
        p = p.parent
    return p


def _inside(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def git_worktree_of(path: Path) -> Path | None:
    """The top of the git worktree containing path, or None."""
    probe = _nearest_existing(path)
    if probe.is_file():
        probe = probe.parent
    try:
        out = subprocess.run(
            ["git", "-C", str(probe), "rev-parse", "--show-toplevel"],
            capture_output=True, text=True, timeout=10, check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if out.returncode != 0 or not out.stdout.strip():
        return None
    return Path(out.stdout.strip()).resolve()


def check_output_dir(path: Path) -> Path:
    """Resolve path and refuse it if the golden set could leak from it."""
    resolved = Path(os.path.expanduser(str(path))).resolve()
    if _inside(resolved, REPO_ROOT.resolve()):
        raise UnsafeOutputDir(f"{resolved} is inside the mnemos checkout {REPO_ROOT}; "
                              "the golden set must stay out of the repository")
    if _inside(resolved, BRAIN_DATA_DIR.resolve()):
        raise UnsafeOutputDir(f"{resolved} is inside the brain's data directory {BRAIN_DATA_DIR}; "
                              f"use a directory such as {DEFAULT_OUT}")
    top = git_worktree_of(resolved)
    if top is not None:
        raise UnsafeOutputDir(f"{resolved} is inside the git worktree {top}; "
                              "the golden set must not be written where git can commit it")
    return resolved


def make_private_dir(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=True)
    os.chmod(path, 0o700)
    guard = path / ".gitignore"
    if not guard.exists():
        write_private(guard, "# Private golden set (#441 Phase B): never commit anything here.\n*\n")


def write_private(path: Path, text: str) -> None:
    """Write atomically, owner-only."""
    tmp = path.with_name(path.name + ".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(text)
    os.chmod(tmp, 0o600)
    os.replace(tmp, path)


def write_json(path: Path, obj) -> None:
    write_private(path, json.dumps(obj, indent=1, ensure_ascii=False, sort_keys=False) + "\n")
