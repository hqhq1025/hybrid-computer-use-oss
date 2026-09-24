"""Canonical AX records and stable local indexing."""

from .foundation import (
    Atspi,  # pyright: ignore[reportAttributeAccessIssue]
    DEFAULT_TEXT_LIMIT,
    limit_text,
    node_name,
    node_pid,
    node_role,
    process_generation,
    provider_bus_name,
    provider_object_path,
    safe,
    state_value,
)
from .x11_windows import relative_frame
from .primitives import name_fingerprint
from .ax_node_properties import (
    accessible_id,
    element_value,
    node_actions,
    node_description,
    placeholder_text,
    state_segment,
    text_attributes,
)

class StableIndexer:
    """Assign element indices that survive across snapshots.

    Adapted from Playwright's ariaSnapshot.ts (Apache-2.0): an index is not a
    traversal ordinal but a cached id per element, reissued only when the
    element's role or name changes. An element whose role, name and full-name
    hash are unchanged keeps the same index in every snapshot.

    The provider keeps no per-element state across snapshots (its process can
    be restarted), so the Go side, which keeps the previous snapshot, passes
    in its `path -> (index, role, name)` mapping instead.

    Without stable indices, a tree reordered by an opening dialog makes an old
    index act on a different control without any error. With them, controls
    of the main window get their previous indices back once the dialog
    closes, so an old reference is either still correct or reported missing.
    """

    def __init__(self, known):
        # known: {"0.1.2": {"index": 7, "role": "push button", "name": "Save"}}
        self.known = known or {}
        self.next_free = 0
        for entry in self.known.values():
            try:
                self.next_free = max(self.next_free, int(entry.get("index", 0)) + 1)
            except (TypeError, ValueError):
                pass
        self.used = set()

    def index_for(self, path, role, name, name_hash=""):
        key = ".".join(str(p) for p in path)
        entry = self.known.get(key)
        if (
            entry is not None
            and entry.get("role") == role
            and entry.get("name") == name
            and str(entry.get("nameHash") or "") == str(name_hash or "")
        ):
            index = entry.get("index")
            if isinstance(index, int) and index not in self.used:
                self.used.add(index)
                return index
        while self.next_free in self.used:
            self.next_free += 1
        index = self.next_free
        self.used.add(index)
        self.next_free += 1
        return index


def labelled_by_name(node):
    """Borrow a name from the AT-SPI LABELLED_BY relation for an unnamed node.

    Toolkits often put a control's label in a relation instead of on the
    control. In the LibreOffice 7.3 Position and Size dialog, all 13 unnamed
    spin buttons get names this way (`Position Y:`, `Position X:`, `Width:`,
    `Height:`, `Angle:`, `Radius:`), so they can be selected by name instead
    of by position. The standard accessible-name computation includes
    labelled-by (Playwright's does too), so this does not invent a name. The
    rendered line still carries a `labelled` mark because a borrowed name may
    not be unique: the same dialog has two `Position Y:` labels, one per tab.

    get_relation_set costs one D-Bus round trip per node (about 1 ms), so it
    is queried only for nodes without their own name, where all of the
    benefit lies; querying all 3261 nodes of a GIMP window would add 3.4 s.
    For the same reason the pruning predicate, which runs on every node, does
    not consult relations. A node worth keeping only for its borrowed name
    can therefore be pruned. This has not been observed (such spin buttons
    are kept for their actions), but it is a known limitation.
    """
    relations = safe(lambda: node.get_relation_set(), None)
    if not relations:
        return ""
    names = []
    for relation in relations:
        kind = safe(lambda: relation.get_relation_type())
        if kind != Atspi.RelationType.LABELLED_BY:
            continue
        count = safe(lambda: relation.get_n_targets(), 0) or 0
        for index in range(count):
            target = safe(lambda: relation.get_target(index))
            if target is None:
                continue
            text = str(node_name(target) or "").strip()
            if text:
                names.append(text)
    # Labels often end with a colon (`Position Y:`). Strip it: it carries no
    # information and is awkward to write in selectors.
    return " ".join(names).rstrip(":").strip()


def effective_name(node):
    """Return the node's own name, or one borrowed from LABELLED_BY.

    Every name comparison must use this single definition. If a record holds
    a borrowed name such as `Position Y` while matching reads node_name(),
    which returns an empty string for the same node, the match fails silently
    and identity falls back to the weakest criterion, role plus screen
    position, so the reference follows whatever element moves into place.
    """
    own = node_name(node)
    if str(own or "").strip():
        return own
    return labelled_by_name(node)

def semantic_relations(node, text_limit=DEFAULT_TEXT_LIMIT):
    """Return bounded relation facts for retained nodes only.

    Relations are evidence, not a replacement address. Targets stay descriptive
    because AT-SPI relation objects do not expose the provider traversal path
    used by StableIndexer. Go therefore uses them only for conservative
    duplicate-label pruning, never for action resolution.
    """
    wanted = {}
    for enum_name, output_name in (
        ("LABELLED_BY", "labelled-by"),
        ("LABEL_FOR", "label-for"),
        ("CONTROLLER_FOR", "controller-for"),
        ("MEMBER_OF", "member-of"),
    ):
        value = getattr(Atspi.RelationType, enum_name, None)
        if value is not None:
            wanted[value] = output_name
    try:
        relations = node.get_relation_set()
    except Exception:
        return {}, False
    if relations is None:
        return {}, False
    if not relations:
        return {}, True
    out = {}
    complete = True
    for relation in relations:
        kind = safe(lambda: relation.get_relation_type())
        label = wanted.get(kind)
        if not label:
            continue
        count = min(int(safe(lambda: relation.get_n_targets(), 0) or 0), 16)
        targets = []
        for index in range(count):
            try:
                target = relation.get_target(index)
            except Exception:
                complete = False
                continue
            if target is None:
                complete = False
                continue
            targets.append({
                "role": str(safe(lambda: node_role(target), "") or ""),
                "name": limit_text(
                    str(
                        safe(lambda: node_name(target), "")
                        or safe(
                            lambda: element_value(
                                target,
                                text_limit=min(text_limit, 160),
                            ),
                            "",
                        )
                        or ""
                    ),
                    text_limit=min(text_limit, 160),
                ),
                "providerBusName": provider_bus_name(target),
                "providerObjectPath": provider_object_path(target),
                "processGeneration": process_generation(node_pid(target)),
            })
        if targets:
            out[label] = targets
    return out, complete


def record_for(node, index, path, window_bounds, text_limit=DEFAULT_TEXT_LIMIT):
    bounds = relative_frame(node, window_bounds)
    role = node_role(node)
    # Read the action table once for both fields; see node_actions() about the
    # LibreOffice ATK bridge.
    actions, has_click_action = node_actions(node)
    own_name = node_name(node)
    relations, relations_complete = semantic_relations(
        node, text_limit=text_limit
    )
    labelled_names = [
        str(target.get("name") or "").strip()
        for target in relations.get("labelled-by", [])
        if str(target.get("name") or "").strip()
    ]
    if str(own_name or "").strip():
        full_name = own_name
        name_source = "provider"
        borrowed = False
    else:
        full_name = " ".join(labelled_names).rstrip(":").strip()
        name_source = "labelled_by" if full_name else ""
        borrowed = bool(full_name)
    name = limit_text(full_name, text_limit=text_limit)
    record = {
        "index": index,
        "runtimeId": path[:],
        "automationId": accessible_id(node),
        "providerBusName": provider_bus_name(node),
        "providerObjectPath": provider_object_path(node),
        "processGeneration": process_generation(node_pid(node)),
        "showing": state_value(node, Atspi.StateType.SHOWING),
        "visible": state_value(node, Atspi.StateType.VISIBLE),
        "name": name,
        "controlType": role,
        "localizedControlType": role,
        "className": str(safe(node.get_toolkit_name, "") or ""),
        "value": element_value(node, text_limit=text_limit),
        "nativeWindowHandle": 0,
        "frame": bounds,
        "actions": actions,
        "states": state_segment(
            node, has_click_action=has_click_action, borrowed_name=borrowed
        ),
        "description": node_description(node, text_limit=text_limit),
        "placeholder": placeholder_text(
            node, text_limit=text_limit, role=role
        ),
        # Text attributes travel with the record instead of a separate walk:
        # rendering already visits every node and Go keeps the pre-action
        # snapshot, so comparing the two costs no extra AT-SPI calls. Reading
        # them from the focused text node does not work because the
        # LibreOffice root pane also reports FOCUSED, and a separate full-tree
        # scan took 551 ms and could miss the target paragraph.
        "textAttributes": text_attributes(node),
        "relations": relations,
        "relationReadComplete": relations_complete,
        "nameSource": name_source,
    }
    # When the name is truncated, also store a hash of the full name.
    #
    # Identity checks compare the live full name, which never equals the
    # truncated recorded name, so without the hash a lower text_limit would
    # silently weaken identity to role plus screen position. Comparing only
    # the truncated prefix would be worse: two elements sharing a prefix
    # would match. The hash restores exact full-name equality at the cost of
    # 16 characters on truncated records.
    if str(full_name or "") != name:
        record["nameHash"] = name_fingerprint(full_name)
    return record
