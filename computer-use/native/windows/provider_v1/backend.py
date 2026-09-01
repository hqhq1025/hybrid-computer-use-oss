"""Windows provider backed by UI Automation, Win32 input, and GDI capture."""

from __future__ import annotations

import base64
import ctypes
from ctypes import wintypes
import hashlib
import json
import os
import subprocess
import time
from typing import Any

from native.common.provider_v1.wire_v2 import NativeProviderError
from native.common.provider_v1.images import png_content_digest


POWERSHELL_SOURCE = r"""
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
$request = $env:COMPUTER_USE_NATIVE_REQUEST | ConvertFrom-Json
function Safe([scriptblock]$Block, $Fallback) { try { & $Block } catch { $Fallback } }
function Get-Generation($Process) { try { [string]$Process.StartTime.ToUniversalTime().Ticks } catch { [string]$Process.Id } }
function App-Record($Process) {
  [ordered]@{name=[string]$Process.ProcessName; displayText=[string](Safe {$Process.MainWindowTitle} ''); pid=[int]$Process.Id;
    bundleIdentifier=[string](Safe {$Process.Path} ''); providerBusName=('windows:' + $Process.Id); processGeneration=(Get-Generation $Process); inputCapable=$true}
}
function Visible-Processes {
  @(Get-Process | Where-Object {$_.MainWindowHandle -ne 0} | Sort-Object Id)
}
function Resolve-Process($Payload) {
  $bus = [string](Safe {$Payload.app_identity.providerBusName} '')
  if ($bus.StartsWith('windows:')) { $pidValue=0; if ([int]::TryParse($bus.Substring(8), [ref]$pidValue)) {
    $process=Get-Process -Id $pidValue; $expected=[string](Safe {$Payload.app_identity.processGeneration} '')
    if($expected -and (Get-Generation $process) -ne $expected){throw 'application identity is stale'}; return $process
  } }
  $query = ([string]$Payload.app).Trim().ToLowerInvariant()
  $all = @(Visible-Processes)
  $exact = @($all | Where-Object {$_.ProcessName.ToLowerInvariant() -eq $query -or $_.MainWindowTitle.ToLowerInvariant() -eq $query})
  if ($exact.Count -eq 1) { return $exact[0] }
  $loose = @($all | Where-Object {$_.ProcessName.ToLowerInvariant().Contains($query) -or $_.MainWindowTitle.ToLowerInvariant().Contains($query)})
  if ($loose.Count -eq 1) { return $loose[0] }
  if ($exact.Count + $loose.Count -gt 1) { throw 'ambiguous application' }
  throw 'application not found'
}
function Role-Name($Element) {
  $name = [string](Safe {$Element.Current.ControlType.ProgrammaticName} 'ControlType.Unknown')
  $name = $name.Replace('ControlType.','')
  switch ($name) { 'Edit' {'entry'} 'Text' {'label'} 'CheckBox' {'check box'} 'RadioButton' {'radio button'}
    'ComboBox' {'combo box'} 'ListItem' {'list item'} 'MenuItem' {'menu item'} 'TabItem' {'page tab'}
    'Tab' {'page tab list'} 'DataGrid' {'table'} 'DataItem' {'table cell'} 'ScrollBar' {'scroll bar'}
    'ToolBar' {'tool bar'} default {$name.ToLowerInvariant()} }
}
function Runtime-Id($Element) { @($Element.GetRuntimeId() | ForEach-Object {[int]$_}) }
function Bounds($Element) {
  $rect = Safe {$Element.Current.BoundingRectangle} $null
  if ($null -eq $rect -or $rect.IsEmpty) { return $null }
  [ordered]@{x=[double]$rect.X; y=[double]$rect.Y; width=[double]$rect.Width; height=[double]$rect.Height}
}
function Window-Elements($Process) {
  $condition = New-Object System.Windows.Automation.PropertyCondition([System.Windows.Automation.AutomationElement]::ProcessIdProperty, [int]$Process.Id)
  $items = [System.Windows.Automation.AutomationElement]::RootElement.FindAll([System.Windows.Automation.TreeScope]::Children, $condition)
  $result = @()
  for ($i=0; $i -lt $items.Count; $i++) {
    $w=$items.Item($i); $handle=[int64](Safe {$w.Current.NativeWindowHandle} 0)
    $windowPattern=$null; $hasPattern=$w.TryGetCurrentPattern([System.Windows.Automation.WindowPattern]::Pattern,[ref]$windowPattern)
    $result += [ordered]@{index=$i; nativeId=[string]$handle; title=[string](Safe {$w.Current.Name} ''); role=(Role-Name $w);
    active=[bool](Safe {$w.Current.HasKeyboardFocus} $false); modal=[bool]$(if($hasPattern){Safe {$windowPattern.Current.IsModal} $false}else{$false});
      showing=[bool](-not (Safe {$w.Current.IsOffscreen} $true)); frame=(Bounds $w); element=$w}
  }
  @($result)
}
function Resolve-Window($Process,$Payload) {
  $windows=@(Window-Elements $Process); if ($windows.Count -eq 0) { throw 'application has no UI Automation windows' }
  $objectPath=[string](Safe {$Payload.target.providerObjectPath} '')
  if ($objectPath.StartsWith('window:')) { $native=$objectPath.Substring(7); $matched=@($windows | Where-Object {$_.nativeId -eq $native}); if($matched.Count -eq 1){return $matched[0]} }
  $name=[string](Safe {$Payload.target.providerWindowName} '')
  if (-not $name) { $name=[string](Safe {$Payload.target.name} '') }
  if ($name) { $matched=@($windows | Where-Object {$_.title -eq $name}); if($matched.Count -eq 1){return $matched[0]} }
  $focused=@($windows | Where-Object {$_.active}); if($focused.Count -eq 1){return $focused[0]}
  $windows[0]
}
function Pattern-Actions($Element) {
  $actions=@(); $pattern=$null
  if($Element.TryGetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern,[ref]$pattern)){$actions+='invoke'}
  if($Element.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern,[ref]$pattern)){$actions+='setvalue'}
  if($Element.TryGetCurrentPattern([System.Windows.Automation.TogglePattern]::Pattern,[ref]$pattern)){$actions+='toggle'}
  if($Element.TryGetCurrentPattern([System.Windows.Automation.SelectionItemPattern]::Pattern,[ref]$pattern)){$actions+='select'}
  if($Element.TryGetCurrentPattern([System.Windows.Automation.ExpandCollapsePattern]::Pattern,[ref]$pattern)){$actions+='expandcollapse'}
  if($Element.TryGetCurrentPattern([System.Windows.Automation.ScrollItemPattern]::Pattern,[ref]$pattern)){$actions+='scrollintoview'}
  @($actions)
}
function Element-Record($Element,$Index,$Window,$Process) {
  $states=@(); if(Safe {$Element.Current.IsEnabled} $false){$states+='enabled'}; if(Safe {$Element.Current.HasKeyboardFocus} $false){$states+='focused'}
  if(-not (Safe {$Element.Current.IsOffscreen} $true)){$states+='showing'}
  $value=''; $valuePattern=$null; if($Element.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern,[ref]$valuePattern)){$value=[string](Safe {$valuePattern.Current.Value} '')}
  $rid=@(Runtime-Id $Element); $windowRid=@(Runtime-Id $Window.element)
  [ordered]@{index=$Index; runtimeId=$rid; automationId=[string](Safe {$Element.Current.AutomationId} '');
    providerBusName=('windows:'+$Process.Id); providerObjectPath=('element:'+($rid -join '.')); processGeneration=(Get-Generation $Process);
    providerIdentityScope='window'; providerWindowBusName=('windows:'+$Process.Id); providerWindowObjectPath=('window:'+$Window.nativeId);
    providerWindowProcessGeneration=(Get-Generation $Process); providerWindowRole=$Window.role; providerWindowName=$Window.title;
    name=[string](Safe {$Element.Current.Name} ''); controlType=(Role-Name $Element); localizedControlType=[string](Safe {$Element.Current.LocalizedControlType} '');
    className=[string](Safe {$Element.Current.ClassName} ''); value=$value; nativeWindowHandle=[int64](Safe {$Element.Current.NativeWindowHandle} 0);
    frame=(Bounds $Element); actions=@(Pattern-Actions $Element); states=($states -join ' '); description=[string](Safe {$Element.Current.HelpText} '');
    showing=[bool](-not (Safe {$Element.Current.IsOffscreen} $true)); visible=[bool](-not (Safe {$Element.Current.IsOffscreen} $true))}
}
function Tree($Process,$Payload) {
  $window=Resolve-Window $Process $Payload; $maxNodes=[int](Safe {$Payload.observation.max_tree_nodes} 1200); if($maxNodes -le 0){$maxNodes=1200}
  $maxDepth=[int](Safe {$Payload.observation.max_tree_depth} 64); if($maxDepth -le 0){$maxDepth=64}
  $records=New-Object System.Collections.ArrayList; $lines=New-Object System.Collections.ArrayList
  $walker=[System.Windows.Automation.TreeWalker]::RawViewWalker
  function Walk($Element,[int]$Depth) {
    if($records.Count -ge $maxNodes -or $Depth -gt $maxDepth){return}
    $record=Element-Record $Element $records.Count $window $Process; [void]$records.Add($record)
    [void]$lines.Add((('  '*$Depth)+$record.index+' '+$record.controlType+$(if($record.name){' '+$record.name}else{''})))
    $child=Safe {$walker.GetFirstChild($Element)} $null
    while($null -ne $child -and $records.Count -lt $maxNodes){Walk $child ($Depth+1); $child=Safe {$walker.GetNextSibling($child)} $null}
  }
  Walk $window.element 0
  $publicWindows=@(Window-Elements $Process | ForEach-Object {[ordered]@{index=$_.index;nativeId=$_.nativeId;title=$_.title;role=$_.role;active=$_.active;modal=$_.modal;showing=$_.showing;frame=$_.frame}})
  [ordered]@{app=(App-Record $Process);windows=$publicWindows;selected=[ordered]@{index=$window.index;nativeId=$window.nativeId;title=$window.title;role=$window.role;active=$window.active;modal=$window.modal;showing=$window.showing;frame=$window.frame};elements=@($records);treeLines=@($lines)}
}
function Find-Element($Process,$Payload) {
  $target=if($null -ne $Payload.target){$Payload.target}else{$Payload.element}; $wanted=@($target.runtimeId)
  if($wanted.Count -eq 0){throw 'target runtime id is missing'}
  $window=Resolve-Window $Process $Payload; $walker=[System.Windows.Automation.TreeWalker]::RawViewWalker; $found=$null
  function Search($Element) {
    if($null -ne $script:found){return}; $rid=@(Runtime-Id $Element)
    if(($rid -join '.') -eq ($wanted -join '.')){$script:found=$Element;return}
    $child=Safe {$walker.GetFirstChild($Element)} $null; while($null -ne $child -and $null -eq $script:found){Search $child;$child=Safe {$walker.GetNextSibling($child)} $null}
  }
  Search $window.element; if($null -eq $script:found){throw 'target element no longer exists'}; [ordered]@{element=$script:found;window=$window}
}
function Act($Process,$Payload) {
  $intent=[string]$Payload.intent; $hasTarget=($null -ne $Payload.target -or $null -ne $Payload.element)
  if(-not $hasTarget -and $intent -in @('type','keypress','scroll')){$window=Resolve-Window $Process $Payload;$resolved=[ordered]@{element=$window.element;window=$window}}
  else{$resolved=Find-Element $Process $Payload}
  $element=$resolved.element
  if($intent -eq 'resolve_target'){return Element-Record $element 0 $resolved.window $Process}
  if($intent -eq 'click'){
    $pattern=$null; if($element.TryGetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern,[ref]$pattern)){$pattern.Invoke();return [ordered]@{route='UIA.Invoke'}}
    if($element.TryGetCurrentPattern([System.Windows.Automation.SelectionItemPattern]::Pattern,[ref]$pattern)){$pattern.Select();return [ordered]@{route='UIA.Select'}}
    if($element.TryGetCurrentPattern([System.Windows.Automation.TogglePattern]::Pattern,[ref]$pattern)){$pattern.Toggle();return [ordered]@{route='UIA.Toggle'}}
    $frame=Bounds $element;if($null -eq $frame){throw 'target has no actionable bounds'};return [ordered]@{route='coordinate';point=@($frame.x+$frame.width/2,$frame.y+$frame.height/2)}
  }
  if($intent -eq 'set_value'){$pattern=$null;if(-not $element.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern,[ref]$pattern)){throw 'target does not support ValuePattern'};$pattern.SetValue([string]$Payload.input.value);return [ordered]@{route='UIA.Value'}}
  if($intent -eq 'type'){$element.SetFocus();return [ordered]@{route='keyboard';text=[string]$Payload.input.text}}
  if($intent -eq 'keypress'){$element.SetFocus();return [ordered]@{route='keyboard';key=[string]$Payload.input.key}}
  if($intent -eq 'scroll'){$element.SetFocus();$frame=Bounds $element;return [ordered]@{route='scroll';point=$(if($frame){@($frame.x+$frame.width/2,$frame.y+$frame.height/2)}else{$null})}}
  throw 'unsupported accessibility action'
}
switch ([string]$request.op) {
  'apps' {$result=[ordered]@{items=@(Visible-Processes | ForEach-Object {App-Record $_})}}
  'windows' {$process=Resolve-Process $request.payload;$result=[ordered]@{app=(App-Record $process);windows=@(Window-Elements $process | ForEach-Object {[ordered]@{index=$_.index;nativeId=$_.nativeId;title=$_.title;role=$_.role;active=$_.active;modal=$_.modal;showing=$_.showing;frame=$_.frame}})}}
  'tree' {$process=Resolve-Process $request.payload;$result=Tree $process $request.payload}
  'act' {$process=Resolve-Process $request.payload;$result=Act $process $request.payload}
  'window_action' {$process=Resolve-Process $request.payload;$window=Resolve-Window $process $request.payload;$pattern=$null
    if(-not $window.element.TryGetCurrentPattern([System.Windows.Automation.WindowPattern]::Pattern,[ref]$pattern)){throw 'window does not support WindowPattern'}
    if($request.action -eq 'close'){$pattern.Close()}elseif($request.action -eq 'activate'){$pattern.SetWindowVisualState([System.Windows.Automation.WindowVisualState]::Normal);$window.element.SetFocus()}else{throw 'unsupported window action'}
    $result=[ordered]@{route=('UIA.Window.'+$request.action);app=(App-Record $process);nativeId=$window.nativeId}}
  default {throw 'unsupported operation'}
}
$result | ConvertTo-Json -Depth 16 -Compress
"""


CAPTURE_SOURCE = r"""
$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing
$bounds=[System.Windows.Forms.SystemInformation]::VirtualScreen
$bitmap=New-Object System.Drawing.Bitmap($bounds.Width,$bounds.Height,[System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
$graphics=[System.Drawing.Graphics]::FromImage($bitmap);$graphics.CopyFromScreen($bounds.Left,$bounds.Top,0,0,$bitmap.Size,[System.Drawing.CopyPixelOperation]::SourceCopy)
$graphics.Dispose();$scale=[Math]::Min(1.0,[Math]::Min(1920.0/$bounds.Width,1080.0/$bounds.Height))
$outWidth=[Math]::Max(1,[int][Math]::Round($bounds.Width*$scale));$outHeight=[Math]::Max(1,[int][Math]::Round($bounds.Height*$scale))
if($outWidth -ne $bounds.Width -or $outHeight -ne $bounds.Height){
  $output=New-Object System.Drawing.Bitmap($outWidth,$outHeight,[System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
  $scaled=[System.Drawing.Graphics]::FromImage($output);$scaled.InterpolationMode=[System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
  $scaled.DrawImage($bitmap,0,0,$outWidth,$outHeight);$scaled.Dispose();$bitmap.Dispose();$bitmap=$output
}
$stream=New-Object System.IO.MemoryStream;$bitmap.Save($stream,[System.Drawing.Imaging.ImageFormat]::Png);$bitmap.Dispose()
[ordered]@{png=[Convert]::ToBase64String($stream.ToArray());x=$bounds.Left;y=$bounds.Top;width=$outWidth;height=$outHeight;nativeWidth=$bounds.Width;nativeHeight=$bounds.Height}|ConvertTo-Json -Compress
"""


class WindowsBackend:
    def __init__(self) -> None:
        self._user32 = ctypes.windll.user32
        self._user32.SetProcessDPIAware()
        self._user32.SendInput.argtypes = [wintypes.UINT, ctypes.POINTER(_INPUT), ctypes.c_int]
        self._user32.SendInput.restype = wintypes.UINT
        self._origin = (0, 0)
        self._scale = (1.0, 1.0)
        self._window_handles: dict[str, str] = {}

    @staticmethod
    def _powershell(source: str, request: dict[str, Any] | None = None, timeout: int = 30) -> Any:
        environment = os.environ.copy()
        if request is not None:
            environment["COMPUTER_USE_NATIVE_REQUEST"] = json.dumps(request, separators=(",", ":"))
        executable = os.environ.get("COMPUTER_USE_POWERSHELL_BINARY", "powershell.exe")
        completed = subprocess.run(
            [executable, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", source],
            env=environment, capture_output=True, text=True, timeout=timeout, check=False,
        )
        if completed.returncode != 0:
            diagnostic = (completed.stderr or completed.stdout).strip().lower()
            permission = "access is denied" in diagnostic or "e_accessdenied" in diagnostic
            raise NativeProviderError(
                "Windows desktop access is denied" if permission else "Windows native desktop operation failed",
                code="permission_denied" if permission else "native_accessibility_failed",
                phase="permission" if permission else "native_api",
            )
        try:
            return json.loads(completed.stdout)
        except json.JSONDecodeError as error:
            raise NativeProviderError("Windows native bridge returned invalid data", code="native_protocol_failed") from error

    def _uia(self, op: str, payload: dict[str, Any] | None = None, **extra: Any) -> Any:
        payload = dict(payload or {})
        handle = str(payload.get("window_handle") or "")
        if handle in self._window_handles and not payload.get("target") and not payload.get("expected_window"):
            payload["target"] = {"providerObjectPath": self._window_handles[handle]}
        request = {"op": op, "payload": payload}
        request.update(extra)
        return self._powershell(POWERSHELL_SOURCE, request)

    @staticmethod
    def _window_handle(pid: int, native_id: str) -> str:
        return "w2:" + hashlib.md5(f"windows\0{pid}\0{native_id}".encode(), usedforsecurity=False).hexdigest()

    def observe_apps(self) -> list[dict[str, Any]]:
        return self._uia("apps").get("items", [])

    def _snapshot(self, raw: dict[str, Any]) -> dict[str, Any]:
        app, selected = raw["app"], raw["selected"]
        pid, generation = int(app["pid"]), str(app["processGeneration"])
        windows = []
        for item in raw.get("windows", []):
            handle = self._window_handle(pid, str(item["nativeId"]))
            self._window_handles[handle] = "window:" + str(item["nativeId"])
            windows.append({"title": item.get("title", ""), "role": item.get("role", "window"),
                            "handle": handle, "providerBusName": app["providerBusName"],
                            "providerObjectPath": "window:" + str(item["nativeId"]), "providerProcessGeneration": generation,
                            "selected": item.get("nativeId") == selected.get("nativeId"), "active": bool(item.get("active")),
                            "modal": bool(item.get("modal")), "showing": bool(item.get("showing", True))})
        if selected.get("frame"):
            for record in raw.get("elements", []):
                if record.get("frame"):
                    record["frame"]["x"] -= selected["frame"]["x"]
                    record["frame"]["y"] -= selected["frame"]["y"]
        identity = json.dumps([(item.get("runtimeId"), item.get("controlType"), item.get("name"), item.get("value")) for item in raw.get("elements", [])], separators=(",", ":"))
        return {"app": app, "windowTitle": selected.get("title", ""), "windowHandle": self._window_handle(pid, str(selected["nativeId"])),
                "windows": windows, "snapshotId": "windows-" + hashlib.sha256(identity.encode()).hexdigest()[:24],
                "sampledAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "windowBounds": selected.get("frame"),
                "treeLines": raw.get("treeLines", []), "elements": raw.get("elements", [])}

    def observe(self, payload: dict[str, Any]) -> dict[str, Any]:
        snapshot = self._snapshot(self._uia("tree", payload))
        if (payload.get("observation") or {}).get("include_screenshot"):
            snapshot.update(self._capture())
        return {"ok": True, "snapshot": snapshot}

    def _capture(self) -> dict[str, Any]:
        raw = self._powershell(CAPTURE_SOURCE, timeout=20)
        self._origin = (int(raw["x"]), int(raw["y"]))
        self._scale = (float(raw["nativeWidth"]) / int(raw["width"]), float(raw["nativeHeight"]) / int(raw["height"]))
        frame_id = "layout-windows-" + hashlib.sha256(
            f"{raw['x']}:{raw['y']}:{raw['nativeWidth']}x{raw['nativeHeight']}:{raw['width']}x{raw['height']}".encode()
        ).hexdigest()[:16]
        return {"app": {"name": "", "pid": 0, "inputCapable": False}, "sampledAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "screenshotPngBase64": raw["png"], "screenshotMimeType": "image/png", "screenshotEncoding": "png",
                "coordinateSpace": "screen_image_px", "frameId": frame_id, "width": int(raw["width"]), "height": int(raw["height"]),
                "desktopLayout": {"originX": int(raw["x"]), "originY": int(raw["y"]),
                                  "width": int(raw["nativeWidth"]), "height": int(raw["nativeHeight"])}}

    def _screen_point(self, x: float, y: float) -> tuple[int, int]:
        return round(x * self._scale[0] + self._origin[0]), round(y * self._scale[1] + self._origin[1])

    def _move(self, x: float, y: float) -> None:
        sx, sy = self._screen_point(x, y)
        if not self._user32.SetCursorPos(sx, sy):
            raise NativeProviderError("SetCursorPos failed", code="input_failed", phase="emit")

    def _click(self, x: float, y: float, button: str = "left", count: int = 1) -> None:
        self._move(x, y)
        flags = {"left": (0x0002, 0x0004), "right": (0x0008, 0x0010), "wheel": (0x0020, 0x0040),
                 "back": (0x0080, 0x0100), "forward": (0x0080, 0x0100)}
        down, up = flags.get(button, flags["left"])
        data = 2 if button == "back" else 1 if button == "forward" else 0
        for _ in range(max(1, count)):
            self._user32.mouse_event(down, 0, 0, data, 0); self._user32.mouse_event(up, 0, 0, data, 0)

    def _unicode(self, text: str) -> None:
        inputs = []
        data = text.encode("utf-16-le")
        for offset in range(0, len(data), 2):
            code = int.from_bytes(data[offset:offset + 2], "little")
            inputs.extend([_keyboard_input(code, 0x0004), _keyboard_input(code, 0x0004 | 0x0002)])
        if inputs:
            array = (_INPUT * len(inputs))(*inputs)
            sent = self._user32.SendInput(len(array), array, ctypes.sizeof(_INPUT))
            if sent != len(array):
                raise NativeProviderError("SendInput did not emit all Unicode keys", code="input_failed", phase="emit", emitted=sent > 0)

    def _keypress(self, keys: list[str] | str) -> None:
        if isinstance(keys, str):
            keys = [part for part in keys.replace("-", "+").split("+") if part]
        names = [str(key).strip().upper() for key in keys]
        codes = {"CTRL":0x11,"CONTROL":0x11,"SHIFT":0x10,"ALT":0x12,"META":0x5B,"WIN":0x5B,
                 "ENTER":0x0D,"RETURN":0x0D,"TAB":0x09,"SPACE":0x20,"BACKSPACE":0x08,"DELETE":0x2E,
                 "ESC":0x1B,"ESCAPE":0x1B,"LEFT":0x25,"UP":0x26,"RIGHT":0x27,"DOWN":0x28,
                 "HOME":0x24,"END":0x23,"PAGEUP":0x21,"PAGEDOWN":0x22}
        resolved = [codes.get(name, ord(name) if len(name) == 1 else 0) for name in names]
        if not resolved or any(code == 0 for code in resolved):
            raise NativeProviderError("unsupported Windows keypress", code="input_failed", phase="emit")
        for code in resolved: self._user32.keybd_event(code, 0, 0, 0)
        for code in reversed(resolved): self._user32.keybd_event(code, 0, 0x0002, 0)

    def _scroll(self, dx: float, dy: float) -> None:
        if dy: self._user32.mouse_event(0x0800, 0, 0, int(-dy), 0)
        if dx: self._user32.mouse_event(0x01000, 0, 0, int(-dx), 0)

    def resolve_and_act(self, payload: dict[str, Any]) -> dict[str, Any]:
        result = self._uia("act", payload)
        if payload["intent"] == "resolve_target":
            raw = self._uia("tree", payload); snapshot = self._snapshot(raw)
            runtime_id = (payload.get("target") or payload.get("element") or {}).get("runtimeId")
            return {"ok": True, "resolved_element": next((item for item in snapshot["elements"] if item.get("runtimeId") == runtime_id), result)}
        emitted = True
        if result.get("route") == "coordinate":
            x, y = result["point"]
            self._click((x - self._origin[0]) / self._scale[0], (y - self._origin[1]) / self._scale[1])
        elif result.get("route") == "keyboard":
            self._unicode(result["text"]) if "text" in result else self._keypress(result["key"])
        elif result.get("route") == "scroll":
            direction=payload["input"]["direction"]; amount=float(payload["input"].get("pages",1))*600
            self._scroll(amount if direction=="right" else -amount if direction=="left" else 0,
                         amount if direction=="down" else -amount if direction=="up" else 0)
        observation = payload.get("observation") or {}
        if observation.get("capture_snapshot") is False:
            return {"ok": True, "observation_skipped": True, "mutation": {"input_events_emitted": emitted, "desktop_state_mutated": True},
                    "action_evidence": {"route": result.get("route", "UIA"), "input_emitted": emitted, "readback_sampled": False}}
        try:
            snapshot = self._snapshot(self._uia("tree", payload))
        except NativeProviderError as error:
            error.emitted = emitted
            raise
        return {"ok": True, "snapshot": snapshot,
                "mutation": {"input_events_emitted": emitted, "desktop_state_mutated": True},
                "action_evidence": {"route": result.get("route", "UIA"), "input_emitted": emitted, "readback_sampled": payload["intent"] == "set_value"}}

    def desktop_execute_batch(self, payload: dict[str, Any]) -> dict[str, Any]:
        before = self._capture(); requested = payload.get("frame_id")
        if requested and requested != before["frameId"]:
            return {"ok": False, "error": "stale GUI layout; take a fresh screenshot", "snapshot": before,
                    "failure": {"code": "stale_layout", "phase": "precondition", "facts_complete": True}}
        inputs=0; waits=0
        try:
            for action in payload["actions"]:
                kind=action["type"]
                if kind=="screenshot": continue
                if kind=="click": self._click(action["x"],action["y"],action.get("button","left"));inputs+=1
                elif kind=="double_click": self._click(action["x"],action["y"],"left",2);inputs+=1
                elif kind=="move": self._move(action["x"],action["y"]);inputs+=1
                elif kind=="drag":
                    path=action["path"];self._move(path[0]["x"],path[0]["y"]);self._user32.mouse_event(0x0002,0,0,0,0)
                    for point in path[1:]:self._move(point["x"],point["y"])
                    self._user32.mouse_event(0x0004,0,0,0,0);inputs+=1
                elif kind=="scroll":self._move(action["x"],action["y"]);self._scroll(action.get("scroll_x",0),action.get("scroll_y",0));inputs+=1
                elif kind=="type":self._unicode(action["text"]);inputs+=1
                elif kind=="keypress":self._keypress(action["keys"]);inputs+=1
                elif kind=="wait":time.sleep(1);waits+=1
        except NativeProviderError as error:
            error.emitted=error.emitted or inputs>0
            raise
        if payload.get("settle_ms",0):time.sleep(payload["settle_ms"]/1000)
        try: after=self._capture()
        except NativeProviderError as error: error.emitted=inputs>0; raise
        changed=png_content_digest(before["screenshotPngBase64"])!=png_content_digest(after["screenshotPngBase64"])
        return {"ok":True,"snapshot":after,"mutation":{"input_events_emitted":inputs>0,"desktop_state_mutated":inputs>0},
                "gui_result":{"dispatched":len(payload["actions"]),"input_events_emitted":inputs>0,"input_actions_dispatched":inputs,
                              "effect_unknown":inputs>0,"quiescent":True,"visual_change_observed":changed,"wait_actions_requested":waits,
                              "gui_lease_state":"valid","gui_lease_revalidated":bool(requested)}}

    def window_list(self, payload: dict[str, Any]) -> dict[str, Any]:
        raw=self._uia("windows",payload);app=raw["app"]
        return {"ok":True,"app_name":app["name"],"app_pid":app["pid"],"window_titles":[item.get("title","") for item in raw["windows"]]}

    def window_action(self, payload: dict[str, Any]) -> dict[str, Any]:
        result=self._uia("window_action",payload,action=payload["action"])
        response={"ok":True,"mutation":{"input_events_emitted":True,"desktop_state_mutated":True},
                  "action_evidence":{"route":result.get("route","UIA.Window"),"input_emitted":True,"readback_sampled":True}}
        if payload["action"]=="activate":
            try: response["snapshot"]=self._snapshot(self._uia("tree",payload)); response["action_evidence"]["readback_verified"]=True
            except NativeProviderError as error: error.emitted=True; raise
        else:
            try:
                after=self._uia("windows",payload)
                if any(str(item.get("nativeId"))==str(result.get("nativeId")) for item in after.get("windows",[])):
                    raise NativeProviderError("closed window is still present",code="readback_failed",phase="readback",emitted=True)
            except NativeProviderError as error: error.emitted=True; raise
            app=result["app"]
            response["snapshot"]={"app":app,"windowAbsent":True,"closedWindowHandle":payload.get("window_handle",""),
                                  "snapshotId":"windows-close-"+hashlib.sha256(f"{app['pid']}:{result.get('nativeId')}:{time.time_ns()}".encode()).hexdigest()[:24],
                                  "sampledAt":time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime())}
            response["action_evidence"]["readback_verified"]=True
        return response


class _KEYBDINPUT(ctypes.Structure):
    _fields_ = [("wVk", wintypes.WORD), ("wScan", wintypes.WORD), ("dwFlags", wintypes.DWORD),
                ("time", wintypes.DWORD), ("dwExtraInfo", ctypes.c_size_t)]


class _MOUSEINPUT(ctypes.Structure):
    _fields_ = [("dx", wintypes.LONG), ("dy", wintypes.LONG), ("mouseData", wintypes.DWORD),
                ("dwFlags", wintypes.DWORD), ("time", wintypes.DWORD), ("dwExtraInfo", ctypes.c_size_t)]


class _HARDWAREINPUT(ctypes.Structure):
    _fields_ = [("uMsg", wintypes.DWORD), ("wParamL", wintypes.WORD), ("wParamH", wintypes.WORD)]


class _INPUTUNION(ctypes.Union):
    _fields_ = [("mi", _MOUSEINPUT), ("ki", _KEYBDINPUT), ("hi", _HARDWAREINPUT)]


class _INPUT(ctypes.Structure):
    _anonymous_ = ("union",)
    _fields_ = [("type", wintypes.DWORD), ("union", _INPUTUNION)]


def _keyboard_input(code: int, flags: int) -> _INPUT:
    return _INPUT(type=1, ki=_KEYBDINPUT(0, code, flags, 0, 0))
