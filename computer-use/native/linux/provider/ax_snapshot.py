"""Linux provider module: ax snapshot."""

import time
from .foundation import (
    Atspi,
    BUDGET_PRESSURE_RATIO,
    DEFAULT_TEXT_LIMIT,
    MAX_CHILD_FANOUT,
    MAX_DEPTH,
    MAX_ELEMENTS,
    MAX_RENDERED_CHARS,
    MENU_ROLES,
    TREE_DEADLINE_SECONDS,
    is_interactive_role,
    node_name,
    node_pid,
    node_role,
    process_generation,
    provider_bus_name,
    provider_object_path,
    safe,
    should_enumerate_children,
    state_contains,
    strict_prune_probe,
)

from .ax_node_properties import (
    render_element_line,
)
from .ax_records import (
    record_for,
)
from .ax_virtual_table import (
    render_visible_cells,
)


def render_tree(root, window_bounds, root_path, text_limit=DEFAULT_TEXT_LIMIT,
                boxes=True, indexer=None,
                max_tree_nodes=MAX_ELEMENTS, max_tree_depth=MAX_DEPTH, prune=True,
                max_rendered_chars=MAX_RENDERED_CHARS,
                deadline_seconds=TREE_DEADLINE_SECONDS,
                include_annotations=False):
    records = []
    lines = []
    annotations = []
    no_child_count_hint = object()
    unknown_child_count = object()
    provider_probe_errors = {}

    # Count each omission cause separately: the report must give the remedy
    # that matches the cause, which a merged count cannot do.
    dropped = {"count": 0, "chars": 0, "time": 0, "depth": 0,
               "budget": 0, "filtered": 0, "pressure": 0,
               "render_error": 0, "fanout": 0}
    budget = {"chars": 0}
    deadline = time.monotonic() + deadline_seconds if deadline_seconds else None
    pressure_at = max(1, int(max_tree_nodes * BUDGET_PRESSURE_RATIO))

    def record_provider_errors(errors):
        for field in errors:
            provider_probe_errors[field] = (
                provider_probe_errors.get(field, 0) + 1
            )

    def strict_child_count(node):
        try:
            return max(0, int(node.get_child_count() or 0))
        except Exception as exc:
            record_provider_errors(
                {"child_count": type(exc).__name__}
            )
            return unknown_child_count

    def child_entries(node, count_hint=no_child_count_hint):
        """Return a lazy bounded child iterator plus enumerability metadata."""
        count = (
            strict_child_count(node)
            if count_hint is no_child_count_hint
            else count_hint
        )
        if count is unknown_child_count:
            if state_contains(node, Atspi.StateType.MANAGES_DESCENDANTS):
                return iter(()), False, None

            def unknown_children():
                consecutive_errors = 0
                reached_cap = True
                for child_index in range(MAX_CHILD_FANOUT):
                    try:
                        child = node.get_child_at_index(child_index)
                    except IndexError:
                        reached_cap = False
                        break
                    except Exception as exc:
                        record_provider_errors(
                            {"child_at": type(exc).__name__}
                        )
                        consecutive_errors += 1
                        yield child_index, None
                        if consecutive_errors >= 8:
                            reached_cap = False
                            break
                        continue
                    consecutive_errors = 0
                    if child is None:
                        reached_cap = False
                        break
                    yield child_index, child
                if reached_cap:
                    record_provider_errors(
                        {"unknown_fanout": "bounded"}
                    )

            return unknown_children(), True, None

        if not should_enumerate_children(node, count):
            return iter(()), False, count
        fanout = min(count, MAX_CHILD_FANOUT)
        if count > fanout:
            excess = count - fanout
            dropped["count"] += excess
            dropped["fanout"] += excess

        def known_children():
            for child_index in range(fanout):
                try:
                    child = node.get_child_at_index(child_index)
                except Exception as exc:
                    record_provider_errors(
                        {"child_at": type(exc).__name__}
                    )
                    yield child_index, None
                    continue
                if child is None:
                    record_provider_errors({"child_at_none": "missing"})
                yield child_index, child

        return known_children(), True, fanout

    def is_structural_filler(record):
        """Return whether the record is a pure layout container.

        Such a container has no semantics, no state and nothing to act on.
        """
        return not (
            record["name"]
            or record["description"]
            or record["actions"]
            or record["value"]
            or record["placeholder"]
            or record["states"]
            or is_interactive_role(record["controlType"])
        )

    # Text-only roles: not actionable; their only value is the text they
    # carry.
    TEXT_ONLY_ROLES = {"static", "label", "paragraph", "heading", "caption"}

    def visit_impl(node, depth, path, render_depth=0, parent_text=""):
        """Visit one node; `depth` drives the budget, `render_depth` indents.

        The two must differ because pruned nodes still recurse into their
        children. Indenting by `depth` would leave children at their pruned
        parent's level and open gaps in the tree (on Nautilus, indentation
        jumped from level 1 to level 6). `render_depth` advances only when a
        node is actually rendered.
        """
        if node is None:
            return
        if len(records) >= max_tree_nodes:
            if node is not None:
                dropped["count"] += 1
                dropped["budget"] += 1
            return
        if depth > max_tree_depth:
            dropped["count"] += 1
            dropped["depth"] += 1
            return
        # The character budget is separate from the node budget; see
        # MAX_RENDERED_CHARS.
        if budget["chars"] >= max_rendered_chars:
            dropped["count"] += 1
            dropped["chars"] += 1
            return
        # The time budget is separate from both; see TREE_DEADLINE_SECONDS.
        # The root is exempt: an empty tree has no addressable element, and
        # the caller could not even tell which window it observed.
        if deadline is not None and records and time.monotonic() >= deadline:
            dropped["count"] += 1
            dropped["time"] += 1
            return

        child_count_hint = no_child_count_hint

        # Pruning keeps only nodes with an interactive role that are visible
        # on screen; in offline replay this reduced trees to 22% of their size
        # while keeping every action target. Only the node's own line is
        # dropped and its children are still visited: intermediate containers
        # are often the parents of valuable controls, so cutting the subtree
        # would backfire.
        #
        # The decision reads only the fields it uses (strict_prune_probe)
        # instead of building the full record first. On GIMP (GAIL), a full
        # record_for cost 8.45 ms per node; one render_tree call built 3162
        # records but kept 157, so 95% of the cost (87% of rendering time) was
        # thrown away and the tree took 38 s, beyond the Go layer's 30 s
        # timeout. The probe fields cost about 2.3 ms; states (2.52 ms),
        # actions (1.78 ms), value (0.92 ms), placeholder (0.58 ms) and
        # automationId (0.57 ms) are read only for kept nodes. Kept nodes read
        # the probe fields again, which is negligible at a 5% survival rate.
        if prune and depth > 0:
            probe, probe_errors = strict_prune_probe(
                node, window_bounds, text_limit
            )
            record_provider_errors(probe_errors)
            child_count_hint = probe["child_count"]
            if (
                child_count_hint is None
                and "child_count" in probe_errors
            ):
                child_count_hint = unknown_child_count
            probe_frame = probe["frame"]
            probe_role = probe["role"]
            # Keep every visible named node, even with a non-interactive role.
            # The toggle button of LibreOffice's line-spacing combo has no
            # name and can be identified only through its parent, the
            # `Line Spacing` panel. A pure role allowlist would drop that
            # panel: the target stays in the tree but can no longer be
            # identified, which breaks the whole dialog flow. A retention
            # metric that only checks whether the target is present cannot
            # see this failure.
            probe_name = probe["name"]
            keeps = bool(probe_errors) or (
                probe_frame is not None and (
                    is_interactive_role(probe_role)
                    or bool(probe_name)
                    or bool(probe["description"])
                )
            )
            # Drop text-only leaves whose text repeats the parent's verbatim.
            #
            # Keeping every visible named node suits desktop dialogs (see the
            # Line Spacing case above), but on web pages every text node is
            # named. On chrome://flags, 212 of 521 text nodes (41%) repeated
            # their parent's text word for word, adding no information at the
            # same byte cost.
            #
            # Only childless text roles are dropped: a container may still be
            # the only way to identify an unnamed control.
            if (keeps and probe_role in TEXT_ONLY_ROLES and probe_name
                    and parent_text and probe_name == parent_text
                    and child_count_hint == 0):
                keeps = False
            if not keeps:
                dropped["count"] += 1
                dropped["filtered"] += 1
                children, _, _ = child_entries(
                    node, child_count_hint)
                for child_index, child in children:
                    if child is None:
                        continue
                    # This node is not rendered, so render_depth stays and the
                    # children take its place.
                    visit(child, depth + 1,
                          path + [child_index], render_depth, parent_text)
                return

        record = record_for(node, None, path, window_bounds, text_limit=text_limit)

        # Under budget pressure, keep named, actionable and valued nodes by
        # dropping structural containers first. Only the container's own line
        # is dropped and its children are still visited, because a dropped
        # container is often the parent of valuable controls.
        if (
            prune
            and len(records) >= pressure_at
            and is_structural_filler(record)
            and depth > 0
        ):
            dropped["count"] += 1
            dropped["pressure"] += 1
            children, _, _ = child_entries(node, child_count_hint)
            for child_index, child in children:
                if child is None:
                    continue
                visit(child, depth + 1,
                      path + [child_index], render_depth, "")
            return

        # Indices must survive across snapshots, so they cannot be traversal
        # ordinals such as len(records): inserting one element would shift
        # every later index. See StableIndexer.
        #
        # Assign the index only once the node is known to be kept. Assigning
        # it before pruning consumed indices for discarded nodes that never
        # reached records or refs (one Nautilus snapshot of 91 elements used
        # indices spread over 0..670). Pruned nodes are not in `known`, so
        # they took fresh indices on every snapshot and the numbers grew
        # steadily over a session.
        record["index"] = (indexer.index_for(path, record["controlType"],
                                             record["name"],
                                             record.get("nameHash", ""))
                           if indexer is not None else len(records))
        records.append(record)

        rendered = render_element_line(record, render_depth, boxes=boxes)
        lines.append(rendered)
        budget["chars"] += len(rendered) + 1
        role = record["localizedControlType"] or record["controlType"] or "element"

        # Collapsed menu: keep the node itself, which is still the semantic
        # target of a plain click, but do not descend into its items. Role
        # and state come from the libatspi cache, so this costs no D-Bus
        # round trip.
        if (
            depth > 0
            and role.lower() in MENU_ROLES
            and not state_contains(node, Atspi.StateType.SHOWING)
        ):
            pending = (
                strict_child_count(node)
                if child_count_hint is no_child_count_hint
                else child_count_hint
            )
            if pending is unknown_child_count:
                pending = 0
            if pending:
                annotations.append({
                    "parentRuntimeId": path[:],
                    "kind": "hidden_children",
                    "count": int(pending),
                    "reason": "collapsed_menu",
                })
                lines.append(
                    ("\t" * (render_depth + 2))
                    + "(hidden-items={}; open menu, then observe)".format(
                        pending)
                )
            return

        children, children_enumerable, planned_children = child_entries(
            node, child_count_hint
        )
        if not children_enumerable:
            # Huge or self-managing containers cannot be enumerated; address
            # the cells in the current viewport by coordinate instead.
            rendered = render_visible_cells(
                node, render_depth, path, window_bounds, records, lines,
                text_limit=text_limit, max_tree_nodes=max_tree_nodes,
                boxes=boxes,
                # Pass the indexer explicitly: the cell renderer is a
                # module-level function and cannot see this local. Without
                # it, cells fall back to traversal ordinals whose indices are
                # not stable across snapshots. This is the rendering path for
                # LibreOffice Calc sheets, the fallback once a self-managing
                # container refuses enumeration.
                indexer=indexer,
                annotations=annotations,
            )
            if rendered == 0:
                # If cell addressing fails too, say so, so the model does not
                # assume the container is empty.
                lines.append(
                    ("\t" * (render_depth + 2))
                    + "(contents not enumerated: {} manages its own descendants "
                      "and cell addressing failed)".format(role)
                )
            return

        # Check the node budget before each child_at() call. visit() also
        # checks it and returns at once, but the loop itself would not stop:
        # for a container that misreports a billion children, the child_at()
        # round trips that are bound to be discarded would alone take tens of
        # hours. Unlike should_enumerate_children(), which relies on the
        # container declaring MANAGES_DESCENDANTS, this guard relies on no
        # declaration.
        before_records = len(records)
        before_lines = len(lines)
        child_iterator = iter(children)
        child_slots_seen = 0
        while True:
            if len(records) >= max_tree_nodes:
                remaining = (
                    max(1, planned_children - child_slots_seen)
                    if planned_children is not None
                    else 1
                )
                dropped["count"] += remaining
                dropped["budget"] += remaining
                break
            try:
                child_index, child = next(child_iterator)
            except StopIteration:
                break
            child_slots_seen += 1
            if child is None:
                continue
            # This node was rendered: indent its children one level deeper and
            # pass its text down so they can detect verbatim repetition.
            visit(child, depth + 1, path + [child_index], render_depth + 1,
                  str(record.get("name") or record.get("value") or ""))

        # If a menu rendered none of its children, report that on the menu
        # itself.
        #
        # The collapsed-menu check above requires the menu itself to lack
        # SHOWING, which GIMP (GAIL) does not satisfy: the File menu is
        # SHOWING on the menu bar while its 39 items are not. Every item is
        # then pruned, and the agent sees a bare `menu "File"` line that looks
        # exactly like an empty menu, so it cannot find Export or Save As
        # although they are one click away. The global omission count at the
        # end of the tree does not help, because it does not say which menu
        # hid items.
        #
        # This check therefore looks at the outcome instead of a state bit:
        # if the menu produced no rendered line while the provider reported
        # children, state how many items are hidden.
        if (child_slots_seen and len(records) == before_records
                and len(lines) == before_lines
                and role.lower() in MENU_ROLES):
            lines.append(
                ("\t" * (render_depth + 2))
                + "(hidden-items={}; open menu, then observe)".format(
                    child_slots_seen)
            )
            annotations.append({
                "parentRuntimeId": path[:],
                "kind": "hidden_children",
                "count": int(child_slots_seen),
                "reason": "provider_children_not_rendered",
            })

    def visit(node, depth, path, render_depth=0, parent_text=""):
        records_before = len(records)
        lines_before = len(lines)
        chars_before = budget["chars"]
        try:
            return visit_impl(
                node, depth, path, render_depth, parent_text
            )
        except Exception:
            # Roll back any partial records/lines from this node's subtree. Stable
            # index numbers consumed during the failed attempt may remain unused,
            # but no hidden actionable ref is published.
            del records[records_before:]
            del lines[lines_before:]
            budget["chars"] = chars_before
            if node is None:
                return
            dropped["count"] += 1
            dropped["render_error"] += 1
            if len(records) >= max_tree_nodes:
                dropped["budget"] += 1
                return
            role = str(safe(lambda: node_role(node), "element") or "element")
            deadline_expired = (
                deadline is not None and time.monotonic() >= deadline)
            collapsed_menu = (
                depth > 0
                and role.lower() in MENU_ROLES
                and not state_contains(node, Atspi.StateType.SHOWING)
            )
            children = []
            children_allowed = False
            if not deadline_expired and not collapsed_menu:
                children, children_allowed, _ = child_entries(node)
            if deadline_expired:
                child_status = "subtree not traversed because the time budget expired"
            elif collapsed_menu:
                child_status = (
                    "subtree not traversed because the menu is collapsed"
                )
            elif not children_allowed:
                child_status = (
                    "subtree not traversed because the container is not safely enumerable"
                )
            else:
                child_status = "eligible healthy children follow"
            diagnostic = (
                ("\t" * (render_depth + 1))
                + "(accessibility node unavailable; no public ref was issued; {})"
                .format(child_status)
            )
            if budget["chars"] < max_rendered_chars:
                remaining = max_rendered_chars - budget["chars"]
                diagnostic = diagnostic[:remaining]
                if diagnostic:
                    lines.append(diagnostic)
                    budget["chars"] += len(diagnostic) + 1
            if deadline_expired:
                dropped["time"] += 1
                return
            if not children_allowed:
                return
            child_iterator = iter(children)
            while True:
                if len(records) >= max_tree_nodes:
                    dropped["count"] += 1
                    dropped["budget"] += 1
                    break
                try:
                    child_index, child = next(child_iterator)
                except StopIteration:
                    break
                if child is None:
                    continue
                visit(
                    child,
                    depth + 1,
                    path + [child_index],
                    render_depth,
                    parent_text,
                )

    visit(root, 0, root_path)
    if dropped["count"]:
        # The omission summary keeps causes, counts and recovery paths but
        # does not repeat the whole tutorial in every snapshot: the tool
        # description carries the full semantics, and this summary reports
        # what this particular snapshot is missing.
        reasons = []
        if dropped["time"]:
            reasons.append(
                "{} not visited: time budget exhausted ({:.0f}s); tree is partial; "
                "raising node limits does not fix this".format(
                    dropped["time"], deadline_seconds))
        if dropped["chars"]:
            reasons.append(
                "{} not rendered: the server-owned character budget was exhausted "
                "({} chars); use AX mode=find or a rooted observation for the needed "
                "branch".format(
                    dropped["chars"], max_rendered_chars))
        if dropped["budget"]:
            reasons.append(
                "{} not traversed: the server-owned node budget filled up "
                "({} canonical records); use AX mode=find or a rooted observation "
                "instead of requesting a larger whole-window tree".format(
                    dropped["budget"], max_tree_nodes))
        if dropped["depth"]:
            reasons.append(
                "{} subtree root(s) not traversed: the server-owned depth budget "
                "({}) was reached. Their descendants are absent from this snapshot; "
                "AX mode=find cannot discover nodes the provider did not expose".format(
                    dropped["depth"], max_tree_depth))
        if dropped["filtered"]:
            reasons.append(
                "{} filtered: not interactable and/or not on screen "
                "(rendered {}/{}); AX mode=find searches canonical records hidden "
                "only by the working projection, and a rooted observation can return "
                "local context".format(
                    dropped["filtered"], len(records), max_tree_nodes))
        if dropped["pressure"]:
            reasons.append(
                "{} unnamed layout containers dropped after {:.0%} node pressure; "
                "children remain listed".format(
                    dropped["pressure"], BUDGET_PRESSURE_RATIO))
        if dropped["render_error"]:
            reasons.append(
                "{} node(s) lost to isolated per-node exception; no stable ref; "
                "sibling and eligible-child traversal continued; budget-stopped roots "
                "reported separately".format(
                    dropped["render_error"]))
        if dropped["fanout"]:
            reasons.append(
                "{} child root(s) not traversed because a container exceeded "
                "MAX_CHILD_FANOUT={}; omitted subtrees are absent from this snapshot"
                .format(dropped["fanout"], MAX_CHILD_FANOUT))
        lines.append("({} node(s) omitted — {})".format(
            dropped["count"], "; ".join(reasons) if reasons else "cause not recorded"))
        annotations.append({
            "parentRuntimeId": root_path[:],
            "kind": "provider_omission",
            "count": int(dropped["count"]),
            "reason": ",".join(
                key for key in (
                    "time", "chars", "budget", "depth", "filtered",
                    "pressure", "render_error", "fanout",
                ) if dropped[key]
            ),
            "detail": "; ".join(reasons) if reasons else "cause not recorded",
        })
    if provider_probe_errors:
        error_count = sum(provider_probe_errors.values())
        annotations.append({
            "parentRuntimeId": root_path[:],
            "kind": "provider_omission",
            "count": int(error_count),
            "reason": "provider_probe_error",
            "detail": (
                "accessibility provider reads were incomplete; missing "
                "descendants are unknown"
            ),
        })
    window_bus = provider_bus_name(root)
    window_path = provider_object_path(root)
    window_generation = process_generation(node_pid(root))
    window_role = node_role(root)
    window_name = node_name(root)
    for record in records:
        record["providerWindowBusName"] = window_bus
        record["providerWindowObjectPath"] = window_path
        record["providerWindowProcessGeneration"] = window_generation
        record["providerWindowRole"] = window_role
        record["providerWindowName"] = window_name

    if include_annotations:
        return records, lines, annotations
    return records, lines
