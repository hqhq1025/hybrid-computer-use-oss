"""Linux provider module: AX node properties and tree-line rendering."""

from .foundation import (
    Atspi,
    BUDGET_PRESSURE_RATIO,
    CLICK_COVERED_ACTIONS,
    DEFAULT_TEXT_LIMIT,
    MAX_CHILD_FANOUT,
    MAX_DEPTH,
    MAX_ELEMENTS,
    MAX_RENDERED_CHARS,
    MENU_ROLES,
    NON_SELF_ACTIONS,
    PIXEL_DIFF_STRIDE,
    PIXEL_DIFF_THRESHOLD,
    SCREENSHOT_MAX_BASE64_CHARS,
    TREE_DEADLINE_SECONDS,
    UNIVERSAL_ACTIONS,
    child_count,
    has_text_iface,
    is_interactive_role,
    limit_text,
    node_name,
    node_pid,
    node_role,
    normalized_action,
    os,
    process_generation,
    provider_bus_name,
    provider_object_path,
    re,
    safe,
    should_enumerate_children,
    state_contains,
    state_value,
    strict_prune_probe,
    time,
)

from .x11_windows import (
    relative_frame,
)
from .primitives import name_fingerprint, quoted
def node_actions(node):
    """Read the node's action table once and derive both rendering facts.

    Return (names of actions that click does not cover, whether an
    invocable click entry exists).

    The table must be read in a single pass. Queried repeatedly, the
    LibreOffice ATK bridge logs

        (soffice): CRITICAL: impl_get_NActions:
                   assertion 'ATK_IS_ACTION (user_data)' failed

    and under dense querying the application exits entirely (soffice
    disappeared after three or four dialog round trips). Rendering already
    reads the table once per node; reading it again for the
    `[has-click-action]` mark through preferred_action_index() would double
    the load on a bridge known to be fragile. Action names and descriptions
    are therefore read in the same pass.
    """
    # Check for the Action interface before asking for the action count.
    # get_n_actions() on an object without it triggers the ATK assertion
    # above, and most nodes (panels, fillers, labels, other containers) lack
    # the interface, so a single dialog round trip logged 3482 assertions.
    if safe(node.get_action_iface) is None:
        return [], False
    count = int(safe(node.get_n_actions, 0) or 0)
    names = []
    has_click_entry = False
    for index in range(count):
        name = str(safe(lambda i=index: node.get_action_name(i), "") or "")
        description = str(
            safe(lambda i=index: node.get_action_description(i), "") or ""
        )
        label = name or description
        lower = label.strip().lower()
        # Same criteria as preferred_action_index(): an exact match or the
        # activate/click/press substring fallback. The two must agree, or the
        # mark would misreport what click can invoke.
        if lower in CLICK_COVERED_ACTIONS or (
            normalized_action(lower) not in NON_SELF_ACTIONS
            and ("activate" in lower or "click" in lower or "press" in lower)
        ):
            has_click_entry = True
        if not label or label in names:
            continue
        if lower in CLICK_COVERED_ACTIONS:
            continue
        # Skip actions that every node has, so is_structural_filler still
        # recognizes filler nodes.
        if normalized_action(lower) in UNIVERSAL_ACTIONS:
            continue
        names.append(label)
    return names, has_click_entry


def action_names(node):
    """List the node's semantic actions that the click tool does not cover.

    Actions that preferred_action_index() would pick (click, press,
    activate, ...) must be filtered out. Otherwise every clickable node
    shows a secondary `click` action, which is exactly what the click tool
    invokes; the model then reads it as an alternative path and alternates
    between semantic clicks and coordinate fallbacks.
    """
    return node_actions(node)[0]


def accessible_id(node):
    getter = getattr(node, "get_accessible_id", None)
    return str(safe(getter, "") or "") if callable(getter) else ""


def text_value_with_status(node, text_limit=DEFAULT_TEXT_LIMIT):
    """Return text plus whether the provider read completed reliably."""
    try:
        text_iface = node.get_text_iface()
    except Exception:
        return "", False
    if text_iface is None:
        return "", True
    try:
        count = int(Atspi.Text.get_character_count(text_iface) or 0)
        if count <= 0:
            return "", True
        end_offset = count if text_limit is None else min(count, text_limit + 1)
        value = str(
            Atspi.Text.get_text(text_iface, 0, end_offset) or ""
        )
    except Exception:
        return "", False
    return limit_text(value, text_limit=text_limit), True


def text_value(node, text_limit=DEFAULT_TEXT_LIMIT):
    value, _complete = text_value_with_status(
        node, text_limit=text_limit
    )
    return value


def numeric_value(node):
    value_iface = safe(node.get_value_iface)
    if value_iface is None:
        return ""
    current = safe(lambda: Atspi.Value.get_current_value(value_iface))
    if current is None:
        return ""
    return str(current)


# Maximum nodes scanned below a dropdown to find the selected option. Option
# lists usually hold a few to a few dozen entries, so 200 is enough, and the
# cap keeps the cost of this path bounded.
SELECTION_SCAN_BUDGET = 200


def selected_option_name(node):
    """Return the name of the dropdown's selected option, or "".

    Without it, dropdown values are invisible in the tree: Chrome's print
    dialog shows the combo boxes "Margins", "Destination" and "Scale" with
    names but no values, so the agent can neither decide whether a change is
    needed nor confirm that one worked.

    The value is not on the Value interface (Chrome combo boxes do not
    implement it) or the Text interface (it reads back as U+FFFC). It is the
    SELECTED state of a descendant menu item, which is readable even while
    the dropdown is closed; opening every dropdown first would be far too
    costly.

    Some dropdowns expose no selection (Chrome's "Layout" combo box, for
    example). In that case return an empty string rather than guess: a
    missing value is better than an invented one.
    """
    if node is None:
        return ""
    stack = [node]
    seen = 0
    while stack and seen < SELECTION_SCAN_BUDGET:
        current = stack.pop()
        seen += 1
        if current is not node and state_contains(current, Atspi.StateType.SELECTED):
            name = str(node_name(current) or "").strip()
            if name:
                return name
        for index in range(min(child_count(current), 100)):
            child = safe(lambda: current.get_child_at_index(index))
            if child is not None:
                stack.append(child)
    return ""


def element_value(node, text_limit=DEFAULT_TEXT_LIMIT):
    direct = text_value(node, text_limit=text_limit) or numeric_value(node)
    if direct:
        return direct
    # Only dropdown roles pay for the descendant scan.
    role = str(node_role(node) or "")
    if role in ("combo box", "list box"):
        return limit_text(selected_option_name(node), text_limit=text_limit)
    return direct


def positive_int(value, fallback):
    if isinstance(value, bool):
        return fallback
    if isinstance(value, float) and not value.is_integer():
        return fallback
    try:
        integer = int(value)
    except (TypeError, ValueError):
        return fallback
    return integer if integer > 0 else fallback


def parse_text_limit(value, fallback=DEFAULT_TEXT_LIMIT):
    if isinstance(value, str) and value.lower() == "max":
        return None
    return positive_int(value, fallback)


# Decision-relevant states, rendered only on their non-default side. Marking
# every node enabled or not-focused would drown the signal; checked,
# expanded, selected and focused are what the agent decides on.
NOTABLE_STATES = (
    ("CHECKED", "checked"),
    ("EXPANDED", "expanded"),
    ("SELECTED", "selected"),
    ("FOCUSED", "focused"),
)


# Action name -> the state it promises to change.
#
# The approach is adapted from Playwright's `_setChecked`
# (packages/playwright-core/src/server/dom.ts, Apache-2.0): Playwright reads
# the checkbox state back after clicking and throws a non-recoverable error
# ("Clicking the checkbox did not change its state") without retrying,
# because repeating the same action will not give a different result.
#
# The names come from action tables observed in gedit, Nautilus, VLC,
# VS Code, GIMP and Thunderbird. `expand or contract` and `toggle` flip a
# state rather than set a value, so the check is that the state flipped, not
# that it equals a target value.
ACTION_MUST_FLIP = {
    "expand or contract": "EXPANDED",
    "expandorcontract": "EXPANDED",
    "toggle": "CHECKED",
}
# Directional actions: the value must change.
ACTION_MUST_CHANGE_VALUE = ("increase", "decrease")


def readable_states(node):
    """Read back the decidable states, or return None if they are unreadable.

    None is returned instead of an empty dict that would pose as a reading.
    """
    if node is None:
        return None
    out = {}
    for name, _label in NOTABLE_STATES:
        state = getattr(Atspi.StateType, name, None)
        if state is None:
            continue
        out[name] = state_contains(node, state)
    return out or None


def state_transition_note(action, before, after):
    """Verify, after an action that promises a state change, that it happened.

    Return (note, failed). failed=True marks a failure that must not be
    retried: repeating the same action will not produce a different result.

    A flipped state proves that the control changed, not that the
    application acted on it. In VLC's preferences a radio button's CHECKED
    state flipped after `Toggle` while the panel did not switch. This check
    catches only one case, an action that promised a flip and did not flip;
    treating it as general verification would produce false positives.
    """
    name = ACTION_MUST_FLIP.get(str(action).strip().lower())
    if name is None or not before or not after:
        return None, False
    was, now = before.get(name), after.get(name)
    if was is None or now is None:
        return None, False
    if was != now:
        return ("[state] {} flipped {} -> {}, which is what the '{}' action promises. "
                "Note the limit: this proves the CONTROL changed, not that the "
                "application acted on it — measured on VLC, a radio button's CHECKED "
                "flipped while the panel it selects did not switch. Confirm the actual "
                "effect before treating this as done."
                .format(name, was, now, action), False)
    return ("The '{}' action reported success, but {} is still {} — that action promises "
            "to flip it, so nothing happened. Repeating the same call will not help; "
            "the toolkit accepted it without acting. Reach the element with a coordinate "
            "click instead (gui, or click with click_method \"global\")."
            .format(action, name, now), True)


def plain_text_from_rich_text(value):
    """Convert a Qt rich-text tooltip to plain text.

    Qt stores tooltips as complete HTML documents with CSS in `<head>`. In
    VLC's preferences, 19 such tooltips totalled 9149 characters, 56% of one
    observation, while the actual information was often one sentence.
    Unconverted, the description segments would consume the whole
    observation budget.

    Only text starting with `<html`, the Qt rich-text marker, is converted.
    Ordinary text may legitimately contain angle brackets (code, templates,
    math), and stripping it would alter the data the agent reads.
    """
    text = str(value or "")
    if not text.lstrip().lower().startswith("<html"):
        return text
    # `<head>` holds only CSS, which would leak as
    # `p, li { white-space: pre-wrap; }` after tag stripping, so the whole
    # block is dropped instead of stripping tags one by one.
    text = re.sub(r"(?is)<head>.*?</head>", " ", text)
    text = re.sub(r"(?is)<style.*?</style>", " ", text)
    text = re.sub(r"(?i)<br\s*/?>", " ", text)
    text = re.sub(r"(?s)<[^>]+>", "", text)
    for entity, char in (("&amp;", "&"), ("&lt;", "<"), ("&gt;", ">"),
                         ("&quot;", '"'), ("&nbsp;", " ")):
        text = text.replace(entity, char)
    return " ".join(text.split())


def node_description(node, text_limit=DEFAULT_TEXT_LIMIT):
    """Return the control's description (AT-SPI `get_description`).

    GTK usually fills it with the tooltip, and it is often the only readable
    label. In Nautilus the toolbar buttons `Go back`, `Go forward`, `Search`
    and `Show list` have empty names, and three `toggle button Menu`
    controls with identical names differ only by description
    (`Show operations`, `View options`). Without it, core file-manager
    actions such as back and forward render as a bare `push button`, and the
    agent can only guess pixel coordinates.

    GTK's convention is the opposite of macOS, which puts the readable label
    in AXTitle, so Linux has to read this extra field.

    The description is rendered separately instead of replacing the name,
    because the name is part of element identity (matching uses role +
    name) and rewriting it would break matching across snapshots.
    """
    getter = getattr(node, "get_description", None)
    raw = str(safe(getter, "") or "") if callable(getter) else ""
    return limit_text(plain_text_from_rich_text(raw), text_limit=text_limit)


PLACEHOLDER_ROLES = {
    "combo box",
    "entry",
    "password text",
    "search box",
    "text",
    "text area",
    "text box",
    "text field",
}


def role_supports_placeholder(role):
    normalized = str(role or "").strip().lower().replace("_", " ")
    return (
        normalized in PLACEHOLDER_ROLES
        or normalized.replace(" ", "") in {
            "searchbox",
            "textarea",
            "textbox",
            "textfield",
        }
    )


def placeholder_text(node, text_limit=DEFAULT_TEXT_LIMIT, role=None):
    """Return the control's placeholder (AT-SPI `placeholder-text` attribute).

    Equivalent to macOS `placeholderValue`. It must be rendered apart from
    real content: a placeholder looks like content although the control is
    empty, so mixing the two makes the agent skip input it still needs or
    read the hint as data.

    `get_attributes()` is an optional and expensive AT-SPI call and must not
    be probed on every panel, shape or table cell; Impress exited when its
    whole tree was scanned this way after a document structure change.
    Placeholders exist only on text-input roles, so the role is checked first
    and the attributes are read once for the nodes that can carry one.
    """
    effective_role = node_role(node) if role is None else role
    if not role_supports_placeholder(effective_role):
        return ""
    getter = getattr(node, "get_attributes", None)
    attributes = safe(getter) if callable(getter) else {}
    for key in ("placeholder-text", "placeholder"):
        value = attributes.get(key)
        if value:
            return limit_text(str(value), text_limit=text_limit)
    return ""


TEXT_ATTRIBUTES_OF_INTEREST = (
    "justification", "size", "weight", "style", "underline", "strikethrough",
    "family-name", "fg-color", "bg-color", "line-height", "indent",
    "left-margin", "right-margin", "text-decoration",
)


def text_attributes(node):
    """Read selected text-format properties without claiming an empty result."""
    iface = safe(node.get_text_iface)
    if iface is None:
        return None
    raw = safe(lambda: Atspi.Text.get_default_attributes(iface))
    if not raw:
        return None
    try:
        table = dict(raw)
    except Exception:
        return None
    return {key: table[key] for key in TEXT_ATTRIBUTES_OF_INTEREST if key in table}


def render_element_line(record, render_depth, boxes=True):
    """Render one element record as a tree line.

    The grammar is modeled on Playwright's aria snapshot:

        <indent><index> <role> "<name>" [<states>] [desc="..."]
            [placeholder="..."] [actions=a,b] {x,y,w,h}: "<value>"

    - All free text is quoted, so a name cannot be confused with a field
      separator.
    - Extra attributes go in square brackets, like the aria snapshot's
      `[checked]` and `[level=1]`.
    - Geometry is compacted to `{x,y,w,h}`: 14 characters where
      `Frame: {x: 687, y: 23, width: 64, height: 46}` takes 45, with no loss
      of information. Geometry is 35-50% of a rendered tree, so this saves
      about two thirds of it.
    - `boxes=False` omits geometry entirely, like Playwright's `boxes`
      option. Actions take coordinates from the stored record, not from this
      line.
    - The value comes last, after a colon, as in aria's
      `- textbox: Enter your name`.
    - Empty segments are omitted.
    - The index leads the line; the Go side keys rendered lines by it.
    """
    role = record["localizedControlType"] or record["controlType"] or "element"
    title = record["name"] or record["automationId"] or ""
    parts = ["{} {}".format(record["index"], role)]
    if title:
        parts.append(quoted(title))
    state_seg = record.get("states", "")
    if state_seg:
        parts.append(state_seg.strip())
    if record.get("description") and record["description"] != title:
        # A separate segment, not merged into the name, which is part of
        # element identity. GTK often puts the only readable label here,
        # especially on icon-only buttons.
        parts.append("[desc=" + quoted(record["description"]) + "]")
    if record.get("placeholder") and record["placeholder"] != title:
        # A placeholder is not content: the control is actually empty, so it
        # must never be merged into the value.
        parts.append("[placeholder=" + quoted(record["placeholder"]) + "]")
    if record["actions"]:
        parts.append("[actions=" + ",".join(record["actions"]) + "]")
    if boxes and record["frame"] is not None:
        f = record["frame"]
        parts.append("{{{0},{1},{2},{3}}}".format(
            round(f["x"]), round(f["y"]), round(f["width"]), round(f["height"])))
    line = ("\t" * (render_depth + 1)) + " ".join(parts)
    if record["value"] and record["value"] != title:
        line += ": " + quoted(record["value"])
    return line


def state_segment(node, has_click_action=False, borrowed_name=False):
    """Render notable states as compact marks such as `[checked expanded]`.

    Report only states whose presence is meaningful, and never infer meaning
    from an absent state (绝不从状态的缺失反推语义). In particular, a
    missing ENABLED state is not rendered as `[disabled]`: Nautilus file
    icons set neither ENABLED nor SENSITIVE (only SHOWING, VISIBLE and
    FOCUSABLE) yet expose working `open` and `menu` actions. Toolkits set
    ENABLED/SENSITIVE inconsistently on Linux, and nothing reliably
    distinguishes a disabled control from a toolkit that does not report the
    state. A missing signal is better than a false one that makes the agent
    skip a usable target.
    """
    marks = []
    for name, label in NOTABLE_STATES:
        state = getattr(Atspi.StateType, name, None)
        if state is not None and state_contains(node, state):
            marks.append(label)
    # `has-click-action` is a capability, not an AT-SPI state, but it belongs
    # in the same group: the agent should see at a glance whether the element
    # can be clicked semantically.
    #
    # The mark is needed because action_names() hides click-like actions
    # (otherwise every clickable node would list `click` as another action,
    # and the model would alternate between two tools). With them hidden, a
    # node with a semantic click and a node without an Action interface
    # would render identically, and the agent would fall back to coordinates
    # for both. Clickable nodes are the minority (10 of 113 in Nautilus, 7 of
    # 37 in gedit), so marking them is an order of magnitude cheaper than
    # marking the rest.
    #
    # The mark must use the same criteria as the entry point that the click
    # tool invokes (preferred_action_index), or it would mislead.
    if has_click_action:
        # The wording promises an invocable action, not an effect: in
        # Nautilus, GIMP and VLC such actions can report success and do
        # nothing. A name like `clickable` would read as a guarantee.
        marks.append("has-click-action")
    # Mark names borrowed from LABELLED_BY because they may not be unique:
    # LibreOffice's Position and Size dialog has two `Position Y` labels, one
    # per tab. When a selector then matches several elements, this mark
    # explains why.
    if borrowed_name:
        marks.append("labelled")
    if not marks:
        return ""
    return " [" + " ".join(marks) + "]"
