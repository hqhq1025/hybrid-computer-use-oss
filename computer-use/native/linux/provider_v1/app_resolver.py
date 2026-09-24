"""Linux AT-SPI application discovery and inventory."""

from .foundation import (
    OperationFailure,
    node_name,
    node_pid,
    process_generation,
    provider_bus_name,
)
from .atspi_inventory import resolve_app, resolve_app_identity
from .snapshot import snapshot_digest, utc_timestamp
from .x11_windows import app_windows, iter_apps, window_instance_handle
from .window_diagnostics import accepts_user_input

def list_apps_inventory():
    entries = []
    seen_identities = set()
    for app in sorted(
        iter_apps(),
        key=lambda item: (node_name(item).lower(), node_pid(item)),
    ):
        pid = node_pid(app)
        bus = provider_bus_name(app)
        generation = process_generation(pid)
        identity = (str(bus or "").strip(), str(generation or "").strip())
        if all(identity) and identity in seen_identities:
            continue
        if all(identity):
            seen_identities.add(identity)
        windows = app_windows(app)
        if not windows:
            continue
        titles = [node_name(window) or "untitled" for _, window in windows]
        title = titles[0]
        name = node_name(app)
        # Flag entries that cannot receive input directly in the listing:
        # desktop, panel and window-manager entries otherwise look like real
        # applications, and resolve_app rejects actions against them (see
        # infrastructure_app_error).
        input_capable = accepts_user_input(app)
        suffix = "" if input_capable else (
            ", NOT an input target: every window is untitled and has no "
            "interactive content — desktop infrastructure, action tools will "
            "refuse it")
        entries.append({
            "name": name,
            "pid": pid,
            "title": title,
            "other_windows": titles[1:],
            "suffix": suffix,
            "input_capable": input_capable,
            "provider_bus_name": bus,
            "process_generation": generation,
        })
    name_counts = {}
    for entry in entries:
        name_counts[entry["name"]] = name_counts.get(entry["name"], 0) + 1
    lines = []
    for entry in entries:
        pid = (
            ", pid={}".format(entry["pid"])
            if name_counts[entry["name"]] > 1
            else ""
        )
        other_windows = (
            ", other_windows=" + " | ".join(entry["other_windows"])
            if entry["other_windows"]
            else ""
        )
        display_text = (
            '{} [window="{}"{}{}{}]'.format(
                entry["name"],
                entry["title"],
                pid,
                other_windows,
                entry["suffix"],
            )
        )
        entry["display_text"] = display_text
        lines.append(display_text)
    return {
        "text": "\n".join(lines),
        "apps": [
            {
                "name": entry["name"],
                "bundleIdentifier": entry["name"],
                "pid": entry["pid"],
                "providerBusName": entry["provider_bus_name"],
                "processGeneration": entry["process_generation"],
                "displayText": entry["display_text"],
                "inputCapable": entry["input_capable"],
            }
            for entry in entries
        ],
    }


def list_apps_text():
    return list_apps_inventory()["text"]


def list_apps_records():
    return list_apps_inventory()["apps"]


def app_window_inventory(query, app_identity=None, target_window_handle=None):
    """Return every current top-level window title for one resolved AX app."""
    app = (
        resolve_app_identity(app_identity)
        if app_identity is not None
        else resolve_app(query)
    )
    windows = app_windows(app)
    if not windows.complete:
        raise OperationFailure(
            "observation_incomplete: the application window inventory could not "
            "be read completely.",
            failure_code="observation_incomplete",
            failure_phase="observation",
            input_events_emitted=False,
            failure_subject="window",
        )
    target = str(target_window_handle or "").strip()
    titles = []
    matched = 0
    for index, window in windows:
        if target and window_instance_handle(app, index, window) != target:
            continue
        matched += 1
        title = str(node_name(window) or "").strip()
        if title:
            titles.append(title)
    if target and matched == 0:
        raise OperationFailure(
            "stale_window_ref: the referenced logical window no longer exists.",
            failure_code="stale_window_ref",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    return {
        "ok": True,
        "appName": str(node_name(app) or "").strip(),
        "appPid": node_pid(app),
        "windowTitles": titles,
    }


def windowless_snapshot(app_name, app_pid, closed_handle):
    sampled_at = utc_timestamp()
    lines = ["No top-level AX windows remain for this application after close."]
    return {
        "app": {
            "name": app_name or "closed application",
            "bundleIdentifier": app_name or "closed application",
            "pid": int(app_pid or 0),
        },
        "windowAbsent": True,
        "closedWindowHandle": closed_handle,
        "windows": [],
        "sampledAt": sampled_at,
        "snapshotId": snapshot_digest(
            "windowless:" + str(closed_handle or ""), lines, "", ""
        ),
        "treeLines": lines,
        "elements": [],
        "refs": {},
    }
