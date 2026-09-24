"""Host kernel pressure checks for high-concurrency container lifecycles."""

from __future__ import annotations

import os
from pathlib import Path
import platform
import time


DEFAULT_MAX_D_STATE = 128
DEFAULT_MAX_INODE_SWITCH_WBS = 32
DEFAULT_RUNC_DELETE_AGE_SECONDS = 30.0
DEFAULT_MAX_IO_FULL_AVG10 = 10.0


def block_device_name_for_path(
    path: Path,
    *,
    sys_dev_block: Path = Path("/sys/dev/block"),
) -> str | None:
    """Return the kernel block-device name backing an existing path."""

    try:
        stat = path.resolve().stat()
        device_link = sys_dev_block / (
            f"{os.major(stat.st_dev)}:{os.minor(stat.st_dev)}"
        )
        return device_link.resolve(strict=True).name
    except (FileNotFoundError, OSError, RuntimeError):
        return None


def block_device_names_for_paths(paths: list[Path]) -> set[str]:
    return {
        device
        for path in paths
        if (device := block_device_name_for_path(path)) is not None
    }


def _process_record(proc_path: Path, uptime: float, clock_ticks: int) -> dict | None:
    try:
        raw_stat = (proc_path / "stat").read_text(encoding="utf-8")
        right_paren = raw_stat.rfind(")")
        fields = raw_stat[right_paren + 2 :].split()
        state = fields[0]
        start_ticks = int(fields[19])
        comm = (proc_path / "comm").read_text(encoding="utf-8").strip()
        cmdline = (
            (proc_path / "cmdline")
            .read_bytes()
            .replace(b"\0", b" ")
            .decode(errors="replace")
            .strip()
        )
    except (FileNotFoundError, OSError, ValueError, IndexError):
        return None
    return {
        "pid": int(proc_path.name),
        "state": state,
        "comm": comm,
        "cmdline": cmdline,
        "age_seconds": max(0.0, uptime - start_ticks / clock_ticks),
    }


def sample_host_kernel_pressure(
    *,
    proc_root: Path = Path("/proc"),
    max_d_state: int = DEFAULT_MAX_D_STATE,
    max_inode_switch_wbs: int = DEFAULT_MAX_INODE_SWITCH_WBS,
    runc_delete_age_seconds: float = DEFAULT_RUNC_DELETE_AGE_SECONDS,
    max_io_full_avg10: float | None = None,
    monitored_block_devices: set[str] | None = None,
) -> dict:
    try:
        uptime = float((proc_root / "uptime").read_text().split()[0])
        clock_ticks = int(os.sysconf("SC_CLK_TCK"))
        proc_paths = [
            path for path in proc_root.iterdir() if path.name.isdigit()
        ]
    except (OSError, ValueError, IndexError):
        return {
            "healthy": False,
            "reasons": ["host_process_state_unavailable"],
            "sampled_at": time.time(),
            "kernel_release": platform.release(),
        }

    d_state = 0
    inode_switch_wbs = 0
    aged_runc_delete: list[dict] = []
    critical_d_state: list[dict] = []
    ignored_unrelated_xfs_d_state: list[dict] = []

    for proc_path in proc_paths:
        record = _process_record(proc_path, uptime, clock_ticks)
        if record is None:
            continue
        state = record["state"]
        comm = record["comm"]
        if state == "D":
            d_state += 1
        if "inode_switch_wbs" in comm:
            inode_switch_wbs += 1
        if (
            comm == "runc"
            and " delete " in f" {record['cmdline']} "
            and record["age_seconds"] >= runc_delete_age_seconds
        ):
            aged_runc_delete.append({
                "pid": record["pid"],
                "age_seconds": round(record["age_seconds"], 1),
                "state": state,
            })
        is_rcu_critical = comm.startswith("rcu_exp_gp")
        is_xfs_critical = comm.startswith("xfsaild/")
        if is_xfs_critical and monitored_block_devices is not None:
            xfs_device = comm.removeprefix("xfsaild/")
            is_xfs_critical = xfs_device in monitored_block_devices
            if state == "D" and not is_xfs_critical:
                ignored_unrelated_xfs_d_state.append({
                    "pid": record["pid"],
                    "comm": comm,
                })
        if state == "D" and (is_rcu_critical or is_xfs_critical):
            critical_d_state.append({
                "pid": record["pid"],
                "comm": comm,
            })

    reasons = []
    io_full_avg10 = None
    if max_io_full_avg10 is not None:
        try:
            pressure_lines = (proc_root / "pressure/io").read_text(
                encoding="utf-8"
            ).splitlines()
            full_line = next(
                line for line in pressure_lines if line.startswith("full ")
            )
            values = dict(
                field.split("=", 1)
                for field in full_line.split()[1:]
            )
            io_full_avg10 = float(values["avg10"])
        except (
            FileNotFoundError,
            OSError,
            StopIteration,
            ValueError,
            KeyError,
        ):
            reasons.append("io_pressure_unavailable")
        else:
            if io_full_avg10 > max_io_full_avg10:
                reasons.append(
                    f"io_full_avg10={io_full_avg10:.2f}>"
                    f"{max_io_full_avg10:.2f}"
                )
    if d_state > max_d_state:
        reasons.append(f"d_state={d_state}>{max_d_state}")
    if inode_switch_wbs > max_inode_switch_wbs:
        reasons.append(
            f"inode_switch_wbs={inode_switch_wbs}>{max_inode_switch_wbs}"
        )
    if aged_runc_delete:
        reasons.append(f"aged_runc_delete={len(aged_runc_delete)}")
    if critical_d_state:
        reasons.append(f"critical_kernel_d_state={len(critical_d_state)}")

    return {
        "healthy": not reasons,
        "reasons": reasons,
        "sampled_at": time.time(),
        "kernel_release": platform.release(),
        "d_state": d_state,
        "io_full_avg10": io_full_avg10,
        "inode_switch_wbs": inode_switch_wbs,
        "aged_runc_delete": aged_runc_delete,
        "critical_d_state": critical_d_state,
        "monitored_block_devices": (
            sorted(monitored_block_devices)
            if monitored_block_devices is not None
            else None
        ),
        "ignored_unrelated_xfs_d_state": ignored_unrelated_xfs_d_state,
        "limits": {
            "max_d_state": max_d_state,
            "max_inode_switch_wbs": max_inode_switch_wbs,
            "runc_delete_age_seconds": runc_delete_age_seconds,
            "max_io_full_avg10": max_io_full_avg10,
        },
    }
