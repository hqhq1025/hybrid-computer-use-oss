"""Cross-channel freshness and public evidence helpers."""

from .x11_windows import desktop_layout_state

def gui_lease_after_ax_mutation(before_layout, bounds):
    if before_layout is None or bounds is None:
        return "suspect"
    after_layout = desktop_layout_state(bounds)
    if after_layout is None:
        return "suspect"
    return "fresh" if after_layout == before_layout else "stale"


def external_desktop_transition_after_ax_mutation(
    before_layout,
    after_layout,
    app_windows_before,
    app_windows_after,
):
    """Detect a newly active native window absent from the current AX app."""
    if before_layout is None or after_layout is None:
        return False
    before_active = str(before_layout.get("activeWindow") or "").strip()
    after_active = str(after_layout.get("activeWindow") or "").strip()
    if not after_active or after_active == before_active:
        return False
    before_ids = {
        str(window.get("id") or "").strip()
        for window in before_layout.get("windows") or []
    }
    if after_active in before_ids:
        return False
    if app_windows_before is None or app_windows_after is None:
        return False
    return app_windows_before == app_windows_after




def public_element_record(record):
    if not isinstance(record, dict):
        return record
    return {
        key: value
        for key, value in record.items()
        if not str(key).startswith("_")
    }


