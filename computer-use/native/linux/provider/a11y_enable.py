"""Turn on desktop accessibility the way a screen reader does.

Toolkits decide whether to export an accessibility tree from session state,
and most decide only once, when the application starts:

- GTK 3 and GTK 4 always register on the AT-SPI bus.
- Qt 5/6 and Gecko register only if `org.a11y.Status.IsEnabled` was true
  when they started.
- Chromium and Electron read ACCESSIBILITY_ENABLED / GNOME_ACCESSIBILITY /
  QT_ACCESSIBILITY, then `IsEnabled`, then the GSettings key
  `toolkit-accessibility`, once at startup. Even then they expose only the
  application and its frames until an assistive technology calls
  GetAttributes or GetRelationSet, which they take as the sign that a screen
  reader is present (the heuristic Chromium keeps for Orca).
- GTK 2 needs the gail and atk-bridge GTK modules. A running GTK 2
  application loads them when the XSETTINGS value Gtk/Modules names them.

A plain desktop session has none of this switched on, so applications the
user starts expose no tree. On provider start this module sets `IsEnabled`
(at-spi-bus-launcher mirrors it to `toolkit-accessibility`) and, on Xfce,
adds gail and atk-bridge to Gtk/Modules. GNOME's settings daemon derives
Gtk/Modules from `toolkit-accessibility` itself. Both settings persist in
the user's configuration, as when Orca enables them.

`ScreenReaderEnabled` is never set: no toolkit above needs it, and the bus
launcher mirrors it to the GSettings key that starts Orca on GNOME.

Applications already running when accessibility is switched on stay
invisible if their toolkit reads the setting only at startup;
`invisible_window_notes` names them and says what to restart.
"""

from .foundation import provider_bus_name, safe

try:
    import gi

    gi.require_version("Gio", "2.0")
    from gi.repository import Gio, GLib
except (ImportError, ValueError):
    Gio = None
    GLib = None


A11Y_BUS_NAME = "org.a11y.Bus"
A11Y_BUS_PATH = "/org/a11y/bus"
A11Y_STATUS_INTERFACE = "org.a11y.Status"
XFCONF_NAME = "org.xfce.Xfconf"
XFCONF_PATH = "/org/xfce/Xfconf"
XFCONF_INTERFACE = "org.xfce.Xfconf"
GTK2_ACCESSIBILITY_MODULES = ("gail", "atk-bridge")
DBUS_TIMEOUT_MS = 2000


class SessionBus:
    """The few synchronous session-bus calls this module needs."""

    def __init__(self, connection):
        self.connection = connection

    @classmethod
    def open(cls):
        if Gio is None:
            raise RuntimeError("Gio is unavailable")
        return cls(Gio.bus_get_sync(Gio.BusType.SESSION, None))

    def _call(self, name, path, interface, method, args, reply_type):
        return self.connection.call_sync(
            name, path, interface, method, args,
            GLib.VariantType.new(reply_type) if reply_type else None,
            Gio.DBusCallFlags.NONE, DBUS_TIMEOUT_MS, None,
        )

    def has_owner(self, name):
        reply = self._call(
            "org.freedesktop.DBus", "/org/freedesktop/DBus",
            "org.freedesktop.DBus", "NameHasOwner",
            GLib.Variant("(s)", (name,)), "(b)",
        )
        return bool(reply.unpack()[0])

    def get_property(self, name, path, interface, prop):
        reply = self._call(
            name, path, "org.freedesktop.DBus.Properties", "Get",
            GLib.Variant("(ss)", (interface, prop)), "(v)",
        )
        return reply.unpack()[0]

    def set_bool_property(self, name, path, interface, prop, value):
        self._call(
            name, path, "org.freedesktop.DBus.Properties", "Set",
            GLib.Variant("(ssv)", (interface, prop, GLib.Variant("b", value))),
            None,
        )

    def xfconf_get_string(self, channel, prop):
        reply = self._call(
            XFCONF_NAME, XFCONF_PATH, XFCONF_INTERFACE, "GetProperty",
            GLib.Variant("(ss)", (channel, prop)), "(v)",
        )
        return str(reply.unpack()[0])

    def xfconf_set_string(self, channel, prop, value):
        self._call(
            XFCONF_NAME, XFCONF_PATH, XFCONF_INTERFACE, "SetProperty",
            GLib.Variant("(ssv)", (channel, prop, GLib.Variant("s", value))),
            None,
        )


def _is_missing_property(error):
    return "PropertyNotFound" in str(error)


def merged_gtk_modules(current):
    """Return Gtk/Modules with gail and atk-bridge added, or None if present."""
    modules = [item for item in str(current or "").split(":") if item]
    missing = [item for item in GTK2_ACCESSIBILITY_MODULES if item not in modules]
    if not missing:
        return None
    return ":".join(modules + missing)


def enable_session_accessibility(bus=None):
    """Switch session accessibility on; return what was done, for diagnostics.

    Idempotent: a session that already has accessibility on (for example
    one that exported a11y-env.sh) sees reads and no writes.
    """
    facts = {"changed": [], "errors": []}
    try:
        bus = bus or SessionBus.open()
    except Exception as error:
        facts["errors"].append("session bus: {}".format(error))
        return facts
    try:
        # The call also starts at-spi-bus-launcher through D-Bus activation
        # when no desktop component has started it yet.
        enabled = bool(bus.get_property(
            A11Y_BUS_NAME, A11Y_BUS_PATH, A11Y_STATUS_INTERFACE, "IsEnabled"))
        if not enabled:
            bus.set_bool_property(
                A11Y_BUS_NAME, A11Y_BUS_PATH, A11Y_STATUS_INTERFACE,
                "IsEnabled", True)
            facts["changed"].append("org.a11y.Status.IsEnabled=true")
    except Exception as error:
        facts["errors"].append("org.a11y.Status: {}".format(error))
    try:
        if bus.has_owner(XFCONF_NAME):
            try:
                current = bus.xfconf_get_string("xsettings", "/Gtk/Modules")
            except Exception as error:
                if not _is_missing_property(error):
                    raise
                current = ""
            merged = merged_gtk_modules(current)
            if merged is not None:
                bus.xfconf_set_string("xsettings", "/Gtk/Modules", merged)
                facts["changed"].append("xsettings Gtk/Modules={}".format(merged))
    except Exception as error:
        facts["errors"].append("xsettings Gtk/Modules: {}".format(error))
    return facts


# Applications already asked for their attributes, by bus name. A bus name
# is unique for the lifetime of one connection, so a restarted application
# is asked again.
_attribute_requested = set()


def request_full_tree(app):
    """Ask an application for its attributes once, as a screen reader does.

    Chromium and Electron treat GetAttributes on any of their objects as the
    sign that an assistive technology is present and only then build the
    tree below their frames (Chrome 150: 2 nodes before, 238 after; VS Code
    1.130: 2 before, 679 after). Other toolkits answer it as an ordinary
    read. One call per application connection keeps the cost to one round
    trip.
    """
    key = provider_bus_name(app)
    if not key or key in _attribute_requested:
        return
    _attribute_requested.add(key)
    safe(app.get_attributes)
