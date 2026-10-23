"""The AX select action on drop-downs: choose an option and read it back.

A drop-down is a combo box or list box whose options are its descendants.
Choosing an option succeeds only when the drop-down reads back the choice:
the option's SELECTED state, or for a combo box that names itself after its
choice (GTK, Qt), that name.
"""

from .foundation import (
    Atspi,
    OBJECT_REPLACEMENT,
    node_name,
    node_role,
    raw_extents,
    safe,
    state_contains,
)
from .ax_actions import send_key
from .ax_select import (
    _failure,
    _quote,
    _same,
    _selection_container,
    _wait_until,
    text_between,
)
from .input_primitives import require_window_focus


DROPDOWN_ROLES = ("combo box", "list box")
# Combo box actions that open the popup list: Qt "ShowMenu", GTK "press",
# Chromium "open".
POPUP_OPEN_ACTIONS = ("showmenu", "press", "open")
OPTION_ROLES = ("menu item", "list item", "option", "check menu item",
                "radio menu item", "table cell")
# Options read below one drop-down; HTML <select> lists rarely exceed this.
MAX_DROPDOWN_OPTIONS = 500


def _dropdown_options(node):
    """Return the option nodes below a drop-down, in tree order."""
    options = []
    stack = [node]
    seen = 0
    while stack and seen < MAX_DROPDOWN_OPTIONS:
        current = stack.pop(0)
        seen += 1
        if current is not node and node_role(current) in OPTION_ROLES:
            options.append(current)
            continue
        count = int(safe(current.get_child_count, 0) or 0)
        for index in range(min(count, MAX_DROPDOWN_OPTIONS)):
            child = safe(lambda index=index: current.get_child_at_index(index))
            if child is not None:
                stack.append(child)
    return options


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
    return str(node_name(option) or "").strip()


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
            safe(lambda: combo.do_action(names.index(wanted)), False)
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
        send_key("Down" if target > current else "Up")
        if not _wait_until(lambda c=current: _focused_index(options) != c, 0.6):
            break
    if target is None or _focused_index(options) != target:
        send_key("Escape")
        return False
    send_key("Return")
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
    current item to the option and press Return (Qt).
    """
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
        return option
    emitted = False
    count = int(safe(option.get_n_actions, 0) or 0)
    names = [str(safe(lambda k=k: option.get_action_name(k), "") or "")
             for k in range(count)]
    if "select" in names:
        emitted = True
        safe(lambda: option.do_action(names.index("select")), False)
        if _wait_until(committed, 0.6):
            return option
    index = safe(lambda: option.get_index_in_parent())
    parent = safe(option.get_parent)
    if (index is not None and index >= 0 and parent is not None
            and safe(node.get_selection_iface) is not None
            and _same(safe(parent.get_parent), node)):
        # GTK commits through the combo box's own Selection, which sets the
        # active item; the popup menu's Selection only highlights an item.
        emitted = True
        safe(lambda: Atspi.Selection.select_child(node, int(index)), False)
        if _wait_until(committed, 0.6):
            return option
    container = _selection_container(option)
    if container is not None and index is not None and index >= 0:
        emitted = True
        safe(lambda: Atspi.Selection.select_child(container, int(index)), False)
        if _wait_until(committed, 0.6):
            return option
    if shows_choice:
        emitted = True
        if _commit_from_popup(node, option, window) and _wait_until(committed, 1.0):
            return option
        raise _failure(
            "select_unverified: the drop-down shows {} after choosing {}; "
            "its choice may have changed. Open it, then press Return on the "
            "option or click it with the GUI.".format(
                _quote(_shown_choice(node)), _quote(text)),
            code="effect_unknown", emitted=True, phase="readback")
    if not emitted:
        raise _failure(
            "select_unavailable: the option {} has no select action and its "
            "list has no selection interface; no input was emitted. Open the "
            "drop-down and click the option.".format(_quote(text)))
    raise _failure(
        "select_unverified: the option {} did not report itself selected."
        .format(_quote(text)),
        code="effect_unknown", emitted=True, phase="readback")


def owning_dropdown(node):
    """Return the combo box whose popup list holds the option ``node``."""
    if node_role(node) not in OPTION_ROLES:
        return None
    current = node
    for _ in range(3):
        current = safe(current.get_parent)
        if current is None:
            return None
        if node_role(current) == "combo box":
            return current
    return None
