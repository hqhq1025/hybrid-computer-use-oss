"""Task-scoped setup dependencies; no guest execution or network discovery."""

from __future__ import annotations

import ast
import inspect
from pathlib import Path

from hcu.osworld.task import task_value


def config_asset_sources(config: list[dict]) -> list[str]:
    from desktop_env.file_source import HISTORY_DB_URL

    sources = []
    for step in config:
        kind, parameters = step["type"], step.get("parameters", {})
        if kind == "download":
            sources.extend(item["url"] for item in parameters["files"])
        elif kind == "upload_file":
            sources.extend(item["local_path"] for item in parameters["files"])
        elif kind == "update_browse_history":
            sources.append(HISTORY_DB_URL)
        elif kind == "googledrive":
            sources.extend(
                args["url"]
                for operation, args in zip(parameters["operation"], parameters["args"])
                if operation == "upload"
            )
    if any(not isinstance(source, str) or not source for source in sources):
        raise ValueError("Setup asset sources must be nonempty strings")
    return sources


def _static_string(node: ast.AST, constants: dict) -> str:
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value
    if isinstance(node, ast.Name) and isinstance(constants.get(node.id), str):
        return constants[node.id]
    if isinstance(node, ast.JoinedStr):
        return "".join(
            _static_string(part.value, constants)
            if isinstance(part, ast.FormattedValue) and part.conversion == -1 and part.format_spec is None
            else _static_string(part, constants)
            for part in node.values
        )
    if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Add):
        return _static_string(node.left, constants) + _static_string(node.right, constants)
    raise ValueError("runtime-computed asset expression")


def task_asset_sources(task, benchmark_version: str) -> tuple[list[str], list[str]]:
    """Return known sources and unresolved Python declarations.

    Python setup code is not executed speculatively. Computed paths require a
    task entry in the local manifest; runtime downloads still fail closed.
    """
    from desktop_env.file_source import asset

    if benchmark_version not in ("v1", "v2"):
        raise ValueError(f"Unknown benchmark version: {benchmark_version}")
    sources = config_asset_sources(task_value(task, "config", []) or [])
    # Evaluator postconfig uses the same setup controller and task-local bindings.
    evaluator = task_value(task, "evaluator", {}) or {}
    sources.extend(config_asset_sources(evaluator.get("postconfig", []) or []))
    unresolved = []
    if type(task) is not dict:
        module = inspect.getmodule(type(task))
        if module is None or not getattr(module, "__file__", None):
            raise ValueError("Cannot inspect task dependencies; task module has no source")
        source_path = Path(module.__file__)
        tree = ast.parse(source_path.read_text(encoding="utf-8"))
        names = {
            alias.asname or alias.name
            for node in ast.walk(tree)
            if isinstance(node, ast.ImportFrom) and node.module == "desktop_env.file_source"
            for alias in node.names if alias.name == "asset"
        }
        constants = dict(vars(module))
        # Only literal assignments, never eval or task setup execution.
        for node in ast.walk(tree):
            if isinstance(node, ast.Assign):
                try:
                    value = _static_string(node.value, constants)
                except ValueError:
                    continue
                for target in node.targets:
                    if isinstance(target, ast.Name):
                        constants.setdefault(target.id, value)
        for node in ast.walk(tree):
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id in names:
                try:
                    if len(node.args) != 1 or node.keywords:
                        raise ValueError("unsupported asset call")
                    sources.append(asset(_static_string(node.args[0], constants)))
                except ValueError:
                    unresolved.append(f"{source_path.name}:{node.lineno}")
        # Custom setup need not use asset(), so require an explicit inventory
        # even when no declarations were statically visible.
        if not names:
            unresolved.append(f"{source_path.name}: custom Python setup dependencies")
    return list(dict.fromkeys(sources)), unresolved
