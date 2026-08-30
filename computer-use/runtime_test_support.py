#!/usr/bin/env python3
"""Shared fixtures and dependency-aware patch helpers for Linux provider tests."""

import json

import os

from pathlib import Path

import subprocess

import sys

import tempfile

import unittest

from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import runtime as runtime_entrypoint  # noqa: E402

from native.linux.provider_v1 import (  # noqa: E402
    actionability,
    action_evidence,
    app_resolver,
    atspi_inventory,
    ax_dispatch,
    ax_actions,
    ax_node_properties,
    ax_records,
    ax_snapshot,
    ax_snapshot_diagnostics,
    ax_target_notes,
    ax_transition_evidence,
    ax_virtual_table,
    element_resolver,
    foundation,
    gui_capture,
    gui_dispatch,
    gui_input,
    input_primitives,
    ref_continuity,
    recovery,
    response_builder,
    snapshot,
    window_diagnostics,
    window_resolver,
    wire_v2,
    x11_windows,
)

PROVIDER_MODULES = (
    foundation,
    action_evidence,
    app_resolver,
    x11_windows,
    atspi_inventory,
    ax_node_properties,
    ax_records,
    ax_snapshot,
    ax_virtual_table,
    gui_capture,
    gui_dispatch,
    snapshot,
    ref_continuity,
    recovery,
    element_resolver,
    window_resolver,
    actionability,
    gui_input,
    input_primitives,
    ax_actions,
    ax_dispatch,
    ax_snapshot_diagnostics,
    ax_target_notes,
    ax_transition_evidence,
    window_diagnostics,
    response_builder,
    wire_v2,
)

class RuntimeTestSurface:
    """Compatibility facade that patches only real imported dependencies."""

    def __init__(self):
        object.__setattr__(self, "_originals", {})

    def _targets(self, name):
        modules = (runtime_entrypoint,) + PROVIDER_MODULES
        for module in modules:
            if name in vars(module):
                value = vars(module)[name]
                return [
                    candidate
                    for candidate in modules
                    if vars(candidate).get(name, object()) is value
                ]
        return []

    def __getattr__(self, name):
        targets = self._targets(name)
        if targets:
            return getattr(targets[0], name)
        raise AttributeError(name)

    def __setattr__(self, name, value):
        originals = object.__getattribute__(self, "_originals")
        if name not in originals:
            originals[name] = [
                (module, getattr(module, name))
                for module in self._targets(name)
            ]
        targets = [module for module, _value in originals[name]]
        if not targets:
            setattr(runtime_entrypoint, name, value)
            return
        for module in targets:
            setattr(module, name, value)

    def __delattr__(self, name):
        originals = object.__getattribute__(self, "_originals")
        entries = originals.pop(name, [])
        for target, value in entries:
            setattr(target, name, value)

runtime = RuntimeTestSurface()

def _provider_target_for_test(target):
    if not isinstance(target, dict):
        return None
    result = dict(target)
    result.pop("sourceIndex", None)
    return result

def perform_provider_operation(operation):
    """Exercise behavior cases using only Provider V2 request fields."""
    intent = operation["intent"]
    if intent == "observe_apps":
        return runtime.perform_provider_request(
            "observe_apps",
            {"protocol_version": 2},
        )
    if intent == "observe":
        return runtime.perform_provider_request(
            "observe",
            {
                "protocol_version": 2,
                "app": operation.get("app", ""),
                "app_identity": operation.get("app_identity"),
                "window_handle": operation.get("window_handle", ""),
                "observation": {
                    key: value
                    for key, value in {
                        "text_limit": operation.get("text_limit"),
                        "max_tree_nodes": operation.get("max_tree_nodes"),
                        "max_tree_depth": operation.get("max_tree_depth"),
                        "prune": operation.get("prune"),
                        "boxes": operation.get("boxes"),
                        "include_screenshot": operation.get("include_screenshot"),
                        "screenshot_scope": operation.get("screenshot_scope"),
                    }.items()
                    if value is not None
                },
            },
        )
    if intent == "desktop_execute_batch":
        return runtime.perform_provider_request(
            "desktop_execute_batch",
            {
                "protocol_version": 2,
                "actions": operation.get("actions") or [],
                "settle_ms": operation.get("settle_ms", 800),
                "frame_id": operation.get("frame_id"),
                "lease_state": operation.get("lease_state"),
            },
        )
    if intent == "window_list":
        return runtime.perform_provider_request(
            "window_list",
            {
                "protocol_version": 2,
                "app": operation.get("app", ""),
                "app_identity": operation.get("app_identity"),
                "window_handle": operation.get("window_handle", ""),
            },
        )
    if intent in {"activate", "close"}:
        return runtime.perform_provider_request(
            "window_action",
            {
                "protocol_version": 2,
                "action": intent,
                "app": operation.get("app", ""),
                "app_identity": operation.get("app_identity"),
                "window_handle": operation.get("window_handle", ""),
                "target": _provider_target_for_test(operation.get("target")),
                "observation": {},
            },
        )
    return runtime.perform_provider_request(
        "resolve_and_act",
        {
            "protocol_version": 2,
            "intent": intent,
            "app": operation.get("app", ""),
            "app_identity": operation.get("app_identity"),
            "window_handle": operation.get("window_handle", ""),
            "element": operation.get("element"),
            "target": _provider_target_for_test(operation.get("target")),
            "input": {
                key: value
                for key, value in {
                    "x": operation.get("x"),
                    "y": operation.get("y"),
                    "click_count": operation.get("click_count"),
                    "mouse_button": operation.get("mouse_button"),
                    "click_method": operation.get("click_method"),
                    "action": operation.get("action"),
                    "direction": operation.get("direction"),
                    "pages": operation.get("pages"),
                    "text": operation.get("text"),
                    "key": operation.get("key"),
                    "value": operation.get("value"),
                    "window_centre": operation.get("window_centre"),
                }.items()
                if value is not None
            },
            "window_bounds": operation.get("window_bounds"),
            "observation": {},
        },
    )

STATE = runtime.Atspi.StateType

def effective_runtime_source():
    parts_dir = Path(__file__).resolve().parent / "native" / "linux" / "provider_v1"
    return "".join(
        Path(module.__file__).read_text(encoding="utf-8")
        for module in PROVIDER_MODULES
    )

class FakeStateSet:
    def __init__(self, states):
        self._states = set(states)

    def contains(self, state):
        return state in self._states

class FakeSpan:
    def __init__(self, start, end):
        self.start_offset = start
        self.end_offset = end

class FakeTextIface:
    """假的 Text/EditableText 接口。

    `honest=False` 复现真实世界里的坑：insert_text 返回 True，字符数却不变。
    `caret=None` 模拟不实现 caret 查询的控件。
    """

    def __init__(self, text="", honest=True, accept=True, caret=None, selection=None):
        self.text = text
        self.honest = honest
        self.accept = accept
        self.caret = caret
        self.selection = selection
        self.inserts = []
        self.deletes = []
        self.set_calls = []

    def insert(self, offset, payload):
        self.inserts.append((offset, payload))
        if not self.accept:
            return False
        if self.honest:
            self.text = self.text[:offset] + payload + self.text[offset:]
        return True

    def delete(self, start, end):
        self.deletes.append((start, end))
        if self.honest:
            self.text = self.text[:start] + self.text[end:]
        return True

    def set_contents(self, payload):
        self.set_calls.append(payload)
        if not self.accept:
            return False
        if self.honest:
            self.text = payload
        return True

class FakeExtents:
    def __init__(self, x, y, width, height):
        self.x = x
        self.y = y
        self.width = width
        self.height = height

class FakeComponent:
    def __init__(self, window=None, grabs=False, extents=None):
        self.window = window
        self.grabs = grabs
        self.extents = extents
        self.grab_calls = 0

    def grab_focus(self):
        self.grab_calls += 1
        if not self.grabs:
            return False
        if self.window is not None:
            self.window.states.add(STATE.ACTIVE)
        return True

class FakeNode:
    def __init__(
        self,
        states=(),
        text=None,
        editable=True,
        children=(),
        component=None,
        extents=None,
        value=None,
    ):
        self.states = set(states)
        self._text = text
        self._editable = editable
        self.children = list(children)
        self.component = component
        self.extents = extents
        self.value = value

    # --- AT-SPI Accessible surface used by runtime.py ---
    def get_state_set(self):
        return FakeStateSet(self.states)

    def get_child_count(self):
        return len(self.children)

    def get_child_at_index(self, index):
        return self.children[index]

    def get_text_iface(self):
        return self._text

    def get_editable_text_iface(self):
        return self._text if (self._text is not None and self._editable) else None

    def get_component_iface(self):
        return self.component

    def get_value_iface(self):
        return self.value

    def get_name(self):
        return ""

    def get_role_name(self):
        # 真实 AT-SPI 节点一律实现这个方法。假节点缺了它会让 node_role()
        # 在属性访问阶段就抛 AttributeError——safe() 收的是可调用对象，
        # 拦不住这一步。
        return ""

class FakeAtspiText:
    @staticmethod
    def get_character_count(iface):
        return len(iface.text)

    @staticmethod
    def get_text(iface, start, end):
        return iface.text[start:end]

    @staticmethod
    def get_caret_offset(iface):
        if iface.caret is None:
            raise RuntimeError("caret not supported")
        return iface.caret

    @staticmethod
    def get_n_selections(iface):
        return 1 if iface.selection else 0

    @staticmethod
    def get_selection(iface, index):
        start, end = iface.selection
        return FakeSpan(start, end)

class FakeAtspiEditableText:
    @staticmethod
    def insert_text(iface, offset, payload, length):
        return iface.insert(offset, payload)

    @staticmethod
    def delete_text(iface, start, end):
        return iface.delete(start, end)

    @staticmethod
    def set_text_contents(iface, payload):
        return iface.set_contents(payload)

class FakeAtspiComponent:
    @staticmethod
    def grab_focus(component):
        return component.grab_focus()

    @staticmethod
    def get_extents(component, coord_type):
        return component.extents

class FakeValueIface:
    def __init__(self, value, honest=True):
        self.current = float(value)
        self.honest = honest
        self.set_calls = []

class FakeAtspiValue:
    @staticmethod
    def get_current_value(iface):
        return iface.current

    @staticmethod
    def set_current_value(iface, value):
        iface.set_calls.append(value)
        if iface.honest:
            iface.current = float(value)
        return True

class FakeCoordType:
    SCREEN = "screen"

RELATION = runtime.Atspi.RelationType

class FakeAtspi:
    """保留真的 StateType 枚举，只替换会打到真实 a11y 总线的接口调用。"""

    StateType = STATE
    # RelationType 和 StateType 一样保留**真的**枚举：labelled_by_name() 拿它
    # 做相等比较，换成假值就等于测了一个不存在的分支。
    RelationType = RELATION
    CoordType = FakeCoordType
    Text = FakeAtspiText
    EditableText = FakeAtspiEditableText
    Component = FakeAtspiComponent
    Value = FakeAtspiValue

class AtspiPatchedTestCase(unittest.TestCase):
    def setUp(self):
        self._real_atspi = runtime.Atspi
        runtime.Atspi = FakeAtspi
        self.addCleanup(self._restore)

    def _restore(self):
        runtime.Atspi = self._real_atspi

class _LyingNode(FakeNode):
    """自报子节点数与真实子节点数不一致的节点。

    这不是构造出来的假设：Calc 的 sheet 自报 21 亿个子节点（accessible range
    是整张表），Nautilus 的侧边栏声明自管理却只有 12 个。守卫必须只看
    **自报数**，因为真实数量恰恰是它不该去问的东西。
    """

    def __init__(self, reported, **kwargs):
        super().__init__(**kwargs)
        self.reported = reported

    def get_child_count(self):
        return self.reported

class _TreeNode(FakeNode):
    """够渲染一棵树用的节点。FakeNode 只覆盖了 a11y 接口取值那部分，
    record_for() 还要 role / toolkit / accessible_id / attributes 等一整圈。"""

    def __init__(self, role="panel", name="", x=0, y=0, w=10, h=10, kids=(),
                 description=""):
        super().__init__(
            states=(STATE.SHOWING, STATE.VISIBLE),
            children=kids,
            component=FakeComponent(extents=FakeExtents(x, y, w, h)),
        )
        self.role = role
        self.title = name
        self.description = description

    def get_role_name(self):
        return self.role

    def get_name(self):
        return self.title

    def get_description(self):
        return self.description

    def get_toolkit_name(self):
        return "gtk"

    def get_accessible_id(self):
        return ""

    def get_action_iface(self):
        return None

    def get_n_actions(self):
        return 0

    def get_table_iface(self):
        return None

    def get_attributes(self):
        return {}

    def get_process_id(self):
        return 1

class _ProbeFailureNode(_TreeNode):
    def __init__(self, failing_field, **kwargs):
        super().__init__(**kwargs)
        self.failing_field = failing_field

    def _maybe_fail(self, field):
        if self.failing_field == field:
            raise RuntimeError("provider {} probe failed".format(field))

    def get_component_iface(self):
        self._maybe_fail("component")
        return super().get_component_iface()

    def get_role_name(self):
        self._maybe_fail("role")
        return super().get_role_name()

    def get_name(self):
        self._maybe_fail("name")
        return super().get_name()

    def get_description(self):
        self._maybe_fail("description")
        return super().get_description()

    def get_child_count(self):
        self._maybe_fail("child_count")
        return super().get_child_count()

class _FakeClock:
    """假时钟。sleep 直接推进时间，测试因此既确定又不真的等。"""

    def __init__(self):
        self.now = 0.0

    def monotonic(self):
        return self.now

    def sleep(self, seconds):
        self.now += seconds

class _WindowNode(_TreeNode):
    def __init__(self, role, name, active=False, modal=False):
        super().__init__(role=role, name=name)
        if active:
            self.states.add(STATE.ACTIVE)
        if modal:
            self.states.add(STATE.MODAL)

class _ScriptedApp(_TreeNode):
    """顶层窗口集合随假时钟推进而变化的应用节点。

    timeline 形如 [(生效时刻, [窗口...]), ...]，按时刻升序；读取时取最后一条
    已经生效的。这样就能把实测到的时序原样搬进测试。
    """

    def __init__(self, clock, timeline):
        super().__init__(role="application", name="app")
        self.clock = clock
        self.timeline = timeline

    def _windows(self):
        current = []
        for at, windows in self.timeline:
            if self.clock.now >= at:
                current = windows
        return current

    def get_child_count(self):
        return len(self._windows())

    def get_child_at_index(self, index):
        return self._windows()[index]

class _NoSleep:
    """让 perform_operation 里的固定 sleep 不拖慢测试。"""

    @staticmethod
    def sleep(_seconds):
        return None

    @staticmethod
    def time():
        return 0.0

    @staticmethod
    def monotonic():
        return 0.0

class _FakeRelation:
    def __init__(self, kind, targets):
        self.kind = kind
        self.targets = list(targets)

    def get_relation_type(self):
        return self.kind

    def get_n_targets(self):
        return len(self.targets)

    def get_target(self, index):
        return self.targets[index]

class _NamedNode(FakeNode):
    """带名字、可带 LABELLED_BY 关系的假节点。"""

    def __init__(self, name="", role="", relations=(), **kwargs):  # noqa: D107
        super().__init__(**kwargs)
        self._name = name
        self._role = role
        self._relations = list(relations)

    def get_name(self):
        return self._name

    def get_role_name(self):
        return self._role

    def get_relation_set(self):
        return self._relations

    def get_description(self):
        return getattr(self, "_description", "")

    def get_attributes(self):
        placeholder = getattr(self, "_placeholder", "")
        return {"placeholder-text": placeholder} if placeholder else {}

__all__ = [name for name in globals() if not name.startswith("__")]
