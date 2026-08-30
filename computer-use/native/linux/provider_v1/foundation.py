#!/usr/bin/env python3

import base64
from functools import lru_cache
import hashlib
import json
import math
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import traceback
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

# 渲染出来的树最多占多少字符。
#
# **预算的单位此前是错的。** MAX_ELEMENTS 按节点数发预算，而 MCP 客户端限制的
# 是字节：实测 chrome://flags 正好吃满 1200 个节点，平均每行 112 字符，
# 合计 135327 字符 ≈ 132KB——客户端把超过约 100KB 的工具结果整块换成一个
# 文件指针，在禁用 Read 的跑测里等于 agent **什么都没拿到**
# （26 条真实轨迹里 13% 的观测调用如此）。
#
# 节点数管不住字节：1200 个短节点是 40KB，1200 个长节点是 135KB。
# 所以再加一条**以字符计**的预算，并且在渲染时就停手——
# 不能等渲染完再截，那样 800 个节点的 record_for 开销白付了
# （GIMP 实测 8.45ms/节点，白付 800 个就是 6.7 秒）。
MAX_RENDERED_CHARS = 40000

# 遍历一棵树最多花多少秒。**借鉴 maka-cu 的 macOS 侧**（它的四条硬预算里
# 有一条是 deadline，我们此前只有节点数和深度）。
#
# 为什么节点预算管不住时间：每个节点的开销差一个数量级。本仓库实测 GIMP
# （GAIL）完整 record_for 是 8.45ms/节点，一次 render_tree 调它 3162 次、
# 整棵树 38 秒，**超过 Go 层 30 秒的超时**——a11y 通道在 GIMP 上默认直接不可用。
# maka-cu 的注释记着同型的教训：跨进程的存取面板 23.6ms/节点，1500 个节点
# 35 秒，而宿主只给 20 秒，"an unbounded walk does not merely return late,
# it takes the session with it"。
#
# 我们后来靠"先便宜探针再建记录"把 GIMP 救回来了，但那是针对**那一种**开销
# 分布的优化；deadline 是与分布无关的兜底：无论每个节点多贵，整棵树的耗时
# 都有上界。两者不冲突，一个降低常态成本，一个封住最坏情况。
TREE_DEADLINE_SECONDS = 20.0
MAX_DEPTH = 64
DEFAULT_TEXT_LIMIT = 500
# 抬窗时最多尝试几个 FOCUSABLE 控件。抓焦点会真的改变应用内的焦点位置，
# 所以只试少量最可能的候选，不要把整棵树扫一遍。
FOCUS_GRAB_CANDIDATES = 8
# 合理屏幕坐标/尺寸的上限。超出这个量级的只可能是未渲染控件的 INT_MIN 哨兵值，
# 再夸张的多显示器布局也到不了这个数量级。
MAX_SANE_EXTENT = 100000
# 动作之后、建快照之前的最短安置时间。顶层窗口集合没变时就只等这么久，
# 与加入 settle 判据之前的行为完全一致。
SETTLE_MIN_SECONDS = 0.12
# 开出/关掉窗口之后，最多再等多久让焦点落定。实测焦点转移只要 0.053s，
# 这里留了十几倍余量；超时说明新窗口压根没接管焦点（比如提示气泡这类
# 不可聚焦的顶层），带着当前状态返回并**明说没稳定**，好过把工具卡死。
SETTLE_FOCUS_TIMEOUT_SECONDS = 0.8
SETTLE_POLL_SECONDS = 0.02
# 这些工具的截图**不可关**，`#29` 的 A/B 也不例外。
#
# `drag` 是目前唯一一个：它两头都够不着 a11y。
# - **执行**：三处实现（官方 Codex macOS、本仓库 macOS Kit、本仓库 Linux）
#   都只接受 `from_x/from_y/to_x/to_y`，没有 `element_index`。这不是遗漏——
#   拖拽的**目标位置通常不是元素**（"移到幻灯片下方 15cm 处"不是一个元素），
#   所以元素锚定从根上解决不了它。
# - **验证**：实测把 Impress 的标题从 0.76cm 拖到 15.00cm 之后，
#   a11y 树里该元素的 `Frame` **一点没变**，只有状态栏文本变了。
#   树接不住拖拽的效果，所以事后必须有一张图。
SCREENSHOT_SCOPE_SCREEN = "screen"
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
GUI_MODEL_MAX_WIDTH = 1920
GUI_MODEL_MAX_HEIGHT = 1080
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
AX_WINDOW_ACTION_TYPES = {"activate", "close"}
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
    """a11y 轨要不要顺带带截图。默认**开**。

    设 `COMPUTER_USE_A11Y_SCREENSHOTS=0` 关掉——这是 `#29`（截图 A/B）
    的开关，而不是给日常使用准备的。关掉之后
    GUI 截图由独立的 gui 工具强制返回，不受这个 A11Y 开关影响。
    """
    value = os.environ.get("COMPUTER_USE_A11Y_SCREENSHOTS", "1").strip().lower()
    return value not in {"0", "false", "no", "off"}
# 预算用到这个比例之后，开始丢弃"无名 + 无动作 + 无值"的纯结构容器。
# 深度优先截断等于按遍历顺序随机丢弃，先到的占满配额、后面的整片消失；
# 而结构容器（filler / panel / separator）对 agent 没有可操作价值，
# 是唯一可以安全牺牲的一类。
BUDGET_PRESSURE_RATIO = 0.8

# 裁剪用的角色判据：前缀/后缀匹配加一个精确集合，再叠加可见性要求。
#
# 离线评测（13 步轨迹 / 9 步元素定向，含完整对话框链路）显示这套判据能压到
# 22% 且保留率 100%——是所有候选里唯一同时最激进且无损的。前提是渲染保真度
# 到位：单元格要带 Frame、角色不能硬编码，两者都已修。
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
    """角色是否属于"agent 可能需要操作或读取"的一类。"""
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
    """节点是否实现 Text 接口。

    不能用 Accessible.is_text —— 该便捷方法是 libatspi 2.52+ 才加入的，
    在 at-spi2-core 2.44（Ubuntu 22.04）上不存在。而且它是**属性访问**，
    会在传进 safe() 之前就抛 AttributeError，safe() 的 try/except 兜不住，
    导致 get_app_state 整个失败。

    get_text_iface() 在新旧版本都存在，是更基础也更可移植的判据。
    """
    return safe(node.get_text_iface) is not None


def has_editable_text_iface(node):
    """节点是否实现 EditableText 接口。理由同 has_text_iface。"""
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


# Chromium/Electron 在文本里用 U+FFFC（对象替换符）给嵌入对象占位。
# 它对 agent 零信息量，却会渲染成 `Value: ￼￼￼` 这样的乱码。
# VS Code 欢迎页实测：186 个占位符散布在 115 行里，纯占位的 Value 段
# 合计约 247 token——不多，但它更严重的问题是**看起来像内容**：
# 一个 `Value: ￼` 会让 agent 以为这个控件有值。
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


# 子节点数硬上限。LibreOffice Calc 的 sheet 节点会谎报 2^31 个子节点：
# 它的 accessible range 是整张表（16384 列 × 1048576 行），
# getAccessibleChildCount() 返回 rows*cols = 1.7e10，经 D-Bus int32 截断后
# 就是 2147483647。朴素遍历掉进这个节点不是"慢"，是永远不会结束。
# 源码：sc/source/ui/Accessibility/AccessibleTableBase.cxx:274
#       （上游注释原文：'FIXME: ... is a plain and simple madness'）
HARD_CHILD_CAP = int(os.environ.get("COMPUTER_USE_MAX_CHILDREN", "4096"))

# 单个容器最多发起多少次 child_at()。与 HARD_CHILD_CAP 的区别：后者决定
# "要不要枚举"，这个决定"最多枚举几个"，用于兜住不声明 MANAGES_DESCENDANTS
# 却依然谎报子节点数的实现。
MAX_CHILD_FANOUT = int(os.environ.get("COMPUTER_USE_MAX_FANOUT", "512"))

# 声明了 MANAGES_DESCENDANTS 但自报子节点数不超过这个值时，仍然直接枚举。
# 该状态是"我可能很大，别硬枚举"的提示，不是"我一定很大"的事实：
# Nautilus 的侧边栏就声明了它，却只有 12 个子节点，而且**不实现 Table 接口**，
# 于是坐标寻址兜底必然失败——结果整个侧边栏对 agent 不可见。
# 真正的危险案例（Calc 的 sheet 自报 21 亿）会被 HARD_CHILD_CAP 拦住，
# 不依赖这个阈值。
MANAGED_ENUMERATE_CAP = int(os.environ.get("COMPUTER_USE_MANAGED_CAP", "256"))

# find_first() 的节点预算。它被 focused_summary/selected_text 用于定位焦点，
# 原实现无上限，谓词不匹配时会尝试走遍整棵树。
FIND_FIRST_BUDGET = int(os.environ.get("COMPUTER_USE_FIND_BUDGET", "4000"))

# 已被 click 工具覆盖的语义动作名。必须与 preferred_action_index() 里的
# preferred_exact 保持一致 —— 两者是同一件事的两面：一个负责调用，
# 一个负责不要重复展示。
CLICK_COVERED_ACTIONS = {
    "click",
    "press",
    "activate",
    "default.activate",
    "invoke",
    "select",
    "toggle",
    "open",
    # Chromium/Electron 的默认动作名。VS Code 实测：19 个节点的动作表是
    # ('doDefault', 'showContextMenu')——`doDefault` 就是它们的点击入口。
    # 不认这个名字的话，这些元素既拿不到 [has-click-action] 标记，
    # click_method "accessibility" 也会直接失败，整个 Electron 系应用
    # 在语义通道上等于不可点。
    "dodefault",
    # Gecko/Thunderbird 用**结果状态**给复选框的动作命名，而不是 "toggle"：
    # 勾上时提供 `uncheck`、没勾时提供 `check`，任一时刻只暴露适用的那一个。
    # 因此调用它就等于 toggle，正是 click 该做的事。
    # 不认这两个名字的话，Gecko 的复选框全部退回坐标点击——而设置类界面
    # 几乎全是复选框（Thunderbird 5/14 个任务是账户设置、3/14 是消息过滤器）。
    "check",
    "uncheck",
}

# 名字里含 click/press/activate，但**作用对象不是这个元素本身**的动作。
# 必须显式排除：`preferred_action_index()` 的兜底是子串匹配，
# `clickAncestor` 会被它匹中，于是 agent 以为点中了目标，
# 实际点的是祖先节点——而且从返回值和树里都看不出来。
# VS Code 实测有 14 个节点的动作表是 ('clickAncestor', 'showContextMenu')。
NON_SELF_ACTIONS = {"clickancestor"}

# **每个节点都有、因而不携带任何信息**的动作。
#
# 实测 chrome://flags：1200 个渲染节点里 `showContextMenu` 出现 **1200 次**——
# 每一个节点都有。Chrome/Blink 给整棵 DOM 都挂了它，因为任何元素都能右键。
#
# 不排掉它有两个后果，都很硬：
#
#  1. **剪枝完全失效。** is_structural_filler 的判据之一是"没有动作"，
#     而这个动作人人都有，于是没有任何节点会被判成填充物。实测那棵树
#     1200 行 / 135327 字符，其中 530 个是纯文本 static —— 而 MCP 客户端会把
#     超过约 100KB 的工具结果整块换成一个文件指针，在禁用 Read 的跑测里
#     等于 agent **什么都没拿到**（实测 26 条轨迹中 13% 的观测调用如此）。
#  2. 渲染出来的 `[actions=…]` 里 1200 行全带这一项，纯噪声。
#
# 它也确实不是一条可用的操作路径：右键菜单打开的是菜单，不是激活这个元素。
UNIVERSAL_ACTIONS = {"showcontextmenu"}


def normalized_action(label):
    """把动作名归一到只剩字母数字，再做比对。

    同一个语义在不同工具包里拼法不同：Chromium/Electron 叫 `clickAncestor`，
    Gecko/Thunderbird 叫 `click ancestor`（带空格）。只排掉其中一种，
    另一种照样会被子串兜底匹中，于是 agent 又去点了祖先节点。
    实测两者都出现过——窄判据漏变体，这里按归一化比。
    """
    return "".join(ch for ch in str(label or "").lower() if ch.isalnum())

# 未展开的菜单不递归其子项。实测默认配额 1200 下，LibreOffice 的菜单树会占掉
# 100% 配额（一份完整菜单栏约 780 节点），表格单元格一个都进不来 —— 功能等于
# 不存在。只对菜单类角色应用该规则：其它中间层容器（panel / scroll pane）在
# LibreOffice 上普遍不设 SHOWING，一并过滤会把整棵树砍空。
MENU_ROLES = {
    "menu",
    "menu bar",
    "menu item",
    "check menu item",
    "radio menu item",
    "popup menu",
}


def should_enumerate_children(node, count=None):
    """是否应该枚举该节点的子节点。

    两道守卫都只读 libatspi 的本地缓存（ATSPI_CACHE_DEFAULT 覆盖 STATES 与
    CHILDREN），不产生 D-Bus 往返，因此可以无脑放在遍历热路径上：

    1. MANAGES_DESCENDANTS —— AT-SPI 规范对超大容器（表格等）的正式契约：
       "the children should not, and need not, be enumerated by the client"。
       Calc 的 sheet 正是这么标记的（AccessibleSpreadsheet.cxx:1066）。
       这类容器要用 Table.get_accessible_at(row, col) 按坐标寻址，
       或 Component.get_accessible_at_point() 做点命中，而不是枚举。
    2. child_count 硬上限 —— 兜住任何谎报子节点数的实现。
    """
    if count is None:
        count = child_count(node)
    if count > HARD_CHILD_CAP:
        return False
    if state_contains(node, Atspi.StateType.MANAGES_DESCENDANTS):
        # 自管理声明是关于**规模**的提示。自报数量很小时它与声明自相矛盾，
        # 此时按数量走：枚举 12 个子节点既安全又是拿到内容的唯一办法
        # （这类容器往往不实现 Table，坐标寻址兜底根本用不上）。
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
    # 未渲染的控件在 GTK 上会返回 INT_MIN 量级的原点。尺寸看着正常（常见 1x1），
    # 所以只过滤 width/height 拦不住：它们会带着 -2147483648 这样的坐标进入
    # element 树，coordinate click/drag 打上去就落到无意义的位置。
    if abs(rect.x) > MAX_SANE_EXTENT or abs(rect.y) > MAX_SANE_EXTENT:
        return None
    return frame(rect.x, rect.y, rect.width, rect.height)


def extents(node):
    component = safe(node.get_component_iface)
    if component is None:
        return None
    rect = safe(lambda: Atspi.Component.get_extents(component, Atspi.CoordType.SCREEN))
    return sane_rect_frame(rect)


def strict_prune_probe(node, window_bounds, text_limit):
    """Read pruning inputs without converting provider errors into empty values."""
    values = {}
    errors = {}

    def read(field, operation, default=None):
        try:
            value = operation()
        except Exception as exc:
            errors[field] = type(exc).__name__
            return default
        return value

    component = read("component", node.get_component_iface)
    if component is None:
        values["frame"] = None
    else:
        rect = read(
            "frame",
            lambda: Atspi.Component.get_extents(
                component, Atspi.CoordType.SCREEN
            ),
        )
        bounds = sane_rect_frame(rect)
        if bounds is not None and window_bounds is not None:
            bounds = frame(
                bounds["x"] - window_bounds["x"],
                bounds["y"] - window_bounds["y"],
                bounds["width"],
                bounds["height"],
            )
        values["frame"] = bounds

    values["role"] = str(
        read("role", node.get_role_name, "") or ""
    )
    values["name"] = limit_text(
        str(read("name", node.get_name, "") or ""),
        text_limit=text_limit,
    )
    values["description"] = limit_text(
        str(read("description", node.get_description, "") or ""),
        text_limit=text_limit,
    )
    raw_count = read("child_count", node.get_child_count)
    if raw_count is None:
        values["child_count"] = None
    else:
        try:
            values["child_count"] = max(0, int(raw_count or 0))
        except Exception as exc:
            errors["child_count"] = type(exc).__name__
            values["child_count"] = None
    return values, errors
