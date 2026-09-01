"""macOS provider backed by Accessibility through JXA and CoreGraphics input."""

from __future__ import annotations

import base64
import ctypes
import hashlib
import json
import os
import re
import struct
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Any

from native.common.provider_v1.wire_v2 import NativeProviderError
from native.common.provider_v1.images import png_content_digest


JXA_SOURCE = r"""
ObjC.import('Foundation');
function safe(fn, fallback) { try { const value = fn(); return value === undefined ? fallback : value; } catch (_) { return fallback; } }
function attr(element, name, fallback) {
  return safe(() => element.attributes.byName(name).value(), fallback);
}
function arrayValue(value) {
  if (value === null || value === undefined) return [];
  try { return Array.from(value); } catch (_) { return []; }
}
function roleName(value) {
  const raw = String(value || '').replace(/^AX/, '').replace(/([a-z])([A-Z])/g, '$1 $2').toLowerCase();
  const aliases = {'textfield':'entry', 'textarea':'text', 'checkbox':'check box', 'radiobutton':'radio button',
    'popupbutton':'combo box', 'statictext':'label', 'scrollarea':'scroll pane',
    'tabgroup':'page tab list', 'toolbar':'tool bar', 'splitgroup':'split pane'};
  return aliases[raw.replace(/ /g, '')] || aliases[raw] || raw || 'unknown';
}
function frameOf(element) {
  const pos = safe(() => element.position(), null), size = safe(() => element.size(), null);
  if (!pos || !size || pos.length < 2 || size.length < 2) return null;
  return {x:Number(pos[0]), y:Number(pos[1]), width:Number(size[0]), height:Number(size[1])};
}
function actionNames(element) {
  return arrayValue(safe(() => element.actions(), [])).map(a => String(safe(() => a.name(), ''))).filter(Boolean);
}
function processInfo(process) {
  const pid = Number(safe(() => process.unixId(), 0));
  return {name:String(safe(() => process.name(), '')), pid:pid, bundleIdentifier:String(attr(process, 'AXBundleIdentifier', '')),
    providerBusName:'macos:' + pid, inputCapable:true};
}
function visibleProcesses(se) {
  return arrayValue(se.applicationProcesses.whose({backgroundOnly:false})()).filter(p => Number(safe(() => p.unixId(), 0)) > 0);
}
function resolveProcess(se, request) {
  const query = String(request.app || '').trim().toLowerCase();
  const identity = request.app_identity || {};
  const bus = String(identity.providerBusName || '');
  const pid = bus.startsWith('macos:') ? Number(bus.slice(6)) : 0;
  const exactPid = pid ? arrayValue(safe(() => se.applicationProcesses.whose({unixId:pid})(), [])) : [];
  if (exactPid.length === 1) return exactPid[0];
  const direct = safe(() => se.applicationProcesses.byName(String(request.app || '')), null);
  if (direct && Number(safe(() => direct.unixId(), 0)) > 0) return direct;
  const processes = visibleProcesses(se);
  const exact = processes.filter(p => String(safe(() => p.name(), '')).toLowerCase() === query);
  if (exact.length === 1) return exact[0];
  const loose = processes.filter(p => String(safe(() => p.name(), '')).toLowerCase().includes(query));
  if (loose.length === 1) return loose[0];
  throw new Error(exact.length + loose.length > 1 ? 'ambiguous application' : 'application not found');
}
function windowNativeId(window, index) {
  return String(attr(window, 'AXWindowNumber', index));
}
function windowsOf(process) {
  return arrayValue(safe(() => process.windows(), [])).map((w, index) => ({
    index:index, nativeId:windowNativeId(w,index), title:String(safe(() => w.name(), '')), role:roleName(attr(w,'AXRole','AXWindow')),
    active:Boolean(attr(w,'AXMain',false) || attr(w,'AXFocused',false)), modal:Boolean(attr(w,'AXModal',false)),
    showing:!Boolean(attr(w,'AXMinimized',false)), frame:frameOf(w)
  }));
}
function resolveWindow(process, request) {
  const windows = arrayValue(safe(() => process.windows(), []));
  if (!windows.length) throw new Error('application has no accessibility windows');
  const target = request.target || request.expected_window || {}, objectPath = String(target.providerObjectPath || '');
  const expectedName = String(target.providerWindowName || target.name || '');
  let candidates = windows.map((w,index) => ({w:w,index:index,id:windowNativeId(w,index),name:String(safe(() => w.name(),''))}));
  if (objectPath.startsWith('window:')) candidates = candidates.filter(c => c.id === objectPath.slice(7));
  if (expectedName) { const named = candidates.filter(c => c.name === expectedName); if (named.length) candidates = named; }
  const main = candidates.filter(c => Boolean(attr(c.w,'AXMain',false) || attr(c.w,'AXFocused',false)));
  if (main.length === 1) return main[0];
  if (candidates.length === 1) return candidates[0];
  return candidates[0];
}
function childrenOf(element) { return arrayValue(safe(() => element.uiElements(), [])); }
function resolvePath(process, path) {
  if (!Array.isArray(path) || !path.length) throw new Error('target runtime path is missing');
  let element = arrayValue(process.windows())[Number(path[0])];
  if (!element) throw new Error('target window no longer exists');
  for (let i=1; i<path.length; i++) { element = childrenOf(element)[Number(path[i])]; if (!element) throw new Error('target element no longer exists'); }
  return element;
}
function elementRecord(element, path, windowInfo) {
  const role = roleName(attr(element,'AXRole', safe(() => element.role(), '')));
  const name = String(safe(() => element.name(), '') || attr(element,'AXTitle','') || attr(element,'AXDescription',''));
  const valueRaw = safe(() => element.value(), '');
  const actions = actionNames(element);
  const states = [];
  if (Boolean(safe(() => element.enabled(), true))) states.push('enabled');
  if (Boolean(safe(() => element.focused(), false))) states.push('focused');
  if (Boolean(attr(element,'AXSelected',false))) states.push('selected');
  if (Boolean(attr(element,'AXExpanded',false))) states.push('expanded');
  if (Boolean(attr(element,'AXModal',false))) states.push('modal');
  return {runtimeId:path, name:name, controlType:role, localizedControlType:String(attr(element,'AXRoleDescription','')),
    value:valueRaw === null || valueRaw === undefined ? '' : String(valueRaw), frame:frameOf(element), actions:actions,
    states:states.join(' '), description:String(safe(() => element.description(),'')),
    placeholder:String(attr(element,'AXPlaceholderValue','')), showing:!Boolean(attr(element,'AXHidden',false)), visible:!Boolean(attr(element,'AXHidden',false)),
    providerObjectPath:'element:' + path.join('.'), providerIdentityScope:'window',
    providerWindowObjectPath:'window:' + windowInfo.nativeId, providerWindowRole:windowInfo.role,
    providerWindowName:windowInfo.title};
}
function tree(process, request) {
  const allWindows = windowsOf(process), selected = resolveWindow(process, request), selectedInfo = allWindows[selected.index];
  const options = request.observation || {}, maxNodes = Number(options.max_tree_nodes || 1200), maxDepth = Number(options.max_tree_depth || 64);
  const records = [], lines = [];
  function walk(element, path, depth) {
    if (records.length >= maxNodes || depth > maxDepth) return;
    const record = elementRecord(element,path,selectedInfo); record.index = records.length; records.push(record);
    const label = record.name ? ' ' + record.name.replace(/[\r\n]+/g,' ') : '';
    lines.push('  '.repeat(depth) + record.index + ' ' + record.controlType + label);
    childrenOf(element).forEach((child,index) => walk(child,path.concat([index]),depth+1));
  }
  walk(selected.w,[selected.index],0);
  return {app:processInfo(process), windows:allWindows, selected:selectedInfo, elements:records, treeLines:lines};
}
function performAction(process, request) {
  const target = request.target || {}, path = target.runtimeId || (request.element || {}).runtimeId;
  let element = path ? resolvePath(process,path) : resolveWindow(process,request).w;
  const intent = request.intent, input = request.input || {};
  const current = path ? elementRecord(element,path,windowsOf(process)[Number(path[0])]) : null;
  if (current && target.providerObjectPath && target.providerObjectPath !== current.providerObjectPath) throw new Error('target provider identity changed');
  if (current && target.role && String(target.role).toLowerCase() !== current.controlType) throw new Error('target role changed');
  if (current && target.hasName && String(target.name || '') !== current.name) throw new Error('target name changed');
  if (intent === 'resolve_target') return current;
  if (intent === 'click') {
    const press = safe(() => element.actions.byName('AXPress'), null);
    if (press && safe(() => { press.perform(); return true; }, false)) return {route:'AXPress'};
    const frame = frameOf(element); if (!frame) throw new Error('target has no actionable frame');
    return {route:'coordinate', point:[frame.x+frame.width/2,frame.y+frame.height/2]};
  }
  if (intent === 'set_value') { element.value = String(input.value); return {route:'AXValue'}; }
  if (intent === 'type') { element.focused = true; return {route:'keyboard', text:String(input.text)}; }
  if (intent === 'keypress') { element.focused = true; return {route:'keyboard', key:String(input.key)}; }
  if (intent === 'scroll') { const frame=frameOf(element); return {route:'scroll', point:frame ? [frame.x+frame.width/2,frame.y+frame.height/2] : null}; }
  throw new Error('unsupported accessibility action');
}
function run(argv) {
  const request = JSON.parse(argv[0]), se = Application('System Events');
  if (request.op === 'apps') return JSON.stringify(visibleProcesses(se).map(processInfo));
  const process = resolveProcess(se, request.payload || {});
  if (request.op === 'windows') return JSON.stringify({app:processInfo(process), windows:windowsOf(process)});
  if (request.op === 'tree') return JSON.stringify(tree(process,request.payload || {}));
  if (request.op === 'act') return JSON.stringify(performAction(process,request.payload || {}));
  if (request.op === 'window_action') {
    const selected=resolveWindow(process,request.payload || {});
    const before=windowsOf(process)[selected.index];
    if (request.action === 'activate') { process.frontmost=true; safe(() => selected.w.actions.byName('AXRaise').perform(), null); }
    else if (request.action === 'close') { const action=selected.w.actions.byName('AXClose'); action.perform(); }
    else throw new Error('unsupported window action');
    return JSON.stringify({app:processInfo(process), window:before});
  }
  throw new Error('unsupported operation');
}
"""


ROLE_CLICK_ACTIONS = {"AXPress", "AXConfirm", "AXPick"}


class MacOSBackend:
    def __init__(self) -> None:
        self._cg = None
        self._last_frame_id = ""
        self._image_origin = (0.0, 0.0)
        self._image_scale = (1.0, 1.0)
        self._window_handles: dict[str, str] = {}

    @staticmethod
    def _accessibility_trusted() -> bool:
        framework = ctypes.CDLL("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices")
        framework.AXIsProcessTrusted.restype = ctypes.c_bool
        return bool(framework.AXIsProcessTrusted())

    def _jxa(self, op: str, payload: dict[str, Any] | None = None, **extra: Any) -> Any:
        if op != "apps" and not self._accessibility_trusted():
            raise NativeProviderError(
                "macOS Accessibility permission is required",
                code="permission_denied",
                phase="permission",
            )
        payload = dict(payload or {})
        handle = str(payload.get("window_handle") or "")
        if handle in self._window_handles and not payload.get("target") and not payload.get("expected_window"):
            payload["target"] = {"providerObjectPath": self._window_handles[handle]}
        identity = payload.get("app_identity") or {}
        bus = str(identity.get("providerBusName") or "")
        expected_generation = str(identity.get("processGeneration") or "")
        if bus.startswith("macos:") and expected_generation:
            try:
                pid = int(bus[6:])
            except ValueError:
                pid = 0
            if pid <= 0 or self._generation(pid) != expected_generation:
                raise NativeProviderError("application identity is stale", code="stale_app_ref", phase="precondition")
        request = {"op": op, "payload": payload}
        request.update(extra)
        command = ["/usr/bin/osascript", "-l", "JavaScript", "-e", JXA_SOURCE, "--", json.dumps(request, separators=(",", ":"))]
        completed = subprocess.run(command, capture_output=True, text=True, timeout=25, check=False)
        if completed.returncode != 0:
            diagnostic = (completed.stderr or completed.stdout).strip()
            permission = "not authorized" in diagnostic.lower() or "-1743" in diagnostic
            raise NativeProviderError(
                "macOS Accessibility permission is required" if permission else "macOS Accessibility operation failed",
                code="permission_denied" if permission else "native_accessibility_failed",
                phase="permission" if permission else "native_api",
            )
        try:
            return json.loads(completed.stdout)
        except json.JSONDecodeError as error:
            raise NativeProviderError("macOS Accessibility returned invalid data", code="native_protocol_failed") from error

    @staticmethod
    def _generation(pid: int) -> str:
        completed = subprocess.run(["/bin/ps", "-p", str(pid), "-o", "lstart="], capture_output=True, text=True, check=False)
        stamp = completed.stdout.strip() or str(pid)
        return hashlib.sha256((str(pid) + "\0" + stamp).encode()).hexdigest()[:24]

    @staticmethod
    def _window_handle(pid: int, native_id: str) -> str:
        return "w2:" + hashlib.md5(f"macos\0{pid}\0{native_id}".encode(), usedforsecurity=False).hexdigest()

    def _decorate_app(self, app: dict[str, Any]) -> dict[str, Any]:
        app["processGeneration"] = self._generation(int(app.get("pid", 0)))
        return app

    def observe_apps(self) -> list[dict[str, Any]]:
        completed = subprocess.run(
            ["/usr/bin/lsappinfo", "list"],
            capture_output=True,
            text=True,
            timeout=5,
            check=False,
        )
        if completed.returncode != 0:
            raise NativeProviderError("macOS application inventory failed", code="native_inventory_failed")
        blocks = re.split(r"(?m)(?=^\s*\d+\) )", completed.stdout)
        apps: list[dict[str, Any]] = []
        for block in blocks:
            header = re.search(r'^\s*\d+\) "([^"]+)"', block)
            pid_match = re.search(r"\bpid\s*=\s*(\d+)(?![^\n]*\(terminated\))", block)
            type_match = re.search(r'\btype="([^"]+)"', block)
            if not header or not pid_match or not type_match or type_match.group(1) != "Foreground":
                continue
            pid = int(pid_match.group(1))
            bundle_match = re.search(r'bundleID="([^"]+)"', block)
            path_match = re.search(r'bundle path="([^"]+)"', block)
            apps.append(self._decorate_app({
                "name": header.group(1),
                "bundleIdentifier": bundle_match.group(1) if bundle_match else "",
                "displayText": path_match.group(1) if path_match else "",
                "pid": pid,
                "providerBusName": f"macos:{pid}",
                "inputCapable": True,
            }))
        return apps

    def _decorate_tree(self, raw: dict[str, Any]) -> dict[str, Any]:
        app = self._decorate_app(raw["app"])
        pid, generation = int(app["pid"]), app["processGeneration"]
        windows = []
        for item in raw.get("windows", []):
            handle = self._window_handle(pid, str(item["nativeId"]))
            self._window_handles[handle] = "window:" + str(item["nativeId"])
            windows.append({
                "title": item.get("title", ""), "role": item.get("role", "window"), "handle": handle,
                "providerBusName": app["providerBusName"], "providerObjectPath": "window:" + str(item["nativeId"]),
                "providerProcessGeneration": generation, "selected": item.get("nativeId") == raw["selected"].get("nativeId"),
                "active": bool(item.get("active")), "modal": bool(item.get("modal")), "showing": bool(item.get("showing", True)),
            })
        selected = raw["selected"]
        handle = self._window_handle(pid, str(selected["nativeId"]))
        for record in raw.get("elements", []):
            record["providerBusName"] = app["providerBusName"]
            record["processGeneration"] = generation
            record["providerWindowBusName"] = app["providerBusName"]
            record["providerWindowProcessGeneration"] = generation
            record["nativeWindowHandle"] = int(selected["nativeId"]) if str(selected["nativeId"]).isdigit() else 0
            if record.get("frame") and selected.get("frame"):
                record["frame"]["x"] -= selected["frame"]["x"]
                record["frame"]["y"] -= selected["frame"]["y"]
        sampled = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        identity = json.dumps([(r.get("runtimeId"), r.get("controlType"), r.get("name"), r.get("value")) for r in raw.get("elements", [])], separators=(",", ":"))
        snapshot = {
            "app": app, "windowTitle": selected.get("title", ""), "windowHandle": handle,
            "windows": windows, "snapshotId": "macos-" + hashlib.sha256(identity.encode()).hexdigest()[:24],
            "sampledAt": sampled, "windowBounds": selected.get("frame"), "treeLines": raw.get("treeLines", []),
            "elements": raw.get("elements", []),
        }
        return snapshot

    def observe(self, payload: dict[str, Any]) -> dict[str, Any]:
        raw = self._jxa("tree", payload)
        snapshot = self._decorate_tree(raw)
        observation = payload.get("observation") or {}
        if observation.get("include_screenshot"):
            region = snapshot.get("windowBounds") if observation.get("screenshot_scope") == "window" else None
            snapshot.update(self._capture(region))
        return {"ok": True, "snapshot": snapshot}

    def _cg_api(self):
        if self._cg is None:
            self._cg = ctypes.CDLL("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices")
            self._cg.CGEventCreateMouseEvent.restype = ctypes.c_void_p
            self._cg.CGEventCreateMouseEvent.argtypes = [ctypes.c_void_p, ctypes.c_uint32, _CGPoint, ctypes.c_uint32]
            self._cg.CGEventCreateKeyboardEvent.restype = ctypes.c_void_p
            self._cg.CGEventCreateKeyboardEvent.argtypes = [ctypes.c_void_p, ctypes.c_uint16, ctypes.c_bool]
            self._cg.CGEventCreateScrollWheelEvent.restype = ctypes.c_void_p
            self._cg.CGEventPost.argtypes = [ctypes.c_uint32, ctypes.c_void_p]
            self._cg.CGEventKeyboardSetUnicodeString.argtypes = [ctypes.c_void_p, ctypes.c_long, ctypes.POINTER(ctypes.c_uint16)]
            self._cg.CFRelease.argtypes = [ctypes.c_void_p]
        return self._cg

    def _post_mouse(self, event_type: int, x: float, y: float, button: int = 0) -> None:
        cg = self._cg_api()
        event = cg.CGEventCreateMouseEvent(None, event_type, _CGPoint(x, y), button)
        if not event:
            raise NativeProviderError("CoreGraphics could not create mouse event", code="input_failed", phase="emit")
        cg.CGEventPost(0, event)
        cg.CFRelease(event)

    def _image_point(self, x: float, y: float) -> tuple[float, float]:
        return (
            self._image_origin[0] + x * self._image_scale[0],
            self._image_origin[1] + y * self._image_scale[1],
        )

    def _click(self, x: float, y: float, button: str = "left", count: int = 1, *, image_coordinates: bool = False) -> None:
        if image_coordinates:
            x, y = self._image_point(x, y)
        mapping = {"left": (1, 2, 0), "right": (3, 4, 1), "wheel": (25, 26, 2), "back": (25, 26, 3), "forward": (25, 26, 4)}
        down, up, native = mapping.get(button, mapping["left"])
        self._post_mouse(5, x, y, native)
        for _ in range(max(1, count)):
            self._post_mouse(down, x, y, native)
            self._post_mouse(up, x, y, native)

    def _scroll(self, dx: float, dy: float) -> None:
        cg = self._cg_api()
        event = cg.CGEventCreateScrollWheelEvent(None, 0, 2, int(-dy), int(-dx))
        if not event:
            raise NativeProviderError("CoreGraphics could not create scroll event", code="input_failed", phase="emit")
        cg.CGEventPost(0, event)
        cg.CFRelease(event)

    def _key_event(self, keycode: int, down: bool, text: str | None = None) -> None:
        cg = self._cg_api()
        event = cg.CGEventCreateKeyboardEvent(None, keycode, bool(down))
        if not event:
            raise NativeProviderError("CoreGraphics could not create keyboard event", code="input_failed", phase="emit")
        if text is not None:
            units = text.encode("utf-16-le")
            values = (ctypes.c_uint16 * (len(units) // 2)).from_buffer_copy(units)
            cg.CGEventKeyboardSetUnicodeString(event, len(values), values)
        cg.CGEventPost(0, event)
        cg.CFRelease(event)

    def _type(self, text: str) -> None:
        for char in text:
            self._key_event(0, True, char)
            self._key_event(0, False, char)

    def _keypress(self, keys: list[str] | str) -> None:
        if isinstance(keys, str):
            keys = [part for part in keys.replace("-", "+").split("+") if part]
        names = [str(key).strip().upper() for key in keys]
        modifiers = {"CMD": 55, "COMMAND": 55, "META": 55, "SHIFT": 56, "ALT": 58, "OPTION": 58, "CTRL": 59, "CONTROL": 59}
        keycodes = {"ENTER": 36, "RETURN": 36, "TAB": 48, "SPACE": 49, "BACKSPACE": 51, "DELETE": 51,
                    "ESC": 53, "ESCAPE": 53, "LEFT": 123, "RIGHT": 124, "DOWN": 125, "UP": 126,
                    "HOME": 115, "END": 119, "PAGEUP": 116, "PAGEDOWN": 121}
        letters = {chr(ord("A") + index): code for index, code in enumerate([0,11,8,2,14,3,5,4,34,38,40,37,46,45,31,35,12,15,1,17,32,9,13,7,16,6])}
        held = [modifiers[name] for name in names if name in modifiers]
        primary = next((keycodes.get(name, letters.get(name)) for name in reversed(names) if name not in modifiers), None)
        if primary is None:
            raise NativeProviderError("unsupported macOS keypress", code="input_failed", phase="emit")
        for code in held:
            self._key_event(code, True)
        self._key_event(primary, True)
        self._key_event(primary, False)
        for code in reversed(held):
            self._key_event(code, False)

    @staticmethod
    def _png_size(data: bytes) -> tuple[int, int]:
        if data[:8] != b"\x89PNG\r\n\x1a\n":
            raise NativeProviderError("screencapture did not return PNG", code="capture_failed")
        return struct.unpack(">II", data[16:24])

    def _desktop_bounds(self) -> tuple[float, float, float, float]:
        cg = self._cg_api()
        cg.CGGetActiveDisplayList.argtypes = [ctypes.c_uint32, ctypes.POINTER(ctypes.c_uint32), ctypes.POINTER(ctypes.c_uint32)]
        cg.CGGetActiveDisplayList.restype = ctypes.c_int32
        cg.CGDisplayBounds.argtypes = [ctypes.c_uint32]
        cg.CGDisplayBounds.restype = _CGRect
        count = ctypes.c_uint32()
        if cg.CGGetActiveDisplayList(0, None, ctypes.byref(count)) != 0 or count.value == 0:
            return (0.0, 0.0, 1.0, 1.0)
        displays = (ctypes.c_uint32 * count.value)()
        if cg.CGGetActiveDisplayList(count.value, displays, ctypes.byref(count)) != 0:
            return (0.0, 0.0, 1.0, 1.0)
        rects = [cg.CGDisplayBounds(displays[index]) for index in range(count.value)]
        left = min(rect.origin.x for rect in rects)
        top = min(rect.origin.y for rect in rects)
        right = max(rect.origin.x + rect.size.width for rect in rects)
        bottom = max(rect.origin.y + rect.size.height for rect in rects)
        return (left, top, right - left, bottom - top)

    def _capture(self, region: dict[str, Any] | None = None) -> dict[str, Any]:
        with tempfile.TemporaryDirectory(prefix="computer-use-macos-") as directory:
            path = Path(directory) / "screen.png"
            command = ["/usr/sbin/screencapture", "-x", "-t", "png"]
            if region and region.get("width", 0) > 0 and region.get("height", 0) > 0:
                command.extend(["-R", f"{int(region['x'])},{int(region['y'])},{int(region['width'])},{int(region['height'])}"])
            command.append(str(path))
            completed = subprocess.run(command, capture_output=True, timeout=20, check=False)
            if completed.returncode != 0 or not path.exists():
                raise NativeProviderError("macOS Screen Recording permission is required", code="permission_denied", phase="permission")
            data = path.read_bytes()
            native_width, native_height = self._png_size(data)
            resize = min(1.0, 1920 / native_width, 1080 / native_height)
            width = max(1, round(native_width * resize))
            height = max(1, round(native_height * resize))
            if (width, height) != (native_width, native_height):
                resized = subprocess.run(
                    ["/usr/bin/sips", "-z", str(height), str(width), str(path)],
                    capture_output=True,
                    timeout=20,
                    check=False,
                )
                if resized.returncode != 0:
                    raise NativeProviderError("macOS screenshot scaling failed", code="capture_failed")
                data = path.read_bytes()
                width, height = self._png_size(data)
        if region:
            logical = (float(region["x"]), float(region["y"]), float(region["width"]), float(region["height"]))
        else:
            logical = self._desktop_bounds()
        self._image_origin = logical[:2]
        self._image_scale = (logical[2] / width, logical[3] / height)
        frame_id = "layout-macos-" + hashlib.sha256(
            f"{logical[0]}:{logical[1]}:{logical[2]}x{logical[3]}:{width}x{height}".encode()
        ).hexdigest()[:16]
        self._last_frame_id = frame_id
        return {
            "screenshotPngBase64": base64.b64encode(data).decode(), "screenshotMimeType": "image/png",
            "screenshotEncoding": "png", "coordinateSpace": "screen_image_px", "frameId": frame_id,
            "width": width, "height": height, "sampledAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "desktopLayout": {"originX": logical[0], "originY": logical[1], "width": logical[2], "height": logical[3]},
            "app": {"name": "", "pid": 0, "inputCapable": False},
        }

    def resolve_and_act(self, payload: dict[str, Any]) -> dict[str, Any]:
        result = self._jxa("act", payload)
        if payload["intent"] == "resolve_target":
            raw = self._jxa("tree", payload)
            snapshot = self._decorate_tree(raw)
            runtime_id = (payload.get("target") or payload.get("element") or {}).get("runtimeId")
            element = next((item for item in snapshot["elements"] if item.get("runtimeId") == runtime_id), None)
            return {"ok": True, "resolved_element": element}
        emitted = False
        try:
            if result.get("route") == "coordinate":
                self._click(*result["point"])
                emitted = True
            elif result.get("route") == "keyboard":
                if "text" in result:
                    self._type(result["text"])
                else:
                    self._keypress(result["key"])
                emitted = True
            elif result.get("route") == "scroll":
                direction = payload["input"]["direction"]
                amount = float(payload["input"].get("pages", 1)) * 600
                dx = amount if direction == "right" else -amount if direction == "left" else 0
                dy = amount if direction == "down" else -amount if direction == "up" else 0
                self._scroll(dx, dy)
                emitted = True
            else:
                emitted = True  # AXPress/AXValue was already issued in JXA.
        except NativeProviderError as error:
            error.emitted = emitted
            raise
        observation = payload.get("observation") or {}
        if observation.get("capture_snapshot") is False:
            return {"ok": True, "observation_skipped": True, "mutation": {"input_events_emitted": emitted, "desktop_state_mutated": True},
                    "action_evidence": {"route": result.get("route", "native_ax"), "input_emitted": emitted, "readback_sampled": False}}
        try:
            raw = self._jxa("tree", payload)
        except NativeProviderError as error:
            error.emitted = emitted
            raise
        return {"ok": True, "snapshot": self._decorate_tree(raw), "mutation": {"input_events_emitted": emitted, "desktop_state_mutated": True},
                "action_evidence": {"route": result.get("route", "native_ax"), "input_emitted": emitted, "readback_sampled": payload["intent"] == "set_value"}}

    def desktop_execute_batch(self, payload: dict[str, Any]) -> dict[str, Any]:
        actions = payload["actions"]
        before = self._capture()
        requested_frame = payload.get("frame_id")
        if requested_frame and requested_frame != before["frameId"]:
            return {"ok": False, "error": "stale GUI layout; take a fresh screenshot", "snapshot": before,
                    "failure": {"code": "stale_layout", "phase": "precondition", "facts_complete": True}}
        input_count = 0
        waits = 0
        try:
            for action in actions:
                kind = action["type"]
                if kind == "screenshot":
                    continue
                if kind == "click":
                    self._click(action["x"], action["y"], action.get("button", "left"), image_coordinates=True); input_count += 1
                elif kind == "double_click":
                    self._click(action["x"], action["y"], "left", 2, image_coordinates=True); input_count += 1
                elif kind == "move":
                    point = self._image_point(action["x"], action["y"]); self._post_mouse(5, *point); input_count += 1
                elif kind == "drag":
                    path = action["path"]; start = self._image_point(path[0]["x"], path[0]["y"]); self._post_mouse(5, *start); self._post_mouse(1, *start)
                    for point in path[1:]: self._post_mouse(6, *self._image_point(point["x"], point["y"]))
                    self._post_mouse(2, *self._image_point(path[-1]["x"], path[-1]["y"])); input_count += 1
                elif kind == "scroll":
                    self._post_mouse(5, *self._image_point(action["x"], action["y"])); self._scroll(action.get("scroll_x", 0), action.get("scroll_y", 0)); input_count += 1
                elif kind == "type":
                    self._type(action["text"]); input_count += 1
                elif kind == "keypress":
                    self._keypress(action["keys"]); input_count += 1
                elif kind == "wait":
                    time.sleep(1); waits += 1
        except NativeProviderError as error:
            error.emitted = error.emitted or input_count > 0
            raise
        if payload.get("settle_ms", 0):
            time.sleep(payload["settle_ms"] / 1000)
        try:
            after = self._capture()
        except NativeProviderError as error:
            error.emitted = input_count > 0
            raise
        changed = png_content_digest(before["screenshotPngBase64"]) != png_content_digest(after["screenshotPngBase64"])
        return {"ok": True, "snapshot": after, "mutation": {"input_events_emitted": input_count > 0, "desktop_state_mutated": input_count > 0},
                "gui_result": {"dispatched": len(actions), "input_events_emitted": input_count > 0, "input_actions_dispatched": input_count,
                               "effect_unknown": input_count > 0, "quiescent": True, "visual_change_observed": changed,
                               "wait_actions_requested": waits, "gui_lease_state": "valid", "gui_lease_revalidated": bool(requested_frame)}}

    def window_list(self, payload: dict[str, Any]) -> dict[str, Any]:
        raw = self._jxa("windows", payload)
        app = self._decorate_app(raw["app"])
        return {"ok": True, "app_name": app["name"], "app_pid": app["pid"], "window_titles": [w.get("title", "") for w in raw["windows"]]}

    def window_action(self, payload: dict[str, Any]) -> dict[str, Any]:
        result = self._jxa("window_action", payload, action=payload["action"])
        emitted = True
        if payload["action"] == "close":
            try:
                after = self._jxa("windows", payload)
                native_id = str(result["window"]["nativeId"])
                if any(str(window.get("nativeId")) == native_id for window in after.get("windows", [])):
                    raise NativeProviderError("closed window is still present", code="readback_failed", phase="readback", emitted=True)
                app = self._decorate_app(after["app"])
            except NativeProviderError as error:
                error.emitted = True
                raise
            snapshot = {
                "app": app,
                "windowAbsent": True,
                "closedWindowHandle": payload.get("window_handle", ""),
                "snapshotId": "macos-close-" + hashlib.sha256(f"{app['pid']}:{native_id}:{time.time_ns()}".encode()).hexdigest()[:24],
                "sampledAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            }
            return {"ok": True, "snapshot": snapshot, "mutation": {"input_events_emitted": emitted, "desktop_state_mutated": True},
                    "action_evidence": {"route": "AXClose", "input_emitted": True, "readback_sampled": True, "readback_verified": True}}
        try:
            raw = self._jxa("tree", payload)
        except NativeProviderError as error:
            error.emitted = True
            raise
        return {"ok": True, "snapshot": self._decorate_tree(raw), "mutation": {"input_events_emitted": emitted, "desktop_state_mutated": True},
                "action_evidence": {"route": "AXRaise", "input_emitted": True, "readback_sampled": True, "readback_verified": True}}


class _CGPoint(ctypes.Structure):
    _fields_ = [("x", ctypes.c_double), ("y", ctypes.c_double)]


class _CGSize(ctypes.Structure):
    _fields_ = [("width", ctypes.c_double), ("height", ctypes.c_double)]


class _CGRect(ctypes.Structure):
    _fields_ = [("origin", _CGPoint), ("size", _CGSize)]
