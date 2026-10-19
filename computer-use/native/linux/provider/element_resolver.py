"""Resolution of snapshot refs and semantic targets to live nodes."""

from dataclasses import dataclass
from typing import Any

import time
from .foundation import (
    Atspi,
    DEFAULT_TEXT_LIMIT,
    MAX_DEPTH,
    MAX_ELEMENTS,
    OperationFailure,
    TREE_DEADLINE_SECONDS,
    child_at,
    extents,
    inventory_complete,
    node_pid,
    node_role,
    process_generation,
    provider_bus_name,
    provider_object_path,
    safe,
    state_contains,
)
from .recovery import recovery_window_candidate
from .primitives import name_fingerprint

from .ax_snapshot import render_tree
from .ax_node_properties import (
    accessible_id,
    parse_text_limit,
    positive_int,
)
from .ax_records import (
    StableIndexer,
    effective_name,
)

from .x11_windows import (
    app_windows,
    main_window,
    relative_frame,
    window_instance_handle,
)
from .ref_continuity import (
    ambiguity_candidate,
    deduplicate_provider_records,
    provider_identity_matches_target,
    provider_identity_record,
    resolve_provider_runtime_paths,
    resolve_table_cell_from_owner,
    scan_provider_identity,
    scan_semantic_target,
    semantic_role_matches,
    semantic_target_predicates_match,
)
from .primitives import semantic_text_key


def resolve_path(app, path):
    if not path:
        return None
    node = app
    for index in path:
        node = child_at(node, int(index))
        if node is None:
            return None
    return node


def same_frame(record_frame, node_frame):
    if record_frame is None or node_frame is None:
        return False
    for key in ("x", "y", "width", "height"):
        if abs(float(record_frame.get(key, 0)) - float(node_frame.get(key, 0))) > 3:
            return False
    return True


def record_name_matches(node, record):
    """Return whether the node's name equals the name stored in ``record``.

    Both sides must be measured the same way. Record names are truncated by
    limit_text() while live names are complete, so a direct comparison would
    fail silently and quietly weaken the identity check. Truncated records
    carry ``nameHash``, a fingerprint of the full name, which avoids both
    truncation mismatches and false matches on a shared prefix.
    """
    live = effective_name(node)
    fingerprint = record.get("nameHash")
    if fingerprint:
        return name_fingerprint(live) == fingerprint
    return live == str(record.get("name") or "")


def record_still_matches(node, record, window_bounds):
    """Return whether a node resolved by path is still the snapshot element.

    ``runtimeId`` is a positional path of child indices, so once the tree
    changes the same path can point at a different control. A ref to a
    context-menu item such as "Rename…" can, for example, resolve to a toolbar
    toggle after the menu closes, and the action would still report success.
    Silently acting on the wrong control is the worst failure mode: it is
    undetectable and can be destructive ("Move to Trash" may sit next to
    "Rename…"). Failing resolution so the caller re-reads state is preferable.

    The check uses only identifiers the snapshot actually recorded:

    - automationId, if present, must match (the strongest, a stable id
      provided by the toolkit);
    - otherwise a recorded name requires role and name to match;
    - otherwise the role must match, the node must still be unnamed, and a
      recorded frame must be unchanged, because an unnamed control can only
      be identified by position.
    """
    if node is None:
        return False
    target_id = str(record.get("automationId") or "")
    if target_id:
        return accessible_id(node) == target_id
    target_role = str(record.get("controlType") or "")
    if target_role and node_role(node) != target_role:
        return False
    target_name = str(record.get("name") or "")
    if target_name:
        return record_name_matches(node, record)
    if effective_name(node):
        # The snapshot recorded no name but the node now has one, so it is a
        # different element. Use effective_name() here as well: a borrowed
        # name counts as a name, as it does when record_for() stores records.
        return False
    if record.get("frame") is None:
        return True
    return same_frame(record.get("frame"), relative_frame(node, window_bounds))


def find_element_at_snapshot_path(app, record):
    """Resolve only the original snapshot path; never rebind to a same-name peer."""
    if not record:
        return None
    _, window = main_window(app)
    return find_element_at_snapshot_path_with_bounds(
        app, record, extents(window))


def find_element_at_snapshot_path_with_bounds(app, record, window_bounds):
    """Resolve a snapshot path using the bounds of the window that owns it."""
    if not record:
        return None
    paths = [record.get("runtimeId") or []]
    paths.extend(record.get("runtimeAliases") or [])
    seen_paths = set()
    for path in paths:
        path_key = tuple(path)
        if path_key in seen_paths:
            continue
        seen_paths.add(path_key)
        node = resolve_path(app, path)
        if node is not None and record_still_matches(
            node, record, window_bounds
        ):
            return node
    # Self-managed tables expose visible cells through Table.get_accessible_at
    # instead of child enumeration. render_visible_cells gives those records a
    # stable virtual suffix [row, col], so the action resolver must understand
    # the same address type. Otherwise the tool returns usable-looking indices
    # and refs that always fail as stale.
    if str(record.get("controlType") or "").lower() == "table cell":
        for path in paths:
            if len(path) < 3:
                continue
            container = resolve_path(app, path[:-2])
            table = (
                safe(container.get_table_iface)
                if container is not None
                else None
            )
            if table is None:
                continue
            try:
                row, col = int(path[-2]), int(path[-1])
            except (TypeError, ValueError):
                continue
            node = safe(
                lambda: Atspi.Table.get_accessible_at(table, row, col))
            if (
                node is not None
                and record_still_matches(node, record, window_bounds)
            ):
                return node
    return None


@dataclass(frozen=True)
class _SemanticLocator:
    """The fields of a semantic target that resolution reads."""

    selector: str
    role: str
    name_key: str
    has_name: bool
    predicates: list
    target_window_handle: str
    provider_bus: str
    provider_path: str
    provider_generation: str
    provider_window_bus: str
    provider_window_path: str
    provider_window_generation: str
    locator_kind: str
    allow_provider_rebind: bool
    provider_requested: bool
    provider_window_requested: bool


def resolve_semantic_target(
    app, window_index, window, bounds, target, known_refs, operation
):
    """Resolve one live semantic target across visible app-owned windows.

    The steps run in a fixed order and each may fail closed before input:
    decode the locator, choose the candidate windows, try the exact provider
    identity, scan the candidates for live matches, then require exactly one.
    """
    observation = operation.get("observation") or {}
    locator = _decode_semantic_target(target)
    text_limit = parse_text_limit(
        observation.get("text_limit"), DEFAULT_TEXT_LIMIT)
    candidates = [(window_index, window)]
    candidate_inventory_complete = True
    provider_window_recovered = False
    provider_scan_hint = None
    if locator.provider_window_requested:
        (
            candidates,
            candidate_inventory_complete,
            provider_window_recovered,
            provider_scan_hint,
        ) = _provider_window_candidates(
            app, window_index, window, locator, observation
        )
    elif locator.target_window_handle:
        (
            candidates,
            candidate_inventory_complete,
        ) = _logical_window_candidates(app, window_index, window, locator)

    if locator.locator_kind == "provider_bound":
        return _resolve_provider_bound_target(
            app,
            window_index,
            window,
            bounds,
            target,
            locator,
            candidates,
            candidate_inventory_complete,
            provider_scan_hint,
            observation,
            text_limit,
        )

    if locator.locator_kind == "semantic" and locator.provider_requested:
        resolved = _resolve_prior_provider_identity(
            app,
            window_index,
            window,
            bounds,
            target,
            locator,
            candidates,
            provider_scan_hint,
            observation,
            text_limit,
        )
        if resolved is not None:
            return resolved

    candidate_count = len(candidates)
    needs_provider_action_scan = any(
        str(predicate.get("kind") or "") == "action"
        for predicate in locator.predicates
    )
    if not needs_provider_action_scan:
        provider_matches = []
        by_name, by_description, scan_incomplete = _lightweight_semantic_scan(
            app,
            window_index,
            window,
            bounds,
            target,
            locator,
            candidates,
            observation,
            text_limit,
        )
    else:
        (
            provider_matches,
            by_name,
            by_description,
            scan_incomplete,
        ) = _render_semantic_scan(
            app,
            window_index,
            window,
            bounds,
            target,
            locator,
            candidates,
            known_refs,
            observation,
            text_limit,
        )

    resolved = _unique_provider_match(app, provider_matches)
    if resolved is not None:
        return resolved
    record, matched_window, matched_bounds, _candidate = _unique_semantic_match(
        by_name,
        by_description,
        scan_incomplete,
        locator.selector,
        candidate_count,
    )
    node = _live_node_for_semantic_match(
        app, target, locator, record, matched_bounds
    )
    note = _semantic_resolution_note(
        target, locator, candidate_count, provider_window_recovered
    )
    return node, record, note, matched_window, matched_bounds


def _decode_semantic_target(target):
    """Read the locator fields and reject a target that names nothing."""
    selector = str(target.get("selector") or "")
    role = str(target.get("role") or "").strip()
    name = str(target.get("name") or "")
    name_key = semantic_text_key(name)
    has_name = bool(target.get("hasName"))
    predicates = target.get("predicates") or []
    target_window_handle = str(target.get("windowHandle") or "").strip()
    provider_bus = str(target.get("providerBusName") or "").strip()
    provider_path = str(target.get("providerObjectPath") or "").strip()
    provider_generation = str(target.get("processGeneration") or "").strip()
    provider_window_bus = str(
        target.get("providerWindowBusName") or ""
    ).strip()
    provider_window_path = str(
        target.get("providerWindowObjectPath") or ""
    ).strip()
    provider_window_generation = str(
        target.get("providerWindowProcessGeneration") or ""
    ).strip()
    locator_kind = str(
        target.get("locatorKind") or "semantic"
    ).strip().lower()
    allow_provider_rebind = bool(target.get("allowProviderRebind", False))
    provider_requested = bool(
        provider_bus and provider_path and provider_generation
    )
    provider_window_requested = bool(
        provider_window_bus
        and provider_window_path
        and provider_window_generation
    )
    if not role and not has_name and not predicates:
        raise OperationFailure(
            "semantic_target_invalid reason=empty selector={!r}; "
            "no input was emitted.".format(selector),
            failure_code="semantic_target_invalid",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    if locator_kind == "provider_bound" and not provider_requested:
        raise OperationFailure(
            "semantic_target_invalid reason=provider_bound_identity_incomplete; "
            "no input was emitted.",
            failure_code="semantic_target_invalid",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    return _SemanticLocator(
        selector=selector,
        role=role,
        name_key=name_key,
        has_name=has_name,
        predicates=predicates,
        target_window_handle=target_window_handle,
        provider_bus=provider_bus,
        provider_path=provider_path,
        provider_generation=provider_generation,
        provider_window_bus=provider_window_bus,
        provider_window_path=provider_window_path,
        provider_window_generation=provider_window_generation,
        locator_kind=locator_kind,
        allow_provider_rebind=allow_provider_rebind,
        provider_requested=provider_requested,
        provider_window_requested=provider_window_requested,
    )


def _provider_window_candidates(app, window_index, window, locator, observation):
    """Narrow the candidates to the target's provider window.

    Returns (candidates, inventory_complete, window_recovered, scan_hint).
    The scan hint is set when the window was found through its element.
    """
    selector = locator.selector
    target_window_handle = locator.target_window_handle
    provider_bus = locator.provider_bus
    provider_path = locator.provider_path
    provider_generation = locator.provider_generation
    provider_window_bus = locator.provider_window_bus
    provider_window_path = locator.provider_window_path
    provider_window_generation = locator.provider_window_generation
    provider_requested = locator.provider_requested
    candidates = [(window_index, window)]
    provider_window_recovered = False
    provider_scan_hint = None
    seen_windows = {window_index}
    window_inventory = app_windows(app)
    candidate_inventory_complete = inventory_complete(window_inventory)
    for candidate_index, candidate_window in window_inventory:
        if candidate_index in seen_windows:
            continue
        seen_windows.add(candidate_index)
        candidates.append((candidate_index, candidate_window))
    all_candidates = candidates
    exact_provider_windows = [
        (candidate_index, candidate_window)
        for candidate_index, candidate_window in all_candidates
        if provider_bus_name(candidate_window) == provider_window_bus
        and provider_object_path(candidate_window) == provider_window_path
        and process_generation(node_pid(candidate_window))
        == provider_window_generation
    ]
    logical_window_matches = []
    if target_window_handle:
        logical_window_matches = [
            (candidate_index, candidate_window)
            for candidate_index, candidate_window in all_candidates
            if window_instance_handle(
                app, candidate_index, candidate_window
            ) == target_window_handle
            and provider_bus_name(candidate_window) == provider_window_bus
            and process_generation(node_pid(candidate_window))
            == provider_window_generation
        ]
    # A strong logical window is backed by a one-to-one native window
    # identity. Toolkits may rebuild the provider top-level object while
    # leaving that native window and the exact provider element alive.
    # In that case, allow the later exact-element identity scan to decide
    # continuity without requiring the obsolete wrapper bus/path.
    if (
        not logical_window_matches
        and target_window_handle.startswith("w2:")
        and provider_requested
    ):
        logical_window_matches = [
            (candidate_index, candidate_window)
            for candidate_index, candidate_window in all_candidates
            if window_instance_handle(
                app, candidate_index, candidate_window
            ) == target_window_handle
            and process_generation(node_pid(candidate_window))
            == provider_generation
        ]
    if (
        not exact_provider_windows
        and not logical_window_matches
        and provider_requested
    ):
        if not candidate_inventory_complete:
            raise OperationFailure(
                "observation_incomplete: the accessibility window inventory "
                "could not be read completely; no input was emitted.",
                failure_code="observation_incomplete",
                failure_phase="precondition",
                input_events_emitted=False,
                failure_subject="window",
            )
        if target_window_handle.startswith("w2:"):
            raise OperationFailure(
                "stale_element_ref reason=originating_window_disappeared "
                "selector={!r}; no input was emitted.".format(selector),
                failure_code="stale_element_ref",
                failure_phase="precondition",
                input_events_emitted=False,
                failure_subject="window",
                replacement_window=recovery_window_candidate(
                    app, window_inventory
                ),
            )
        provider_scan_hint = scan_provider_identity(
            all_candidates,
            provider_bus,
            provider_path,
            provider_generation,
            max_nodes=positive_int(
                observation.get("max_tree_nodes"), MAX_ELEMENTS
            ),
            deadline=time.monotonic() + TREE_DEADLINE_SECONDS,
        )
        if provider_scan_hint:
            owner_indexes = {
                path[0]
                for _node, path in provider_scan_hint
                if path
            }
            logical_window_matches = [
                (candidate_index, candidate_window)
                for candidate_index, candidate_window in all_candidates
                if candidate_index in owner_indexes
            ]
            provider_window_recovered = bool(logical_window_matches)
        elif not provider_scan_hint.complete:
            raise OperationFailure(
                "observation_incomplete: the provider identity could not be "
                "checked completely; no input was emitted.",
                failure_code="observation_incomplete",
                failure_phase="precondition",
                input_events_emitted=False,
                failure_subject="element",
            )
    if exact_provider_windows:
        candidates = exact_provider_windows
    elif logical_window_matches:
        # Provider object paths may change while the same one-to-one native
        # window remains alive. Semantic recovery stays inside that exact
        # stable window and still requires one unique canonical match.
        candidates = logical_window_matches
        provider_window_recovered = True
    else:
        if not candidate_inventory_complete:
            raise OperationFailure(
                "observation_incomplete: the accessibility window inventory "
                "could not be read completely; no input was emitted.",
                failure_code="observation_incomplete",
                failure_phase="precondition",
                input_events_emitted=False,
                failure_subject="window",
            )
        raise OperationFailure(
            "stale_element_ref reason=provider_window_disappeared "
            "selector={!r}; no input was emitted.".format(selector),
            failure_code="stale_element_ref",
            failure_phase="precondition",
            input_events_emitted=False,
            replacement_window=recovery_window_candidate(
                app, window_inventory
            ),
        )
    return (
        candidates,
        candidate_inventory_complete,
        provider_window_recovered,
        provider_scan_hint,
    )


def _logical_window_candidates(app, window_index, window, locator):
    """Narrow the candidates to the visible window with the target's handle."""
    selector = locator.selector
    target_window_handle = locator.target_window_handle
    candidates = [(window_index, window)]
    seen_windows = {window_index}
    window_inventory = app_windows(app)
    candidate_inventory_complete = inventory_complete(window_inventory)
    for candidate_index, candidate_window in window_inventory:
        if candidate_index in seen_windows:
            continue
        if not (
            state_contains(candidate_window, Atspi.StateType.SHOWING)
            or state_contains(candidate_window, Atspi.StateType.ACTIVE)
            or state_contains(candidate_window, Atspi.StateType.MODAL)
        ):
            continue
        seen_windows.add(candidate_index)
        candidates.append((candidate_index, candidate_window))
    candidates = [
        (candidate_index, candidate_window)
        for candidate_index, candidate_window in candidates
        if window_instance_handle(
            app, candidate_index, candidate_window
        ) == target_window_handle
    ]
    if not candidates:
        if not candidate_inventory_complete:
            raise OperationFailure(
                "observation_incomplete: the accessibility window inventory "
                "could not be read completely; no input was emitted.",
                failure_code="observation_incomplete",
                failure_phase="precondition",
                input_events_emitted=False,
                failure_subject="window",
            )
        raise OperationFailure(
            "stale_element_ref reason=originating_window_disappeared "
            "selector={!r}; no input was emitted.".format(selector),
            failure_code="stale_element_ref",
            failure_phase="precondition",
            input_events_emitted=False,
            replacement_window=recovery_window_candidate(
                app, window_inventory
            ),
        )
    return candidates, candidate_inventory_complete


def _resolve_provider_bound_target(
    app,
    window_index,
    window,
    bounds,
    target,
    locator,
    candidates,
    candidate_inventory_complete,
    provider_scan_hint,
    observation,
    text_limit,
):
    """Resolve a provider-bound ref by its exact provider identity only."""
    target_window_handle = locator.target_window_handle
    provider_bus = locator.provider_bus
    provider_path = locator.provider_path
    provider_generation = locator.provider_generation
    if not candidate_inventory_complete:
        raise OperationFailure(
            "observation_incomplete: the accessibility window inventory "
            "could not be read completely; no input was emitted.",
            failure_code="observation_incomplete",
            failure_phase="precondition",
            input_events_emitted=False,
            failure_subject="window",
        )
    provider_scan = provider_scan_hint or resolve_provider_runtime_paths(
        app, target, candidates
    )
    if not provider_scan:
        max_nodes = positive_int(
            observation.get("max_tree_nodes"), MAX_ELEMENTS
        )
        provider_scan = scan_provider_identity(
            candidates,
            provider_bus,
            provider_path,
            provider_generation,
            max_nodes=max_nodes,
            deadline=time.monotonic() + TREE_DEADLINE_SECONDS,
        )
        provider_scan = resolve_table_cell_from_owner(
            provider_scan, target
        )
    # A bus + object-path + process-generation tuple is already a unique
    # provider address. Once that exact object is found, unrelated sibling
    # read failures cannot make it ambiguous. Completeness is still
    # required before declaring the provider identity absent.
    if not provider_scan.complete and not provider_scan:
        raise OperationFailure(
            "observation_incomplete: the provider identity could not be "
            "checked completely; no input was emitted.",
            failure_code="observation_incomplete",
            failure_phase="precondition",
            input_events_emitted=False,
            failure_subject="element",
        )
    if not provider_scan:
        raise OperationFailure(
            "stale_element_ref reason=provider_identity_disappeared; "
            "no input was emitted.",
            failure_code="stale_element_ref",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    records = []
    nodes_by_path = {}
    bounds_by_window = {}
    windows_by_index = {
        candidate_index: candidate_window
        for candidate_index, candidate_window in candidates
    }
    for matched_node, path in provider_scan:
        matched_window = windows_by_index.get(path[0])
        matched_bounds = (
            bounds
            if path[0] == window_index and matched_window is window
            else extents(matched_window)
        )
        if matched_window is None or matched_bounds is None:
            raise OperationFailure(
                "observation_incomplete: the provider target window could "
                "not be sampled completely; no input was emitted.",
                failure_code="observation_incomplete",
                failure_phase="precondition",
                input_events_emitted=False,
                failure_subject="element",
            )
        record = provider_identity_record(
            matched_node, path, matched_bounds, text_limit
        )
        records.append(record)
        nodes_by_path[tuple(path)] = matched_node
        bounds_by_window[path[0]] = matched_bounds
    logical_window = target_window_handle or "provider-window"
    merged = deduplicate_provider_records(records, logical_window)
    if len(merged) != 1:
        raise OperationFailure(
            "stale_element_ref reason=provider_identity_changed; "
            "no input was emitted.",
            failure_code="stale_element_ref",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    record = merged[0]
    if not provider_identity_matches_target(record, target):
        raise OperationFailure(
            "stale_element_ref reason=provider_identity_changed; "
            "no input was emitted.",
            failure_code="stale_element_ref",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    resolved_path = tuple(record.get("runtimeId") or [])
    matched_node = nodes_by_path.get(resolved_path)
    matched_window = windows_by_index.get(resolved_path[0])
    matched_bounds = bounds_by_window.get(resolved_path[0])
    if matched_node is None or matched_window is None:
        raise OperationFailure(
            "observation_incomplete: the provider target could not be "
            "re-read consistently; no input was emitted.",
            failure_code="observation_incomplete",
            failure_phase="precondition",
            input_events_emitted=False,
            failure_subject="element",
        )
    return (
        matched_node,
        record,
        "[semantic-resolution] provider_identity=true unique=true; "
        "resolved immediately before input.",
        matched_window,
        matched_bounds,
    )


def _resolve_prior_provider_identity(
    app,
    window_index,
    window,
    bounds,
    target,
    locator,
    candidates,
    provider_scan_hint,
    observation,
    text_limit,
):
    """Resolve a semantic ref through the provider object it last named.

    Returns None when semantic matching should decide instead: the object is
    gone or changed and the target allows a provider rebind.
    """
    provider_bus = locator.provider_bus
    provider_path = locator.provider_path
    provider_generation = locator.provider_generation
    locator_kind = locator.locator_kind
    allow_provider_rebind = locator.allow_provider_rebind
    max_nodes = positive_int(
        observation.get("max_tree_nodes"), MAX_ELEMENTS
    )
    provider_scan = provider_scan_hint or resolve_provider_runtime_paths(
        app, target, candidates
    )
    if not provider_scan:
        provider_scan = scan_provider_identity(
            candidates,
            provider_bus,
            provider_path,
            provider_generation,
            max_nodes=max_nodes,
            deadline=time.monotonic() + TREE_DEADLINE_SECONDS,
        )
    if not provider_scan.complete and not provider_scan:
        raise OperationFailure(
            "observation_incomplete: the prior provider identity could not "
            "be checked completely; no input was emitted.",
            failure_code="observation_incomplete",
            failure_phase="precondition",
            input_events_emitted=False,
            failure_subject="element",
        )
    if not provider_scan:
        if not allow_provider_rebind:
            raise OperationFailure(
                "stale_element_ref reason=provider_identity_replaced; "
                "no input was emitted.",
                failure_code="stale_element_ref",
                failure_phase="precondition",
                input_events_emitted=False,
            )
    else:
        matched_node, resolved_path = provider_scan[0]
        windows_by_index = {
            candidate_index: candidate_window
            for candidate_index, candidate_window in candidates
        }
        matched_window = windows_by_index.get(resolved_path[0])
        matched_bounds = (
            bounds
            if resolved_path[0] == window_index and matched_window is window
            else extents(matched_window)
        )
        if matched_window is None or matched_bounds is None:
            raise OperationFailure(
                "observation_incomplete: the prior provider target window could "
                "not be sampled completely; no input was emitted.",
                failure_code="observation_incomplete",
                failure_phase="precondition",
                input_events_emitted=False,
                failure_subject="element",
            )
        record = provider_identity_record(
            matched_node, resolved_path, matched_bounds, text_limit
        )
        if not provider_identity_matches_target(record, target) and not (
            allow_provider_rebind and locator_kind == "semantic"
        ):
            raise OperationFailure(
                "stale_element_ref reason=provider_identity_changed; "
                "no input was emitted.",
                failure_code="stale_element_ref",
                failure_phase="precondition",
                input_events_emitted=False,
            )
        if provider_identity_matches_target(record, target):
            return (
                matched_node,
                record,
                "[semantic-resolution] provider_identity=true unique=true; "
                "resolved immediately before input.",
                matched_window,
                matched_bounds,
            )
    return None


def _lightweight_semantic_scan(
    app,
    window_index,
    window,
    bounds,
    target,
    locator,
    candidates,
    observation,
    text_limit,
):
    """Match the target without rendering full records for non-matches.

    Returns (by_name, by_description, scan_incomplete).
    """
    name_key = locator.name_key
    has_name = locator.has_name
    target_window_handle = locator.target_window_handle
    by_name = []
    by_description = []
    scan_incomplete = False
    lightweight_candidates = []
    for candidate_index, candidate_window in candidates:
        candidate_bounds = (
            bounds
            if candidate_index == window_index and candidate_window is window
            else extents(candidate_window)
        )
        if candidate_bounds is None:
            scan_incomplete = True
            continue
        lightweight_candidates.append(
            (candidate_index, candidate_window, candidate_bounds)
        )
    semantic_scan = scan_semantic_target(
        lightweight_candidates,
        target,
        max_nodes=positive_int(
            observation.get("max_tree_nodes"), MAX_ELEMENTS
        ),
        deadline=time.monotonic() + TREE_DEADLINE_SECONDS,
        text_limit=text_limit,
    )
    if not semantic_scan.complete:
        scan_incomplete = True
    grouped: dict[int, dict[str, Any]] = {}
    for record, candidate_window, candidate_bounds in semantic_scan:
        runtime_id = list(record.get("runtimeId") or [])
        candidate_index = runtime_id[0] if runtime_id else window_index
        window_group: dict[str, Any] = grouped.setdefault(
            candidate_index,
            {
                "window": candidate_window,
                "bounds": candidate_bounds,
                "records": [],
            },
        )
        window_group["records"].append(record)
    semantic_records = []
    for candidate_index, window_group in grouped.items():
        logical_window = (
            target_window_handle
            or safe(
                lambda: window_instance_handle(
                    app, candidate_index, window_group["window"]
                ),
                "",
            )
            or "window-index:{}".format(candidate_index)
        )
        records = deduplicate_provider_records(
            window_group["records"], logical_window
        )
        if any(
            record.get("_providerIdentityConflict")
            for record in records
        ):
            scan_incomplete = True
        for record in records:
            semantic_records.append(
                (
                    record,
                    window_group["window"],
                    window_group["bounds"],
                    records,
                )
            )
    for (
        record,
        candidate_window,
        candidate_bounds,
        sibling_records,
    ) in semantic_records:
        match_group = "name"
        if has_name and (
            semantic_text_key(record.get("name")) != name_key
        ):
            match_group = "description"
        destination = (
            by_name if match_group == "name" else by_description
        )
        candidate_payload = None
        if len(destination) < 5:
            candidate_index = int(
                (record.get("runtimeId") or [window_index])[0]
            )
            candidate_payload = ambiguity_candidate(
                record,
                sibling_records,
                app,
                candidate_index,
                candidate_window,
            )
        destination.append(
            (
                record,
                candidate_window,
                candidate_bounds,
                candidate_payload,
            )
        )
    return by_name, by_description, scan_incomplete


def _render_semantic_scan(
    app,
    window_index,
    window,
    bounds,
    target,
    locator,
    candidates,
    known_refs,
    observation,
    text_limit,
):
    """Match the target against fully rendered canonical records.

    Action predicates need the rendered records. Returns
    (provider_matches, by_name, by_description, scan_incomplete).
    """
    role = locator.role
    name_key = locator.name_key
    has_name = locator.has_name
    target_window_handle = locator.target_window_handle
    provider_bus = locator.provider_bus
    provider_path = locator.provider_path
    provider_generation = locator.provider_generation
    provider_requested = locator.provider_requested
    indexer = StableIndexer(known_refs)
    provider_matches = []
    by_name = []
    by_description = []
    scan_incomplete = False
    for candidate_index, candidate_window in candidates:
        candidate_bounds = (
            bounds
            if candidate_index == window_index and candidate_window is window
            else extents(candidate_window)
        )
        if candidate_bounds is None:
            scan_incomplete = True
            continue
        records, _lines, annotations = render_tree(
            candidate_window,
            candidate_bounds,
            [candidate_index],
            text_limit=text_limit,  # pyright: ignore[reportArgumentType]
            max_tree_nodes=positive_int(
                observation.get("max_tree_nodes"), MAX_ELEMENTS),
            max_tree_depth=positive_int(
                observation.get("max_tree_depth"), MAX_DEPTH),
            # Model-facing projection must never decide action truth. Find,
            # refs and actions all resolve against canonical provider records.
            prune=False,
            boxes=False,
            indexer=indexer,
        )
        # Any provider omission means the canonical scan did not prove the
        # target absent. The reason taxonomy is diagnostic-only and must not
        # decide action truth.
        if any(
            annotation.get("kind") == "provider_omission"
            for annotation in annotations
        ):
            scan_incomplete = True
        logical_window = (
            target_window_handle
            or safe(
                lambda: window_instance_handle(
                    app, candidate_index, candidate_window
                ),
                "",
            )
            or "window-index:{}".format(candidate_index)
        )
        records = deduplicate_provider_records(
            records,
            logical_window,
        )
        if any(
            record.get("_providerIdentityConflict")
            for record in records
        ):
            scan_incomplete = True
        for record in records:
            if (
                provider_requested
                and str(record.get("providerBusName") or "") == provider_bus
                and str(record.get("providerObjectPath") or "") == provider_path
                and str(record.get("processGeneration") or "")
                == provider_generation
            ):
                provider_matches.append(
                    (record, candidate_window, candidate_bounds)
                )
            live_role = str(record.get("controlType") or "")
            if not semantic_role_matches(
                live_role,
                role,
                target.get("providerRoleAliases"),
            ):
                continue

            match_group = "name"
            if has_name:
                live_name = str(record.get("name") or "")
                if semantic_text_key(live_name) == name_key:
                    match_group = "name"
                else:
                    live_description = str(record.get("description") or "")
                    if semantic_text_key(live_description) != name_key:
                        continue
                    match_group = "description"

            if not semantic_target_predicates_match(record, target):
                continue
            destination = (
                by_name if match_group == "name" else by_description
            )
            candidate_payload = None
            if len(destination) < 5:
                candidate_payload = ambiguity_candidate(
                    record,
                    records,
                    app,
                    candidate_index,
                    candidate_window,
                )
            destination.append(
                (
                    record,
                    candidate_window,
                    candidate_bounds,
                    candidate_payload,
                )
            )
    return provider_matches, by_name, by_description, scan_incomplete


def _unique_provider_match(app, provider_matches):
    """Return the resolution for one exact provider match, or None."""
    if len(provider_matches) > 1:
        raise OperationFailure(
            "provider_identity_ambiguous; no input was emitted.",
            failure_code="semantic_target_ambiguous",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    if len(provider_matches) == 1:
        record, matched_window, matched_bounds = provider_matches[0]
        node = find_element_at_snapshot_path_with_bounds(
            app, record, matched_bounds
        )
        if node is not None:
            note = (
                "[semantic-resolution] provider_identity=true unique=true "
                "resolved immediately before input."
            )
            return (
                node,
                record,
                note,
                matched_window,
                matched_bounds,
            )
    return None


def _unique_semantic_match(
    by_name, by_description, scan_incomplete, selector, candidate_count
):
    """Return the one match; name matches outrank description matches."""
    matches = by_name if by_name else by_description
    if not matches:
        if scan_incomplete:
            raise OperationFailure(
                "observation_incomplete reason=semantic_target_scan_incomplete; "
                "no input was emitted.",
                failure_code="observation_incomplete",
                failure_phase="precondition",
                input_events_emitted=False,
                failure_subject="element",
            )
        raise OperationFailure(
            "semantic_target_not_found selector={!r}; searched {} visible "
            "top-level window(s) owned by the application and found no matching "
            "live element; no input was emitted.".format(
                selector, candidate_count),
            failure_code="semantic_target_not_found",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    if len(matches) != 1:
        candidates_payload = [
            match[3] for match in matches if match[3] is not None
        ][:5]
        raise OperationFailure(
            "semantic_target_ambiguous selector={!r} matches at least {} live "
            "elements; exactly one is required and no input was emitted.".format(
                selector, len(matches)),
            failure_code="semantic_target_ambiguous",
            failure_phase="precondition",
            input_events_emitted=False,
            candidates=candidates_payload,
            candidate_count=len(matches),
        )
    if scan_incomplete:
        raise OperationFailure(
            "observation_incomplete reason=semantic_target_scan_incomplete; "
            "no input was emitted.",
            failure_code="observation_incomplete",
            failure_phase="precondition",
            input_events_emitted=False,
            failure_subject="element",
        )
    return matches[0]


def _live_node_for_semantic_match(app, target, locator, record, matched_bounds):
    """Return the live node of the match, checking rebind lineage first."""
    allow_provider_rebind = locator.allow_provider_rebind
    provider_requested = locator.provider_requested
    if allow_provider_rebind and provider_requested:
        permitted_paths = [target.get("runtimeId") or []]
        permitted_paths.extend(target.get("runtimeAliases") or [])
        live_path = list(record.get("runtimeId") or [])
        if live_path not in permitted_paths:
            raise OperationFailure(
                "stale_element_ref reason=provider_identity_replaced_outside_"
                "runtime_lineage; no input was emitted.",
                failure_code="stale_element_ref",
                failure_phase="precondition",
                input_events_emitted=False,
            )
    node = record.pop("_resolvedNode", None)
    if node is None:
        node = find_element_at_snapshot_path_with_bounds(
            app, record, matched_bounds)
    if node is None:
        raise OperationFailure(
            "stale_element_ref reason=live_semantic_record_changed_before_input "
            "no input was emitted; refresh AX mode=observe/find and use the "
            "newly returned public ref.",
            failure_code="stale_element_ref",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    return node


def _semantic_resolution_note(
    target, locator, candidate_count, provider_window_recovered
):
    selector = locator.selector
    target_window_handle = locator.target_window_handle
    scope_note = " window_scope=verified" if target_window_handle else ""
    source_note = ""
    if target.get("sourceIndex") is not None:
        source_note = " source=current_snapshot_index index={}".format(
            target.get("sourceIndex")
        )
    note = (
        "[semantic-resolution] fresh=true unique=true selector={!r}; searched "
        "{} visible app-owned window(s){}{}{} and resolved inside the action "
        "runtime immediately before input."
    ).format(
        selector,
        candidate_count,
        scope_note,
        source_note,
        (
            " provider_window_recovered=true"
            if provider_window_recovered
            else ""
        ),
    )
    return note
