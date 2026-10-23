"""The AX select action on drop-downs: choose an option and read it back.

A drop-down is a combo box or list box whose options are its descendants.
Choosing an option succeeds only when the drop-down reads back the choice:
the option's SELECTED state, or for a combo box that names itself after its
choice (GTK, Qt), that name.
"""

from collections import deque

from . import action_evidence
from .foundation import (
    Atspi,
    FIND_FIRST_BUDGET,
    MAX_CHILD_FANOUT,
    OBJECT_REPLACEMENT,
    OperationFailure,
    node_name,
    node_role,
    provider_object_path,
    raw_extents,
    safe,
    should_enumerate_children,
    state_contains,
)
from .ax_records import gtk_combo_partner
from .ax_actions import send_key
from .ax_select import (
    _failure,
    _quote,
    _same,
    _selection_container,
    _wait_until,
    text_between,
)
from .input_primitives import InputEmissionError, require_window_focus


DROPDOWN_ROLES = ("combo box", "list box")
# Combo box actions that open the popup list: Qt "ShowMenu", GTK "press",
# Chromium "open".
POPUP_OPEN_ACTIONS = ("showmenu", "press", "open")
OPTION_ROLES = ("menu item", "list item", "option", "check menu item",
                "radio menu item", "table cell")
# Options read below one drop-down; HTML <select> lists rarely exceed this.
MAX_DROPDOWN_OPTIONS = 500


def _dropdown_options(node):
    """Return all options in tree order, or reject an incomplete scan.

    A prefix cannot prove that an option name is unique or absent. Apply
    the same bound to queued and visited nodes so wide trees stay bounded.
    """
    options = []
    stack = deque([node])
    seen = 0
    while stack:
        current = stack.popleft()
        seen += 1
        role = safe(current.get_role_name)
        if role is None:
            raise _incomplete_options()
        if current is not node and role in OPTION_ROLES:
            if safe(current.get_name) is None:
                raise _incomplete_options()
            options.append(current)
            continue
        count = safe(current.get_child_count)
        if (count is None or count < 0
                or seen + len(stack) + count > MAX_DROPDOWN_OPTIONS):
            raise _incomplete_options()
        for index in range(count):
            child = safe(lambda index=index: current.get_child_at_index(index))
            if child is None:
                raise _incomplete_options()
            stack.append(child)
    return options


def _incomplete_options():
    return _failure(
        "select_options_incomplete: the drop-down options could not be read "
        "completely; a unique choice cannot be established. Inspect the "
        "open list and select the option with GUI.",
        emitted=_dropdown_input_emitted())


def _dropdown_input_emitted():
    return bool((action_evidence.current() or {}).get("input_emitted"))


def _emit_dropdown_action(callback):
    # A lost reply does not prove emission or rejection. Keep that uncertainty
    # separate from requests that the provider explicitly accepted.
    try:
        accepted = callback()
    except OperationFailure as exc:
        if exc.input_events_emitted:
            action_evidence.update(input_emitted=True, route="semantic_action")
        elif not exc.failure_facts_complete:
            exc.partial_input_possible = True
        raise
    except Exception as exc:
        exc.partial_input_possible = True
        raise
    if accepted:
        action_evidence.update(input_emitted=True, route="semantic_action")
    return accepted


def _send_dropdown_key(key):
    try:
        send_key(key)
    except InputEmissionError:
        raise
    except OperationFailure as exc:
        if not exc.failure_facts_complete:
            exc.partial_input_possible = True
        raise
    except Exception as exc:
        exc.partial_input_possible = True
        raise
    action_evidence.update(input_emitted=True, route="keyboard_synthesis")


def _option_selected(option):
    if state_contains(option, Atspi.StateType.SELECTED):
        return True
    container = _selection_container(option)
    index = safe(lambda: option.get_index_in_parent())
    if container is None or index is None or index < 0:
        return False
    return bool(safe(lambda: Atspi.Selection.is_child_selected(
        container, int(index)), False))


def option_name(option):
    name = safe(option.get_name)
    if name is None:
        raise _incomplete_options()
    return str(name).strip()


def _shown_choice(node):
    """Return the text a drop-down shows for its current choice, or "".

    GTK and Qt combo boxes name themselves after the current choice. A
    browser <select> is named by its label and its own text is U+FFFC, so
    there this returns the label; callers compare it with the option names.
    """
    name = option_name(node)
    if name:
        return name
    return text_between(node, 0, -1).replace(OBJECT_REPLACEMENT, "").strip()


def _popup_open(combo, option):
    """Return True while the popup list holding ``option`` is on screen."""
    if state_contains(combo, Atspi.StateType.EXPANDABLE):
        return state_contains(combo, Atspi.StateType.EXPANDED)
    # Qt reports no EXPANDED state and keeps the closed list SHOWING with a
    # 0x0 frame; GTK and Chromium do not report the closed list SHOWING.
    container = safe(option.get_parent)
    frame = raw_extents(container) if container is not None else None
    return (frame is not None and frame["width"] > 1 and frame["height"] > 1
            and state_contains(container, Atspi.StateType.SHOWING))


def _focused_index(options):
    focused = [index for index, option in enumerate(options)
               if state_contains(option, Atspi.StateType.FOCUSED)]
    return focused[0] if len(focused) == 1 else None


def _open_popup(combo, option):
    if _popup_open(combo, option):
        return True
    count = int(safe(combo.get_n_actions, 0) or 0)
    names = [str(safe(lambda k=k: combo.get_action_name(k), "") or "").lower()
             for k in range(count)]
    for wanted in POPUP_OPEN_ACTIONS:
        if wanted in names:
            _emit_dropdown_action(lambda: combo.do_action(names.index(wanted)))
            return _wait_until(lambda: _popup_open(combo, option), 1.0)
    return False


def _commit_from_popup(combo, option, window):
    """Make ``option`` the popup's current item and commit it with Return.

    A Qt popup commits its current item, and only the keyboard moves that
    item: the items' Toggle action changes the selection, not the current
    item, and Component.grab_focus is refused. Every arrow key is checked
    against the FOCUSED state, so separators and disabled items cannot
    shift the count. If the option cannot be reached, Escape closes the
    popup without changing the choice. Returns True after Return was sent.
    """
    require_window_focus(window, "select")
    if not _open_popup(combo, option):
        return False
    # Qt recreates popup items on every read, so the option is found again
    # by its unique name (select_dropdown_option checked uniqueness).
    options = _dropdown_options(combo)
    names = [option_name(candidate) for candidate in options]
    target = (names.index(option_name(option))
              if names.count(option_name(option)) == 1 else None)
    for _ in range(len(options)):
        current = _focused_index(options)
        if target is None or current is None or current == target:
            break
        _send_dropdown_key("Down" if target > current else "Up")
        if not _wait_until(lambda c=current: _focused_index(options) != c, 0.6):
            break
    if target is None or _focused_index(options) != target:
        _send_dropdown_key("Escape")
        return False
    _send_dropdown_key("Return")
    return True


def select_dropdown_option(node, text, window=None):
    """Choose the option named ``text`` of a closed or open drop-down.

    Browsers expose an HTML <select> as a combo box whose options are menu
    items with a single "select" action; while the list is closed they are
    not showing and carry no refs. The option's own action is tried first
    (Chrome's Selection.select_child returns False), then the combo box's
    own Selection.select_child (GTK sets the active item through it), then
    the option container's Selection.select_child (Firefox accepts it).

    A combo box that shows its choice (GTK and Qt name themselves after it)
    succeeds only when it shows the option afterwards; otherwise the
    option's SELECTED state is the evidence. When neither route changes the
    shown choice, the popup's own commit path is used: open it, move its
    current item to the option and press Return (Qt). A LibreOffice
    drop-down (a projected toggle) commits through its separate popup list;
    see select_projected_option(). Returns the chosen option's name.
    """
    if _projected_toggle(node):
        return select_projected_option(node, text)
    options = _dropdown_options(node)
    wanted = str(text).strip()
    matches = [option for option in options if option_name(option) == wanted]
    if len(matches) != 1:
        names = [option_name(option) for option in options]
        raise _failure(
            "select_option_not_found: {} {} among the options; no input was "
            "emitted. Options: {}.".format(
                _quote(text),
                "is not" if not matches else "occurs more than once",
                ", ".join(_quote(name) for name in names[:30]) or "none readable"),
            code="invalid_argument")
    option = matches[0]
    shows_choice = (node_role(node) == "combo box" and _shown_choice(node)
                    in {option_name(candidate) for candidate in options})

    def committed():
        if shows_choice:
            return _shown_choice(node) == wanted
        return _option_selected(option)

    if committed():
        return wanted
    count = int(safe(option.get_n_actions, 0) or 0)
    names = [str(safe(lambda k=k: option.get_action_name(k), "") or "")
             for k in range(count)]
    if "select" in names:
        _emit_dropdown_action(lambda: option.do_action(names.index("select")))
        if _wait_until(committed, 0.6):
            return wanted
    index = safe(lambda: option.get_index_in_parent())
    parent = safe(option.get_parent)
    if (index is not None and index >= 0 and parent is not None
            and safe(node.get_selection_iface) is not None
            and _same(safe(parent.get_parent), node)):
        # GTK commits through the combo box's own Selection, which sets the
        # active item; the popup menu's Selection only highlights an item.
        _emit_dropdown_action(lambda: Atspi.Selection.select_child(node, int(index)))
        if _wait_until(committed, 0.6):
            return wanted
    container = _selection_container(option)
    if container is not None and index is not None and index >= 0:
        _emit_dropdown_action(lambda: Atspi.Selection.select_child(container, int(index)))
        if _wait_until(committed, 0.6):
            return wanted
    if shows_choice:
        if _commit_from_popup(node, option, window) and _wait_until(committed, 1.0):
            return wanted
        raise _failure(
            "select_unverified: the drop-down shows {} after choosing {}; "
            "its choice may have changed. Open it, then press Return on the "
            "option or click it with the GUI.".format(
                _quote(_shown_choice(node)), _quote(text)),
            code="effect_unknown", emitted=_dropdown_input_emitted(), phase="readback")
    if not _dropdown_input_emitted():
        raise _failure(
            "select_unavailable: the option {} has no select action and its "
            "list has no selection interface; no input was emitted. Open the "
            "drop-down and click the option.".format(_quote(text)))
    raise _failure(
        "select_unverified: the option {} did not report itself selected."
        .format(_quote(text)),
        code="effect_unknown", emitted=_dropdown_input_emitted(), phase="readback")


def owning_dropdown(node):
    """Return the drop-down whose popup list holds the option ``node``."""
    if node_role(node) not in OPTION_ROLES:
        return None
    current = node
    for _ in range(3):
        current = safe(current.get_parent)
        if current is None:
            break
        if node_role(current) == "combo box":
            return current
    return projected_list_owner(node)


# LibreOffice (gtk3) draws a drop-down as a toggle button beside a hidden
# combo box and a text; ax_records.gtk_combo_partner() projects the toggle
# as the combo box. The list opens in a separate untitled popup window of
# table cells that has no relation back to the toggle. While the list is
# open the toggle reports CHECKED, and the text shows the current choice.


def _projected_toggle(node):
    return gtk_combo_partner(node, node_role(node)) is not None


def _projected_shown_choice(toggle):
    """Return the text the projected combo's text sibling shows, or ""."""
    parent = safe(toggle.get_parent)
    count = int(safe(parent.get_child_count, 0) or 0) if parent else 0
    texts = [child for child in (
        safe(lambda k=k: parent.get_child_at_index(k)) for k in range(count))
        if child is not None and node_role(child) == "text"]
    return text_between(texts[0], 0, -1).strip() if len(texts) == 1 else ""


def _top_level(node):
    """Return ``(application, top-level window)`` holding ``node``.

    LibreOffice reports no parent for its top-level windows, so the
    application comes from get_application when the walk stops short.
    """
    window = node
    for _ in range(64):
        parent = safe(window.get_parent)
        if parent is None or node_role(parent) == "application":
            break
        window = parent
    else:
        return None, None
    app = parent if parent is not None else safe(
        lambda: Atspi.Accessible.get_application(node))
    return (app, window) if app is not None else (None, None)


def _open_lists(app):
    """Return the untitled showing top-level windows of ``app`` with options."""
    count = int(safe(app.get_child_count, 0) or 0) if app else 0
    windows = (safe(lambda k=k: app.get_child_at_index(k)) for k in range(count))
    return [window for window in windows
            if window is not None and not option_name(window)
            and state_contains(window, Atspi.StateType.SHOWING)
            and _dropdown_options(window)]


def _list_choice(toggle):
    """Read a list-only drop-down's choice: the row its list selects on opening.

    A list-only LibreOffice drop-down (dialog combos such as Line Spacing)
    leaves its text sibling empty, so the choice is readable only while the
    list is open. The list is closed again before returning.
    """
    popup = _open_projected_list(toggle)
    if popup is None:
        return ""
    chosen = [option_name(option) for option in _dropdown_options(popup)
              if state_contains(option, Atspi.StateType.SELECTED)]
    _click_toggle(toggle)
    _wait_until(lambda: not state_contains(toggle, Atspi.StateType.CHECKED), 1.0)
    return chosen[0] if len(chosen) == 1 else ""


def _click_toggle(toggle):
    count = int(safe(toggle.get_n_actions, 0) or 0)
    names = [str(safe(lambda k=k: toggle.get_action_name(k), "") or "").lower()
             for k in range(count)]
    if "click" not in names:
        return False
    return bool(_emit_dropdown_action(lambda: toggle.do_action(names.index("click"))))


def _open_projected_list(toggle):
    """Return the popup window holding the toggle's list, opening it if needed."""
    app, _window = _top_level(toggle)
    if state_contains(toggle, Atspi.StateType.CHECKED):
        lists = _open_lists(app)
        return lists[0] if len(lists) == 1 else None
    before = {provider_object_path(window) for window in _open_lists(app)}
    if not _click_toggle(toggle):
        return None
    opened = []

    def appeared():
        opened[:] = [window for window in _open_lists(app)
                     if provider_object_path(window) not in before]
        return len(opened) == 1
    return opened[0] if _wait_until(appeared, 1.0) else None


def _activate_list_row(option):
    """Move the list cursor to ``option`` and activate that row.

    A GTK tree view commits the row under its cursor when a cell is
    activated. Selection.select_child only highlights a row and leaves the
    cursor, so the old choice would be committed; grab_focus on the cell
    moves the cursor.
    """
    component = safe(option.get_component_iface)
    if component is None or not _emit_dropdown_action(
            lambda: Atspi.Component.grab_focus(component)):
        return False
    if not _wait_until(
            lambda: state_contains(option, Atspi.StateType.FOCUSED), 0.6):
        return False
    count = int(safe(option.get_n_actions, 0) or 0)
    names = [str(safe(lambda k=k: option.get_action_name(k), "") or "").lower()
             for k in range(count)]
    if "activate" not in names:
        return False
    return bool(_emit_dropdown_action(lambda: option.do_action(names.index("activate"))))


def select_projected_option(toggle, text):
    """Choose an option of a LibreOffice drop-down and read the choice back.

    The list is opened by clicking the toggle when it is closed, and closed
    the same way if the option cannot be chosen. Success requires the text
    beside the toggle to show the option afterwards or, for a list-only
    drop-down whose text stays empty, the reopened list to select it.
    """
    wanted = str(text).strip()
    shown = _projected_shown_choice(toggle)
    if not state_contains(toggle, Atspi.StateType.CHECKED) and shown == wanted:
        return wanted
    clicked = not state_contains(toggle, Atspi.StateType.CHECKED)
    popup = _open_projected_list(toggle)
    if popup is None:
        if clicked and state_contains(toggle, Atspi.StateType.CHECKED):
            _click_toggle(toggle)
        raise _failure(
            "select_unavailable: the drop-down list could not be found as one "
            "open popup; no option was chosen. Click the drop-down, then "
            "select the option.",
            code="effect_unknown" if _dropdown_input_emitted() else "select_unavailable",
            emitted=_dropdown_input_emitted(),
            phase="emission" if _dropdown_input_emitted() else "precondition")
    options = _dropdown_options(popup)
    matches = [option for option in options if option_name(option) == wanted]
    if len(matches) != 1:
        _click_toggle(toggle)
        names = [option_name(option) for option in options]
        raise _failure(
            "select_option_not_found: {} {} among the options; the list was "
            "closed without choosing. Options: {}.".format(
                _quote(text),
                "is not" if not matches else "occurs more than once",
                ", ".join(_quote(name) for name in names[:30])
                or "none readable"),
            code="invalid_argument", emitted=_dropdown_input_emitted())
    choice = ""
    if _activate_list_row(matches[0]) and _wait_until(
            lambda: not state_contains(toggle, Atspi.StateType.CHECKED), 1.0):
        if shown:
            _wait_until(
                lambda: _projected_shown_choice(toggle) == wanted, 1.0)
            choice = _projected_shown_choice(toggle)
        else:
            choice = _list_choice(toggle)
        if choice == wanted:
            return wanted
    if state_contains(toggle, Atspi.StateType.CHECKED):
        _click_toggle(toggle)
        _wait_until(
            lambda: not state_contains(toggle, Atspi.StateType.CHECKED), 1.0)
    if not choice:
        choice = _projected_shown_choice(toggle) or _list_choice(toggle)
    raise _failure(
        "select_unverified: the drop-down shows {} after choosing {}; its "
        "choice may have changed. Open it and click the option with the GUI."
        .format(_quote(choice) if choice else "no readable choice",
                _quote(text)),
        code="effect_unknown", emitted=_dropdown_input_emitted(), phase="readback")


def projected_list_owner(option):
    """Return the toggle whose open list holds ``option``, or None.

    The popup has no relation back to its toggle, so the owner is the one
    projected combo of the same application that reports its list open.
    """
    app, window = _top_level(option)
    if app is None or option_name(window):
        return None
    count = int(safe(app.get_child_count, 0) or 0)
    stack = [top for top in (safe(lambda k=k: app.get_child_at_index(k))
                             for k in range(count))
             if top is not None and not _same(top, window)]
    budget = FIND_FIRST_BUDGET
    while stack and budget > 0:
        node = stack.pop()
        budget -= 1
        # The opener is drawn, so subtrees that are not showing are skipped.
        if not state_contains(node, Atspi.StateType.SHOWING):
            continue
        if node_role(node) == "toggle button":
            if (state_contains(node, Atspi.StateType.CHECKED)
                    and _projected_toggle(node)):
                return node
            continue
        if should_enumerate_children(node):
            children = int(safe(node.get_child_count, 0) or 0)
            stack.extend(child for child in (
                safe(lambda k=k: node.get_child_at_index(k))
                for k in reversed(range(min(children, MAX_CHILD_FANOUT))))
                if child is not None)
    return None
