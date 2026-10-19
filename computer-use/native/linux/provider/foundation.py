#!/usr/bin/env python3

from functools import lru_cache
import os
from pathlib import Path
import warnings

warnings.filterwarnings("ignore", category=DeprecationWarning)

import gi

gi.require_version("Atspi", "2.0")

try:
    gi.require_version("Gdk", "3.0")
    gi.require_version("GdkPixbuf", "2.0")
    from gi.repository import Gdk, GdkPixbuf
except (ImportError, ValueError):
    Gdk = None
    GdkPixbuf = None

from gi.repository import Atspi


MAX_ELEMENTS = 1200

# Upper bound on the characters of one rendered tree.
#
# MAX_ELEMENTS bounds nodes, but MCP clients limit bytes: clients replace
# tool results larger than about 100 KB with a file pointer, which leaves the
# agent with nothing when it cannot read files, and a 1200-node tree can
# exceed that when lines are long. The budget is enforced while rendering
# because truncating afterwards would waste the per-node record cost.
MAX_RENDERED_CHARS = 40000

# Wall-clock budget for one tree traversal.
#
# Node budgets do not bound time because per-node cost varies by an order of
# magnitude (cross-process accessor calls can cost 20-25 ms per node), so a
# full walk of a large GIMP window can exceed the Go layer's 30 s timeout.
# The cheap pruning probe lowers the typical cost; this deadline bounds the
# worst case regardless of the cost distribution.
TREE_DEADLINE_SECONDS = 20.0
MAX_DEPTH = 64
DEFAULT_TEXT_LIMIT = 500
# Maximum FOCUSABLE controls to try when raising a window. Grabbing focus
# really moves focus inside the application, so only a few likely candidates
# are tried instead of scanning the whole tree.
FOCUS_GRAB_CANDIDATES = 8
# Upper bound for plausible screen coordinates and sizes. Larger values can
# only be INT_MIN-style sentinels of unrendered controls; no multi-monitor
# layout reaches this magnitude.
MAX_SANE_EXTENT = 100000
# Minimum settle time between an action and the next snapshot. When the set
# of top-level windows is unchanged, this is the whole wait.
SETTLE_MIN_SECONDS = 0.12
# Maximum extra wait for focus to settle after a window opens or closes.
# Focus transfers take about 0.05 s, so this leaves a wide margin.
# A timeout means the new window never took focus (for example a
# non-focusable tooltip); the current state is then returned with an
# explicit "not settled" note instead of blocking the tool.
SETTLE_FOCUS_TIMEOUT_SECONDS = 0.8
SETTLE_POLL_SECONDS = 0.02
# With the AT-SPI event monitor active, the settle wait ends this long after
# the application's latest event, but not before SETTLE_EVENT_FLOOR_SECONDS
# and never later than SETTLE_MIN_SECONDS. Measured on LibreOffice Calc 7.3
# and GIMP 2.10 with xdotool input: the first event arrives 1-43 ms after the
# input, the burst for a keypress ends within 50 ms, and GIMP dialogs join
# the tree 35-65 ms after the input inside a continuous burst.
SETTLE_QUIET_SECONDS = 0.05
SETTLE_EVENT_FLOOR_SECONDS = 0.07
SCREENSHOT_SCOPE_SCREEN = "screen"
# Sampling stride and threshold for pixel comparison. An 8 px stride keeps
# a full-window comparison to a few tens of milliseconds, negligible next to
# app resolution, and still detects moved text or an opened or closed
# dialog. Changes are judged by persistence across two post-action captures
# rather than by a percentage threshold; see persistent_pixel_change.
PIXEL_DIFF_STRIDE = 8
PIXEL_DIFF_THRESHOLD = 30
SCREENSHOT_MAX_BASE64_CHARS = 480000

GUI_MAX_ACTIONS = 64
GUI_MAX_SCROLL_DELTA = 2000
GUI_MAX_DRAG_POINTS = 256
GUI_MAX_KEYS = 8
GUI_MAX_KEY_LENGTH = 64
GUI_MAX_TEXT_LENGTH = 10000
GUI_MAX_TOTAL_WAIT_SECONDS = 20
GUI_MAX_SETTLE_MS = 3000
# The GUI screenshot is delivered at the size the model sees. GPT-5.4 at
# detail high shrinks a 1920x1080 image to 1365x768 (the short side to 768),
# so a larger image would put the model's coordinates in a smaller space than
# the delivered pixels. 1366x768 also fits Claude's image limit unscaled.
GUI_MODEL_MAX_WIDTH = 1366
GUI_MODEL_MAX_HEIGHT = 768
GUI_SETTLE_POLL_SECONDS = 0.1
GUI_QUIESCENT_MIN_SAMPLES = 4
GUI_QUIESCENT_MIN_SECONDS = 0.3
GUI_SNAPSHOT_LAYOUT_ATTEMPTS = 3
GUI_MAX_LAYOUT_WINDOWS = 128
MAX_CLICK_COUNT = 10
MAX_SCROLL_PAGES = 100
# A pointer action can synchronously enqueue its events while the toolkit changes
# focus asynchronously.  Give that focus transition a small, app-agnostic barrier
# before dispatching keyboard input in the same batch.
GUI_POINTER_KEYBOARD_BARRIER_SECONDS = 0.12
GUI_KEYBOARD_ACTION_INTERVAL_SECONDS = 0.08
GUI_ACTION_TYPES = {
    "screenshot", "click", "double_click", "move", "drag",
    "scroll", "type", "keypress", "wait",
}
GUI_POINTER_ACTION_TYPES = {
    "click", "double_click", "move", "drag", "scroll",
}
GUI_KEYBOARD_ACTION_TYPES = {"type", "keypress"}
GUI_KEYBOARD_MICRO_WAIT_MAX_SECONDS = 0.3
GUI_FRAME_GUARDED_ACTION_TYPES = GUI_POINTER_ACTION_TYPES
GUI_INPUT_ACTION_TYPES = GUI_POINTER_ACTION_TYPES | GUI_KEYBOARD_ACTION_TYPES
GUI_MOUSE_MODIFIERS = ("CTRL", "CONTROL", "SHIFT", "ALT", "SUPER")
GUI_ACTION_FIELDS = {
    "screenshot": {"type"},
    "click": {"type", "x", "y", "button", "keys"},
    "double_click": {"type", "x", "y", "keys"},
    "move": {"type", "x", "y", "keys"},
    "drag": {"type", "path", "keys"},
    "scroll": {"type", "x", "y", "scroll_x", "scroll_y", "keys"},
    "type": {"type", "text"},
    "keypress": {"type", "keys"},
    "wait": {"type"},
}
AX_WINDOW_ROLES = {
    "frame", "window", "dialog", "alert", "file chooser", "color chooser",
}
AX_WINDOW_ACTION_TIMEOUT_SECONDS = 1.2
AX_WINDOW_ACTION_POLL_SECONDS = 0.04


class OperationFailure(RuntimeError):
    """A runtime failure with machine-readable mutation/emission facts."""

    def __init__(
        self,
        message,
        failure_code,
        failure_phase,
        input_events_emitted=False,
        candidates=None,
        candidate_count=0,
        failure_subject="",
        replacement_window=None,
        failure_facts_complete=None,
        provider_reset_required=False,
    ):
        super().__init__(message)
        self.failure_code = failure_code
        self.failure_phase = failure_phase
        self.input_events_emitted = bool(input_events_emitted)
        self.candidates = list(candidates or [])[:5]
        self.candidate_count = max(
            int(candidate_count or 0),
            len(self.candidates),
        )
        self.failure_subject = (
            str(failure_subject or "").strip().lower()
            or infer_failure_subject(failure_code, message)
        )
        self.replacement_window = dict(replacement_window or {})
        self.provider_reset_required = bool(provider_reset_required)
        if failure_facts_complete is None:
            failure_facts_complete = failure_code not in {
                "observation_incomplete",
                "effect_unknown",
            }
        self.failure_facts_complete = bool(failure_facts_complete)


def infer_failure_subject(failure_code, message):
    code = str(failure_code or "").strip().lower()
    text = str(message or "").strip().lower()
    if code in {"stale_app_ref", "ax_app_ambiguous", "ax_app_unavailable"}:
        return "app"
    if code in {
        "stale_window_ref", "stale_window", "window_target_invalid",
        "window_identity_ambiguous",
    }:
        return "window"
    if code in {
        "stale_element_ref", "semantic_target_invalid",
        "semantic_target_not_found", "semantic_target_ambiguous",
        "target_unstable", "coordinate_target_unverified",
        "semantic_action_unavailable",
    }:
        return "element"
    if code == "observation_incomplete":
        if "application inventory" in text and "window inventory" not in text:
            return "app"
        if (
            "window inventory" in text
            or "logical window" in text
            or "window identity" in text
        ):
            return "window"
        return "element"
    return ""


class InventoryResult:
    """A bounded provider inventory with explicit completeness."""

    def __init__(self, items=None, complete=True, errors=None):
        self.items = list(items or [])
        self.complete = bool(complete)
        self.errors = list(errors or [])[:16]

    def __iter__(self):
        return iter(self.items)

    def __len__(self):
        return len(self.items)

    def __bool__(self):
        return bool(self.items)

    def __getitem__(self, index):
        return self.items[index]


def inventory_complete(value):
    return bool(getattr(value, "complete", True))


def inventory_errors(value):
    return list(getattr(value, "errors", []) or [])


def a11y_screenshots_enabled():
    """Return whether AX responses include a screenshot; enabled by default.

    Set `COMPUTER_USE_A11Y_SCREENSHOTS=0` to disable. The switch exists for
    experiments, not everyday use, and does not affect the GUI tool, which
    always returns its screenshot.
    """
    value = os.environ.get("COMPUTER_USE_A11Y_SCREENSHOTS", "1").strip().lower()
    return value not in {"0", "false", "no", "off"}


# Fraction of the node budget after which unnamed structural containers with
# no actions and no value are dropped. Plain depth-first truncation drops
# whatever the traversal reaches last, so late subtrees vanish entirely.
# Structural containers (filler, panel, separator) have no actionable value
# for the agent and are the only class that can be sacrificed safely.
BUDGET_PRESSURE_RATIO = 0.8

# Role criteria for pruning: prefix and suffix matches plus an exact set,
# combined with a visibility requirement. This is the most aggressive option
# that still keeps every action target in complete dialog flows. It relies
# on rendering fidelity: table cells must carry frames and roles must not be
# hard-coded.
PRUNE_ROLE_SUFFIXES = (
    "item", "button", "heading", "label", "scrollbar", "searchbox", "textbox",
    "link", "tabelement", "textfield", "textarea", "menu",
)
PRUNE_ROLE_EXACT = {
    "alert", "canvas", "check box", "combo box", "entry", "icon", "image",
    "paragraph", "scroll bar", "section", "slider", "static", "table cell",
    "terminal", "text", "table", "list box", "tree", "tree item", "list item",
    "page tab", "page tab list", "spin button", "tool bar", "status bar",
    "frame", "dialog", "window",
}


def is_interactive_role(role):
    """Return whether the role is one the agent may need to act on or read."""
    normalized = role.lower().strip()
    if normalized.startswith("document"):
        return True
    compact = normalized.replace(" ", "")
    if compact.endswith(PRUNE_ROLE_SUFFIXES):
        return True
    return normalized in PRUNE_ROLE_EXACT


def frame(x, y, width, height):
    if width is None or height is None or width < 0 or height < 0:
        return None
    return {
        "x": float(x),
        "y": float(y),
        "width": float(width),
        "height": float(height),
    }


def safe(call, default=None):
    try:
        value = call()
        if value is None:
            return default
        return value
    except Exception:
        return default


def has_text_iface(node):
    """Return whether the node implements the Text interface.

    Accessible.is_text cannot be used: libatspi added it in 2.52, and it is
    missing from at-spi2-core 2.44 (Ubuntu 22.04). Because it is an attribute
    access, the AttributeError is raised before safe() is entered, so safe()
    cannot contain it and the whole observation fails.

    get_text_iface() exists in old and new versions alike and is the more
    basic, portable check.
    """
    return safe(node.get_text_iface) is not None


def has_editable_text_iface(node):
    """Return whether the node implements EditableText; see has_text_iface."""
    return safe(node.get_editable_text_iface) is not None


def require_desktop_session():
    missing = []
    if not os.environ.get("XDG_RUNTIME_DIR"):
        missing.append("XDG_RUNTIME_DIR")
    if not os.environ.get("DBUS_SESSION_BUS_ADDRESS"):
        missing.append("DBUS_SESSION_BUS_ADDRESS")
    if missing:
        raise RuntimeError(
            "Linux runtime requires an active desktop session; missing "
            + ", ".join(missing)
        )


def desktop():
    return Atspi.get_desktop(0)


def child_count(node):
    return int(safe(node.get_child_count, 0) or 0)


def child_at(node, index):
    return safe(lambda: node.get_child_at_index(index))


def enumerate_children(node, label):
    """Enumerate direct children without converting provider failures to absence."""
    errors = []
    try:
        count = int(node.get_child_count())
    except Exception as error:
        return InventoryResult(
            complete=False,
            errors=["{} child_count failed: {}".format(label, type(error).__name__)],
        )
    if count < 0:
        return InventoryResult(
            complete=False,
            errors=["{} child_count was negative".format(label)],
        )
    limit = min(count, HARD_CHILD_CAP)
    complete = count <= HARD_CHILD_CAP
    if not complete:
        errors.append("{} child_count exceeded safety cap".format(label))
    items = []
    for index in range(limit):
        try:
            child = node.get_child_at_index(index)
        except Exception as error:
            complete = False
            errors.append(
                "{} child {} failed: {}".format(
                    label, index, type(error).__name__
                )
            )
            continue
        if child is None:
            complete = False
            errors.append("{} child {} was unavailable".format(label, index))
            continue
        items.append((index, child))
    return InventoryResult(items, complete=complete, errors=errors)


def node_name(node):
    return str(safe(node.get_name, "") or "")


# Chromium/Electron put U+FFFC (OBJECT REPLACEMENT CHARACTER) in text as a
# placeholder for embedded objects. It carries no information for the agent
# but renders as `Value:` followed by placeholder glyphs, which looks like
# content and suggests that the control has a value.
OBJECT_REPLACEMENT = "\ufffc"


def limit_text(value, text_limit=DEFAULT_TEXT_LIMIT):
    text = str(value or "").replace(OBJECT_REPLACEMENT, "")
    if text_limit is None:
        return text
    if len(text) > text_limit:
        return text[:text_limit] + "..."
    return text


def node_role(node):
    return str(safe(node.get_role_name, "") or "")


def node_pid(node):
    getter = getattr(node, "get_process_id", None)
    value = safe(getter, 0) if callable(getter) else 0
    try:
        return int(value or 0)
    except Exception:
        return 0


@lru_cache(maxsize=1024)
def process_generation(pid, proc_root="/proc"):
    """Linux process start time, used only as a PID-reuse generation."""
    try:
        raw = Path(proc_root, str(int(pid)), "stat").read_text(
            encoding="utf-8", errors="replace"
        )
        # comm is parenthesized and may contain spaces or ')'. Parse only the
        # suffix after the final ')' so field 22 (starttime) cannot shift.
        close = raw.rfind(")")
        if close < 0:
            return ""
        suffix = raw[close + 1 :].strip().split()
        return suffix[19] if len(suffix) > 19 else ""
    except Exception:
        return ""


def provider_bus_name(node):
    return str(safe(lambda: node.app.bus_name, "") or "")


def provider_object_path(node):
    return str(safe(lambda: node.path, "") or "")


def state_contains(node, state):
    state_set = safe(node.get_state_set)
    if state_set is None:
        return False
    return bool(safe(lambda: state_set.contains(state), False))


def state_value(node, state):
    """Return provider state as true, false, or unknown."""
    try:
        state_set = node.get_state_set()
        if state_set is None:
            return None
        return bool(state_set.contains(state))
    except Exception:
        return None


# Hard cap on a node's child count. LibreOffice Calc sheet nodes report
# 2147483647 children: the accessible range is the whole sheet (16384
# columns x 1048576 rows), getAccessibleChildCount() returns rows * cols
# (about 1.7e10), and the count reaches clients clamped to the int32
# maximum. A naive traversal of such a node never finishes. See LibreOffice
# sc/source/ui/Accessibility/AccessibleTableBase.cxx.
HARD_CHILD_CAP = 4096

# Maximum child_at() calls per container. HARD_CHILD_CAP decides whether to
# enumerate at all; this bounds how many children are enumerated, covering
# implementations that misreport their child count without declaring
# MANAGES_DESCENDANTS.
MAX_CHILD_FANOUT = 512

# Containers that declare MANAGES_DESCENDANTS are still enumerated when they
# report at most this many children. The state hints that a container may be
# large; it does not prove it. The Nautilus sidebar declares it with only 12
# children and does not implement Table, so coordinate addressing fails and
# the whole sidebar would be invisible. Truly huge containers (a Calc sheet
# reports about 2.1 billion children) are stopped by HARD_CHILD_CAP instead.
MANAGED_ENUMERATE_CAP = 256

# Node budget for find_first(), which focus_and_selection() uses to locate
# focus; without it, a predicate that never matches walks the whole tree.
FIND_FIRST_BUDGET = 4000

# Semantic action names that the click tool invokes. preferred_action_index()
# uses this set to choose the action, and node_actions() uses it to avoid
# listing the same actions again.
CLICK_COVERED_ACTIONS = {
    "click",
    "press",
    "activate",
    "default.activate",
    "invoke",
    "select",
    "toggle",
    "open",
    # Chromium/Electron default action. VS Code nodes expose
    # ('doDefault', 'showContextMenu'), and `doDefault` is their click entry
    # point. Without it these elements get no [has-click-action] mark and
    # semantic clicks fail, so Electron applications could not be clicked
    # through the accessibility channel at all.
    "dodefault",
    # Gecko (Thunderbird) names checkbox actions after the resulting state
    # instead of "toggle": a checked box offers `uncheck`, an unchecked one
    # `check`, and only the applicable one is exposed. Invoking it therefore
    # toggles the box, which is what click should do. Without these names,
    # Gecko checkboxes fall back to coordinate clicks, and settings dialogs
    # consist mostly of checkboxes.
    "check",
    "uncheck",
}

# Actions whose names contain click/press/activate but that act on another
# element. They must be excluded explicitly: the substring fallback in
# preferred_action_index() would match `clickAncestor`, so the agent would
# believe it clicked the target while an ancestor was clicked, with nothing
# in the result or the tree to show it. In VS Code many nodes expose
# ('clickAncestor', 'showContextMenu').
NON_SELF_ACTIONS = {"clickancestor"}

# Actions present on every node, which therefore carry no information.
#
# On chrome://flags, `showContextMenu` appears on all 1200 rendered nodes,
# because Chrome/Blink attaches it to every DOM element. Keeping it has two
# effects:
#
#  1. Pruning stops working. is_structural_filler requires a node without
#     actions, and since every node has this one, nothing counts as filler;
#     the tree then overflows the rendered-size budget (see
#     MAX_RENDERED_CHARS).
#  2. Every `[actions=...]` segment carries the same noise.
#
# It is not a usable way to act on the element either: it opens a context
# menu instead of activating the element.
UNIVERSAL_ACTIONS = {"showcontextmenu"}


def normalized_action(label):
    """Normalize an action name to lowercase alphanumerics for comparison.

    Toolkits spell the same action differently: Chromium/Electron use
    `clickAncestor`, Gecko/Thunderbird `click ancestor`. Both occur in
    practice, and excluding only one spelling would let the substring
    fallback match the other and click the ancestor again.
    """
    return "".join(ch for ch in str(label or "").lower() if ch.isalnum())


# Collapsed menus are not descended into. With the default 1200-node budget,
# a full LibreOffice menu bar (about 780 nodes) takes the whole budget and
# leaves no room for table cells. The rule applies only to menu roles: other
# intermediate containers (panel, scroll pane) often lack SHOWING on
# LibreOffice, and filtering them would empty the tree.
MENU_ROLES = {
    "menu",
    "menu bar",
    "menu item",
    "check menu item",
    "radio menu item",
    "popup menu",
}


def should_enumerate_children(node, count=None, facts=None):
    """Return whether the node's children should be enumerated.

    Each guard costs one provider round trip in this process (the libatspi
    cache is not consulted; see NodeFacts). Pass the node's ``facts`` so the
    traversal reuses the child count and state set it already read:

    1. MANAGES_DESCENDANTS is the AT-SPI contract for very large containers
       such as tables: "the children should not, and need not, be enumerated
       by the client". Calc sheets set it (LibreOffice
       AccessibleSpreadsheet.cxx). Such containers are addressed by
       coordinate with Table.get_accessible_at(row, col) or by hit testing
       with Component.get_accessible_at_point() instead.
    2. A hard child_count cap stops implementations that misreport their
       child count.
    """
    if count is None:
        count = child_count(node) if facts is None else facts.child_count()
    if count > HARD_CHILD_CAP:
        return False
    manages = (
        state_contains(node, Atspi.StateType.MANAGES_DESCENDANTS)
        if facts is None
        else facts.has_state(Atspi.StateType.MANAGES_DESCENDANTS)
    )
    if manages:
        # The declaration is a hint about size. A small reported count
        # contradicts it, so trust the count: enumerating a dozen children is
        # safe and is the only way to reach them, because such containers
        # often do not implement Table and coordinate addressing cannot help.
        return count <= MANAGED_ENUMERATE_CAP
    return True


def sane_rect_frame(rect):
    if (
        rect is None
        or rect.width <= 0
        or rect.height <= 0
        or rect.width > MAX_SANE_EXTENT
        or rect.height > MAX_SANE_EXTENT
    ):
        return None
    # Unrendered GTK controls report origins near INT_MIN with plausible sizes
    # (often 1x1), so a size check alone lets coordinates such as -2147483648
    # into the element tree, where coordinate clicks and drags would land at
    # meaningless positions.
    if abs(rect.x) > MAX_SANE_EXTENT or abs(rect.y) > MAX_SANE_EXTENT:
        return None
    return frame(rect.x, rect.y, rect.width, rect.height)


def extents(node):
    component = safe(node.get_component_iface)
    if component is None:
        return None
    rect = safe(lambda: Atspi.Component.get_extents(component, Atspi.CoordType.SCREEN))
    return sane_rect_frame(rect)


class NodeFacts:
    """Provider reads of one node, each performed at most once per snapshot.

    Every read below is a D-Bus round trip of 30-60 us: the libatspi client
    cache is not consulted in this process, because it only answers while
    atspi_event_main() runs or after set_cache_mask(), and the provider does
    neither. The pruning probe, the enumeration guards, the record builder
    and the focus lookup used to read the same fields again, which made a
    Calc snapshot about three times slower than one pass over the tree.

    An instance lives for one snapshot only. Values are never carried across
    requests, because the provider cannot tell which of them went stale.

    `strict()` keeps provider failures distinct from empty values, as the
    pruning probe requires. `value()` and `has_state()` follow the semantics
    of `safe()` and `state_contains()`, as the record builder requires.
    Looking up a missing method raises in both cases, as it did before.
    """

    __slots__ = ("node", "_reads", "_scope")

    def __init__(self, node, scope=None):
        self.node = node
        self._reads = {}
        # Facts shared by every node of one snapshot; see pid().
        self._scope = scope if scope is not None else {}

    def _operation(self, field):
        node = self.node
        if field == "child_count":
            return node.get_child_count
        if field == "states":
            return node.get_state_set
        if field == "component":
            return node.get_component_iface
        if field == "role":
            return node.get_role_name
        if field == "name":
            return node.get_name
        if field == "description":
            return node.get_description
        if field == "extents":
            component, _error = self.strict("component")
            return lambda: Atspi.Component.get_extents(
                component, Atspi.CoordType.SCREEN
            )
        raise KeyError(field)

    def strict(self, field):
        """Return ``(value, error_name)``; read the provider at most once."""
        cached = self._reads.get(field)
        if cached is None:
            operation = self._operation(field)
            try:
                cached = (operation(), None)
            except Exception as exc:
                cached = (None, type(exc).__name__)
            self._reads[field] = cached
        return cached

    def value(self, field, default=None):
        value, error = self.strict(field)
        if error is not None or value is None:
            return default
        return value

    def has_state(self, state):
        """Return state_contains() for this node."""
        state_set = self.value("states")
        if state_set is None:
            return False
        try:
            return bool(state_set.contains(state))
        except Exception:
            return False

    def state_flag(self, state):
        """Return state_value() for this node: true, false or unknown."""
        try:
            state_set, error = self.strict("states")
        except Exception:
            return None
        if error is not None or state_set is None:
            return None
        try:
            return bool(state_set.contains(state))
        except Exception:
            return None

    def child_count(self):
        """Return child_count() for this node: 0 when unreadable."""
        return int(self.value("child_count", 0) or 0)

    def screen_frame(self):
        """Return extents() for this node."""
        component = self.value("component")
        if component is None:
            return None
        return sane_rect_frame(self.value("extents"))

    def relative_frame(self, window_bounds):
        bounds = self.screen_frame()
        if bounds is None or window_bounds is None:
            return bounds
        return frame(
            bounds["x"] - window_bounds["x"],
            bounds["y"] - window_bounds["y"],
            bounds["width"],
            bounds["height"],
        )

    def pid(self):
        """Return node_pid() for this node, read once per application.

        libatspi answers get_process_id() by asking the bus daemon for the
        process that owns the node's bus name, so every node of one
        application returns the same value. Reading it for each record cost
        one round trip per kept node.
        """
        bus = provider_bus_name(self.node)
        if not bus:
            return node_pid(self.node)
        pids = self._scope.setdefault("pid_by_bus", {})
        pid = pids.get(bus)
        if pid is None:
            pid = node_pid(self.node)
            if pid:
                pids[bus] = pid
        return pid


def strict_prune_probe(node, window_bounds, text_limit, facts=None):
    """Read pruning inputs without converting provider errors into empty values.

    Child count and frame are read first. A node without a frame and without
    a read error is pruned whatever its role, name or description, so those
    three fields are read only for nodes that may be kept. Every field that
    is read stays in `facts` for the record builder.
    """
    if facts is None:
        facts = NodeFacts(node)
    values = {}
    errors = {}

    def read(field, default=None):
        value, error = facts.strict(field)
        if error is not None:
            errors["frame" if field == "extents" else field] = error
            return default
        return value

    raw_count = read("child_count")
    if raw_count is None:
        values["child_count"] = None
    else:
        try:
            values["child_count"] = max(0, int(raw_count or 0))
        except Exception as exc:
            errors["child_count"] = type(exc).__name__
            values["child_count"] = None

    component = read("component")
    if component is None:
        values["frame"] = None
    else:
        bounds = sane_rect_frame(read("extents"))
        if bounds is not None and window_bounds is not None:
            bounds = frame(
                bounds["x"] - window_bounds["x"],
                bounds["y"] - window_bounds["y"],
                bounds["width"],
                bounds["height"],
            )
        values["frame"] = bounds

    if values["frame"] is None and not errors:
        values["role"] = values["name"] = values["description"] = ""
        return values, errors

    values["role"] = str(read("role", "") or "")
    values["name"] = limit_text(
        str(read("name", "") or ""),
        text_limit=text_limit,
    )
    values["description"] = limit_text(
        str(read("description", "") or ""),
        text_limit=text_limit,
    )
    return values, errors
