#!/usr/bin/env python3
"""Run a command with PTY-backed stdout and forward output incrementally."""

from __future__ import annotations

import argparse
import errno
import os
from pathlib import Path
import pty
import subprocess
import sys


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--stdin", required=True)
    parser.add_argument("--stderr", required=True)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.command[:1] == ["--"]:
        args.command = args.command[1:]
    if not args.command:
        parser.error("command is required after --")
    return args


def main() -> int:
    args = parse_args()
    master_fd, slave_fd = pty.openpty()
    try:
        with (
            Path(args.stdin).open("rb") as stdin,
            Path(args.stderr).open("wb") as stderr,
        ):
            process = subprocess.Popen(
                args.command,
                stdin=stdin,
                stdout=slave_fd,
                stderr=stderr,
                close_fds=True,
            )
        os.close(slave_fd)
        slave_fd = -1

        while True:
            try:
                chunk = os.read(master_fd, 65536)
            except OSError as exc:
                if exc.errno == errno.EIO:
                    break
                raise
            if not chunk:
                break
            sys.stdout.buffer.write(chunk)
            sys.stdout.buffer.flush()
        return process.wait()
    finally:
        os.close(master_fd)
        if slave_fd >= 0:
            os.close(slave_fd)


if __name__ == "__main__":
    raise SystemExit(main())
