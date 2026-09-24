"""Target hit-test and model-facing action notes."""

from .foundation import (
    Atspi,  # pyright: ignore[reportAttributeAccessIssue]
    node_role,
    safe,
)
from .ax_records import effective_name

A11Y_CHANNEL = "[a11y]"
GUI_CHANNEL = "[gui]"
# Maximum descent depth for coordinate hit testing. AT-SPI
# get_accessible_at_point returns only a direct child, so reaching a leaf
# requires recursion; the bound stops loops on cyclic structures.
HIT_TEST_MAX_DEPTH = 25


def element_at_point(window, x, y):
    """Return the accessible element at screen point (x, y), as a hint only.

    A bare coordinate click cannot otherwise say what it hit; this lets the
    caller compare the intended target with where the click landed.

    Accuracy depends on the toolkit. Measured by recursing to a leaf and
    hit-testing each element's own centre:
        gedit (GTK) 11/11         Nautilus (GTK) 19/25
        LibreOffice (VCL) 12/25   VLC (Qt) 2/25
    VCL and Qt often resolve only to a container, so the result must be
    presented as a hint, never as proof that the intended element was hit.
    """
    node = window
    for _ in range(HIT_TEST_MAX_DEPTH):
        component = safe(node.get_component_iface)
        if component is None:
            break
        child = safe(
            lambda: Atspi.Component.get_accessible_at_point(
                component, int(round(x)), int(round(y)), Atspi.CoordType.SCREEN
            )
        )
        if child is None:
            break
        node = child
    if node is window:
        return None
    return node


def addressing_channel(element_record):
    """Return the note channel: [a11y] for tree targets, [gui] for points."""
    return A11Y_CHANNEL if element_record else GUI_CHANNEL

UNVERIFIED_SYNTHESIS = (
    "Delivery to the intended target was not verified: AT-SPI input synthesis is "
    "global and reports success as soon as the event is queued."
)

# A semantic action is not evidence of effect either. Measured cases:
#   Nautilus file icon `menu`:   do_action returns True but no menu opens,
#                                regardless of prior focus or selection
#   GIMP layer cell `activate`:  returns True, the active layer is unchanged
#                                (zero pixel difference)
#   VLC radio button `Toggle`:   CHECKED flips but the panel does not switch
# The last case means verification must not read the acted-on node's own
# state: the state can change while the behaviour does not.
#
# do_action's return value only says the toolkit accepted the call, not that
# anything happened. Reporting it as success would let the caller move on
# while the UI has not changed.
UNVERIFIED_SEMANTIC = (
    "The toolkit accepted the call; that is not evidence the action took effect. "
    "AT-SPI actions on Linux routinely report success while doing nothing. "
    "Confirm from the returned state before treating this as done."
)

# Correction appended after a coordinate fallback. It only helps when element
# targeting was possible, so callers decide whether to append it.
PREFER_ELEMENT_INDEX = (
    " This did not use element-targeted invocation. If the target appears in the "
    "accessibility tree, prefer AX click with target.ref — it is verified, cheaper, "
    "and does not steal focus."
)


def resolved_note(element, element_record):
    """Describe which element the action was applied to.

    Action notes otherwise say what was done but not to whom. This matters
    most for selectors such as ``push button "Save"``: the caller cannot see
    which live node the selector resolved to.

    The note reports the live node's role and name, not the snapshot
    record's. When they differ it adds a warning: the reference now resolves
    to a different control, a silent retargeting that would otherwise be
    reported as a successful action.
    """
    if element is None or not element_record:
        return None
    live_role = str(node_role(element) or "")
    live_name = str(effective_name(element) or "")
    if live_name:
        shown = "{} {!r}".format(live_role, live_name)
    else:
        shown = "an unnamed {}".format(live_role or "element")
    note = "Resolved the target ref to {}.".format(shown)

    was_role = str(element_record.get("controlType") or "")
    was_name = str(element_record.get("name") or "")
    drifted = []
    if was_role and was_role != live_role:
        drifted.append("role was {!r}, now {!r}".format(was_role, live_role))
    if was_name and was_name != live_name:
        drifted.append("name was {!r}, now {!r}".format(was_name, live_name))
    if drifted:
        note += (
            " WARNING: this no longer matches the snapshot you read ({}). The "
            "reference resolved to a DIFFERENT control than the one you picked; "
            "re-read the tree before trusting this action.".format("; ".join(drifted))
        )
    return note


