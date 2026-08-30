"""Linux provider module: atspi inventory."""

from .foundation import (
    Atspi,
    OperationFailure,
    SETTLE_FOCUS_TIMEOUT_SECONDS,
    SETTLE_MIN_SECONDS,
    SETTLE_POLL_SECONDS,
    inventory_complete,
    limit_text,
    node_name,
    node_pid,
    node_role,
    os,
    process_generation,
    provider_bus_name,
    re,
    state_contains,
    subprocess,
    time,
)

from .x11_windows import (
    app_windows,
    iter_apps,
)
from .primitives import quoted
from .window_diagnostics import accepts_user_input

def wait_for_ui_to_settle(
    app,
    before,
    min_seconds=SETTLE_MIN_SECONDS,
    timeout_seconds=SETTLE_FOCUS_TIMEOUT_SECONDS,
    poll_seconds=SETTLE_POLL_SECONDS,
    clock=time.monotonic,
    sleep=time.sleep,
):
    """动作之后等界面安置好，再让调用方去建快照。

    修的是一个**静默操作错误窗口**的竞态。实测（Thunderbird
    `Tools → Message Filters`，AT-SPI 2.44 + GNOME Shell）：

        t=0.070s  新窗口已经进入 AT-SPI 树，但 ACTIVE 还挂在主窗口身上
        t=0.123s  ACTIVE 才转移过去（X11 的 _NET_ACTIVE_WINDOW 同一刻翻转）

    而这里原先是固定 `sleep(0.12)`——**正好压在边界上**。快照因此有相当概率
    落在 0.070~0.123 这段窗口期里建成：树是完整的、状态是自洽的、
    `main_window()` 也按既有判据挑得没错，挑出来的却是主窗口。
    agent 拿着主窗口的树按索引点击，就静默地操作了另一个窗口——
    实测后果是在 Message Filters 里点 `New…`，弹出的是主窗口的「新建邮件」。

    **把固定 sleep 调大修不了它**：那只是把边界挪到另一个不确定的位置，
    应用越慢、机器越忙越容易重新撞上。判据必须从"等够时间"换成"等到状态"。

    完成条件分两种：
    - 顶层窗口集合与动作前一致 → 什么都没开也没关，立刻返回（常见路径不变慢）。
    - 集合变了 → 等到**新出现的那个窗口**报 ACTIVE 或 MODAL 为止；
      如果只有窗口关闭，则等到剩下的窗口里有人接管焦点。

    超时**不静默**：返回一条说明，因为"新窗口没接管焦点"意味着这份快照可能
    照的不是 agent 以为的那个窗口，这正是它需要知道的事。

    **边界：窗口在最短安置时间之后才出现的，这里接不住。**
    实测六例开窗口，进入 AT-SPI 树的耗时都 ≤ 0.070s：

        Thunderbird 消息过滤器 (Gecko) 0.070   LibreOffice 查找替换 (VCL) 0.045
        gedit 查找替换       (GTK)   0.045   gedit 另存为        (GTK) 0.064
        VLC 首选项           (Qt)    0.046

    `SETTLE_MIN_SECONDS` = 0.12 对最慢的一例仍有 1.7 倍余量，所以没有再加一段
    固定观察期——那要给**每一个**动作加上几百毫秒，用真实延迟去买一个没量到的
    余量。更慢的应用漏掉之后是另一种失败：快照照到的是动作前的状态，
    新窗口完全不出现。那是**看得见**的失败（agent 会发现动作像是没生效），
    与这里要修的"树自洽、却照错了窗口"不是一回事，不要混为一谈。
    """
    sleep(min_seconds)
    if before is None:
        return None
    deadline = clock() + timeout_seconds
    while True:
        try:
            windows = app_windows(app)
            current = {
                (index, node_role(window), node_name(window))
                for index, window in windows
            }
        except Exception:
            # 应用可能被这次动作关掉了。安置等待失败不该盖住真正的错误——
            # 紧接着的 build_snapshot 会把它报出来。
            return None
        if current == before:
            return None
        appeared = current - before
        for index, window in windows:
            focused = state_contains(window, Atspi.StateType.ACTIVE) or state_contains(
                window, Atspi.StateType.MODAL
            )
            if not focused:
                continue
            if not appeared or (index, node_role(window), node_name(window)) in appeared:
                return None
        if clock() >= deadline:
            return (
                "The set of top-level windows changed but no window took focus within "
                "{:.1f}s. The snapshot below may show a different window than the one "
                "this action opened — confirm the window title before using any "
                "target ref from it.".format(timeout_seconds)
            )
        sleep(poll_seconds)


def loose(text):
    """把应用名压成"只剩字母数字"的形式，用来做**分隔符不敏感**的比对。

    这条是从真实 agent 的轨迹里读出来的：13 条轨迹里有一次直接报了
    `appNotFound("google-chrome")`——而这台机器上那个应用叫 `Google Chrome`。
    agent 猜的名字**完全合理**，只是连字符和空格对不上。

    更贵的是它的连带影响：13 条轨迹里 **12 条**的开局都是
    `list_apps → get_app_state`。agent 不敢直接猜名字，于是每道题都先花一步
    去列应用。分隔符归一化之后，猜中的概率大幅提高，那一步就不必付了。
    """
    return "".join(ch for ch in str(text or "").lower() if ch.isalnum())


_GENERIC_EXECUTABLE_PATH_COMPONENTS = {
    "",
    "app",
    "apps",
    "application",
    "applications",
    "bin",
    "lib",
    "lib32",
    "lib64",
    "libexec",
    "local",
    "opt",
    "program",
    "programs",
    "sbin",
    "usr",
}


def executable_identity_names(path):
    """Return conservative product identity candidates from an executable path.

    The executable basename is always evidence. Some packaged applications put
    a launcher with a different name below a generic directory, for example:

        /usr/lib/<product>/program/<launcher>

    In that shape, the nearest non-generic ancestor is also useful product
    identity evidence. Do not keep walking through a meaningful immediate
    parent: `/opt/vendor/browser/browser` must not make `vendor` an alias.
    """
    normalized = os.path.normpath(str(path or "").strip())
    if not normalized or normalized in {".", os.sep}:
        return set()
    parts = [part.strip().lower() for part in normalized.split(os.sep) if part]
    if not parts:
        return set()
    base = parts[-1]
    stem = base.rsplit(".", 1)[0]
    names = {base}
    if stem:
        names.add(stem)
    skipped_generic_parent = False
    for parent in reversed(parts[:-1]):
        parent_stem = parent.rsplit(".", 1)[0]
        if parent in _GENERIC_EXECUTABLE_PATH_COMPONENTS:
            skipped_generic_parent = True
            continue
        if parent in {base, stem} or parent_stem in {base, stem}:
            break
        if skipped_generic_parent:
            names.add(parent)
            if parent_stem:
                names.add(parent_stem)
        break
    return {name for name in names if name}


def process_names(pid, proc_root="/proc"):
    """Return verified process and executable product identities, lowercase.

    AX application names, launcher names, and user-facing product names often
    differ. `/proc` provides process-backed evidence without maintaining a
    product alias table. Ambiguity is still resolved by the caller across all
    current applications rather than selecting the first match.
    """
    names = set()
    if not pid:
        return names
    try:
        comm_path = os.path.join(proc_root, str(int(pid)), "comm")
        with open(comm_path, encoding="utf-8") as handle:
            comm = handle.read().strip().lower()
        if comm:
            names.add(comm)
    except Exception:
        pass
    try:
        cmd_path = os.path.join(proc_root, str(int(pid)), "cmdline")
        with open(cmd_path, "rb") as handle:
            raw = handle.read()
        # 先按 \0 切，再按空白切。
        #
        # 为什么两步都要：cmdline 的约定是 \0 分隔，但**并非所有进程都守约**——
        # 这台机器上 Chrome 那条读出来是一整块
        # `chrome --remote-debugging-port=1337`，中间是空格。只按 \0 切就会把整块
        # 当成 argv[0]，再取 basename，于是从 `…/drugs.com` 里读出 "drugs.com"，
        # 把一个**网页地址**注册成了 Chrome 的别名。那样 `resolve_app("drugs")`
        # 会解析到 Chrome——工具凭空编出一个不存在的应用名，正是最该避免的那种谎。
        first = raw.split(b"\0")[0].decode("utf-8", "replace").split()
        if first:
            names.update(executable_identity_names(first[0]))
    except Exception:
        pass
    try:
        exe_path = os.readlink(
            os.path.join(proc_root, str(int(pid)), "exe")
        )
        names.update(executable_identity_names(exe_path))
    except Exception:
        pass
    return names


RESOURCE_SCAN_MAX_FDS = 256
RESOURCE_SCAN_MAX_CANDIDATES = 16
RESOURCE_SCAN_MAX_OPEN_FINGERPRINTS = 16
RESOURCE_SCAN_DEADLINE_SECONDS = 0.010
RESOURCE_SCAN_MAX_CMDLINE_BYTES = 65536
RESOURCE_SCAN_MAX_CMDLINE_ARGS = 128


def stat_fingerprint(stat_result):
    if stat_result is None:
        return None
    return {
        "dev": int(stat_result.st_dev),
        "ino": int(stat_result.st_ino),
        "size": int(stat_result.st_size),
        "mtimeNs": int(stat_result.st_mtime_ns),
    }


def resource_kind(stat_result):
    if stat_result is None:
        return "unknown"
    mode = stat_result.st_mode
    if (mode & 0o170000) == 0o100000:
        return "file"
    if (mode & 0o170000) == 0o040000:
        return "directory"
    return "unknown"


def path_matches_window_title(path, title, kind="unknown", app_aliases=()):
    title = str(title or "").strip()
    if not title:
        return False
    base = os.path.basename(str(path or "").rstrip(os.sep)).strip()
    if len(base) < 3:
        return False
    prefixes = (base, "* " + base)
    if title in prefixes:
        return True
    separators = (" - ", " — ", " – ")
    normalized_aliases = {
        loose(alias)
        for alias in app_aliases
        if len(loose(alias)) >= 3
    }
    for prefix in prefixes:
        for separator in separators:
            marker = prefix + separator
            if not title.startswith(marker):
                continue
            suffix = title[len(marker):].strip()
            normalized_suffix = loose(suffix)
            if (
                normalized_suffix
                and not any(other in suffix for other in separators)
                and normalized_suffix in normalized_aliases
            ):
                return True
    return False


def process_resource_candidates(
    pid,
    window_title,
    app_aliases=(),
    proc_root="/proc",
    clock=time.monotonic,
):
    """Correlate the selected window with local file/directory candidates.

    A process can hold many unrelated files, so fd/cmdline presence alone never
    resolves identity. A candidate must also match the current window title.
    """
    result = {
        "identityStatus": "unknown",
        "identityKind": "unknown",
        "identityValue": None,
        "identityBasis": [],
        "candidates": [],
        "dirty": None,
        "dirtyBasis": None,
        "titleModifiedMarker": (
            "*" if str(window_title or "").lstrip().startswith("*") else None
        ),
        "diskStatus": "unknown",
        "pathFingerprint": None,
        "openFileFingerprint": None,
        "openFileFingerprints": [],
        "scanDeadlineExceeded": False,
        "scanIncomplete": False,
    }
    if not pid:
        return result

    deadline = clock() + RESOURCE_SCAN_DEADLINE_SECONDS
    candidates = {}

    def add(path, kind, source, open_fingerprint=None, deleted=False):
        if not path_matches_window_title(
            path,
            window_title,
            kind=kind,
            app_aliases=app_aliases,
        ):
            return
        item = candidates.setdefault(path, {
            "path": path,
            "kind": kind,
            "sources": [],
            "deleted": bool(deleted),
            "openFileFingerprints": [],
        })
        if source not in item["sources"]:
            item["sources"].append(source)
        if open_fingerprint is not None:
            if (
                open_fingerprint not in item["openFileFingerprints"]
                and len(item["openFileFingerprints"])
                < RESOURCE_SCAN_MAX_OPEN_FINGERPRINTS
            ):
                item["openFileFingerprints"].append(open_fingerprint)
            elif open_fingerprint not in item["openFileFingerprints"]:
                result["scanIncomplete"] = True
        item["deleted"] = item["deleted"] or bool(deleted)

    fd_dir = os.path.join(proc_root, str(int(pid)), "fd")
    try:
        names = []
        with os.scandir(fd_dir) as entries:
            for entry in entries:
                if clock() > deadline:
                    result["scanDeadlineExceeded"] = True
                    result["scanIncomplete"] = True
                    break
                if not entry.name.isdigit():
                    continue
                names.append(entry.name)
                if len(names) > RESOURCE_SCAN_MAX_FDS:
                    result["scanIncomplete"] = True
                    names = names[:RESOURCE_SCAN_MAX_FDS]
                    break
        names.sort(key=int)
    except Exception:
        names = []
        result["scanIncomplete"] = True
    for name in names:
        if clock() > deadline:
            result["scanDeadlineExceeded"] = True
            result["scanIncomplete"] = True
            break
        fd_path = os.path.join(fd_dir, name)
        try:
            target = os.readlink(fd_path)
            deleted = target.endswith(" (deleted)")
            path = target[:-10] if deleted else target
            if not os.path.isabs(path):
                continue
            opened = os.stat(fd_path)
            if clock() > deadline:
                result["scanDeadlineExceeded"] = True
                result["scanIncomplete"] = True
                break
            kind = resource_kind(opened)
            if kind == "unknown":
                continue
            add(
                path,
                kind,
                "fd_title",
                {
                    "fd": int(name),
                    **stat_fingerprint(opened),
                    "deleted": deleted,
                },
                deleted=deleted,
            )
        except Exception:
            result["scanIncomplete"] = True
            continue

    cmdline_path = os.path.join(proc_root, str(int(pid)), "cmdline")
    argv = []
    if not result["scanDeadlineExceeded"]:
        try:
            with open(cmdline_path, "rb") as handle:
                raw = handle.read(RESOURCE_SCAN_MAX_CMDLINE_BYTES + 1)
            if clock() > deadline:
                result["scanDeadlineExceeded"] = True
                result["scanIncomplete"] = True
                raw = b""
            if len(raw) > RESOURCE_SCAN_MAX_CMDLINE_BYTES:
                result["scanIncomplete"] = True
                raw = raw[:RESOURCE_SCAN_MAX_CMDLINE_BYTES]
            argv = [
                part.decode("utf-8", "replace")
                for part in raw.split(b"\0")
                if part
            ]
            if len(argv) > RESOURCE_SCAN_MAX_CMDLINE_ARGS + 1:
                result["scanIncomplete"] = True
                argv = argv[:RESOURCE_SCAN_MAX_CMDLINE_ARGS + 1]
        except Exception:
            argv = []
            result["scanIncomplete"] = True
    # argv[0] is the executable, not a document/workspace candidate.
    for arg in argv[1:]:
        if clock() > deadline:
            result["scanDeadlineExceeded"] = True
            result["scanIncomplete"] = True
            break
        path = os.path.abspath(arg) if os.path.isabs(arg) else ""
        if not path:
            continue
        try:
            item_stat = os.stat(path)
        except Exception:
            continue
        if clock() > deadline:
            result["scanDeadlineExceeded"] = True
            result["scanIncomplete"] = True
            break
        kind = resource_kind(item_stat)
        if kind != "unknown":
            add(path, kind, "cmdline_title")

    ordered = sorted(
        candidates.values(),
        key=lambda item: (item["path"], ",".join(item["sources"])),
    )[:RESOURCE_SCAN_MAX_CANDIDATES]
    result["candidates"] = ordered
    if len(ordered) != 1:
        if len(ordered) > 1:
            result["identityStatus"] = "ambiguous"
        return result
    if result["scanIncomplete"] or result["scanDeadlineExceeded"]:
        return result

    selected = ordered[0]
    if "fd_title" not in selected.get("sources", []):
        # Command-line paths describe launch history, not necessarily the
        # currently open document after session restore or File -> Open.
        return result
    path = selected["path"]
    fingerprints = selected.get("openFileFingerprints") or []
    disk_status = "unknown"
    path_fingerprint = None
    if clock() > deadline:
        result["scanDeadlineExceeded"] = True
        result["scanIncomplete"] = True
        return result
    try:
        path_stat = os.stat(path)
        disk_status = "available"
        path_fingerprint = stat_fingerprint(path_stat)
    except FileNotFoundError:
        disk_status = "missing"
    except PermissionError:
        disk_status = "denied"
    except Exception:
        disk_status = "unknown"
    if clock() > deadline:
        result["scanDeadlineExceeded"] = True
        result["scanIncomplete"] = True
        return result

    result["identityStatus"] = "resolved"
    result["identityKind"] = selected["kind"]
    result["identityValue"] = path
    result["identityBasis"] = selected["sources"]
    result["openFileFingerprints"] = fingerprints
    if len(fingerprints) == 1:
        result["openFileFingerprint"] = fingerprints[0]
    result["diskStatus"] = disk_status
    result["pathFingerprint"] = path_fingerprint
    return result


def matches_query(app, query):
    normalized = query.strip().lower()
    if not normalized:
        return False
    if normalized.isdigit() and node_pid(app) == int(normalized):
        return True
    tight = loose(normalized)
    if tight:
        for candidate in process_names(node_pid(app)):
            tight_process = loose(candidate)
            if tight_process and (tight == tight_process
                                  or tight in tight_process
                                  or tight_process in tight):
                return True
    app_name = node_name(app).lower()
    if app_name == normalized or normalized in app_name:
        return True
    # 分隔符不敏感：google-chrome / google_chrome / googlechrome 都能对上
    # "Google Chrome"。只在两边都非空时才比，避免空串匹配一切。
    tight_query, tight_name = loose(normalized), loose(app_name)
    if tight_query and tight_name and (
            tight_query == tight_name or tight_query in tight_name
            or tight_name in tight_query):
        return True
    for _, window in app_windows(app):
        title = node_name(window).lower()
        if title == normalized or normalized in title:
            return True
        tight_title = loose(title)
        if tight_query and tight_title and tight_query in tight_title:
            return True
    return False


def matches_app_bootstrap_query(app, query):
    """Match only application/process identity, never a document or dialog title."""
    normalized = str(query or "").strip().lower()
    if not normalized:
        return False
    if normalized.isdigit() and node_pid(app) == int(normalized):
        return True
    tight_query = loose(normalized)
    for candidate in process_names(node_pid(app)):
        tight_process = loose(candidate)
        if tight_query and tight_process and (
            tight_query == tight_process
            or tight_query in tight_process
            or tight_process in tight_query
        ):
            return True
    app_name = str(node_name(app) or "").strip().lower()
    tight_name = loose(app_name)
    return bool(
        app_name == normalized
        or normalized in app_name
        or (
            tight_query
            and tight_name
            and (
                tight_query == tight_name
                or tight_query in tight_name
                or tight_name in tight_query
            )
        )
    )


def foreground_app():
    """X11 认定的前台窗口属于哪个 a11y 应用。拿不到就 None。

    这条存在的理由是轨迹给的：13 条真实 agent 轨迹里 **12 条**的第一步是
    `list_apps`，第二步才是 `get_app_state`。它们真正想知道的是同一件事——
    "我现在面前是什么"——而这件事此前需要两次调用外加一次猜名字。

    改用 X11 的活动窗口而不是 AT-SPI 的 ACTIVE 位：后者在门户对话框、
    LibreOffice 等一大类窗口上**根本不设**（本仓库反复实测过），
    拿它当"前台"的判据会经常判空。
    """
    try:
        active = subprocess.run(["xdotool", "getactivewindow"],
                                capture_output=True, text=True, timeout=3)
        if active.returncode != 0 or not active.stdout.strip():
            return None
        prop = subprocess.run(["xprop", "-id", active.stdout.strip(), "_NET_WM_PID"],
                              capture_output=True, text=True, timeout=3).stdout
    except Exception:
        return None
    digits = [int(n) for n in re.findall(r"\d+", prop)]
    if not digits:
        return None
    target_pid = digits[-1]
    for app in iter_apps():
        if node_pid(app) == target_pid:
            return app
    return None


def input_capable_app_labels():
    """当前**能承载用户输入**的应用，一行一个，带进程名与窗口标题。

    只列能接收输入的：把窗口管理器、合成器、面板、桌面本身也摆进候选表，
    等于邀请调用方去操作它们——实测确实发生过（agent 对桌面反复 press_key
    254 次）。候选表的语义必须是"这些是你可以操作的"，而不是"这些进程活着"。
    """

    running = []
    try:
        for app in iter_apps():
            name = str(node_name(app) or "").strip()
            if not name:
                continue
            if not accepts_user_input(app):
                continue
            # 一并给出进程名。a11y 名和进程名常常毫不相干（Archive Manager /
            # file-roller），而 agent 猜的多半是后者——把两者摆在一起，它下一次
            # 就知道该怎么对应，而不是再猜一轮。
            others = sorted(n for n in process_names(node_pid(app))
                            if loose(n) and loose(n) != loose(name))
            label = "{} ({})".format(name, others[0]) if others else name
            # 窗口标题也要列出来。
            #
            # 轨迹证据：GIMP 那一段里 agent 三次拿**对话框的名字**当应用名去调，
            # `appNotFound("script-fu")` 两次、`appNotFound("file-jpeg")` 一次。
            # 它并没有猜错——屏幕上确实摆着一个 "Script-Fu Console"、一个
            # "Export Image as JPEG"。错的是它无从知道这两个窗口都归 gimp 管：
            # 候选表里只有一行光秃秃的 "gimp"，看不出它名下有这些窗口。
            titles = []
            for _, window in app_windows(app):
                title = str(node_name(window) or "").strip()
                if title and loose(title) != loose(name):
                    titles.append(limit_text(title, text_limit=44))
                if len(titles) >= 2:
                    break
            if titles:
                label += " — windows: " + ", ".join(quoted(t) for t in titles)
            running.append(label)
    except Exception:
        pass
    return running


def infrastructure_app_error(app, query):
    """解析到了桌面基础设施而不是应用时的拒绝理由。

    必须**明确拒绝**而不是照常返回：动作工具此前无条件接受任何有顶层窗口的
    进程，于是窗口管理器 / 桌面 / 面板都能当"应用"用，输入送过去石沉大海，
    而调用方看到的是一次次"成功"。实测有 agent 因此对桌面本身按了 254 次键。
    """
    name = str(node_name(app) or "").strip() or query
    candidates = input_capable_app_labels()
    listing = (" Applications that can take input right now: {}.".format(
        ", ".join(sorted(set(candidates))[:20])) if candidates else
        " No application with an input-capable window is currently on the "
        "accessibility bus.")
    return RuntimeError(
        'appNotUsable("{}"): {!r} is on the accessibility bus, but every '
        'one of its top-level windows is untitled and exposes no interactive '
        'content. That is the shape of desktop infrastructure — a window manager, '
        'compositor, panel or the desktop backdrop — not of an application window '
        'that can receive user input. Keystrokes and clicks addressed here are '
        'delivered nowhere, and the window would return an empty tree.{} Pass one '
        'of those names, or use AX mode=observe with observation_target=apps.'.format(
            query, name, listing))


def resolve_app(query):
    """按名字/标题/pid 找**能承载用户输入的应用窗口**，找不到时重试几次再宣告失败。

    契约刻意比"找到一个有顶层窗口的进程"窄一档。窗口管理器、合成器、面板、
    桌面本身各自都挂着一个顶层窗口，旧契约下它们都能被解析成"应用"，动作工具
    再无条件接受——实测有 agent 因此对桌面反复 press_key 254 次。解析到这类
    窗口时明确拒绝并列出候选，见 infrastructure_app_error。

    实测（LibreOffice 7.3）：`get_app_state` 刚成功，紧接着同一会话里的 `click`
    就报 `appNotFound("soffice")`，而 AT-SPI 桌面里那一条自始至终都在。
    三次失败都发生在**会开关对话框的点击之后**——应用正忙于重建窗口时，
    枚举链路上任何一环（`Atspi.get_desktop`、`get_child_count`、`get_name`）
    瞬时读失败，都会被 `safe()` 吞成"这个应用不存在"。

    没能钉死具体是哪一环（空名字与桌面条目消失两种猜想都实测证伪了），
    但无论哪种瞬时故障，处置都一样：**"暂时读不到"不等于"不存在"**。
    直接抛 appNotFound 是在向 agent 谎报事实——它会据此改用别的应用名、
    重启应用，甚至判定任务无法完成，而真相只是需要再读一次。

    重试上限很小：应用真的不存在时不该让调用方多等。
    """
    from .ax_actions import RESOLVE_APP_ATTEMPTS, RESOLVE_APP_RETRY_SECONDS

    # 空查询 = "现在前台是什么"。这是 agent 真正想问的第一个问题，
    # 让它一次调用就能问出来，而不是先 list_apps 再猜名字。
    if not str(query or "").strip():
        app = foreground_app()
        if app is not None:
            # 前台那个也可能是桌面本身：X11 的活动窗口在没有应用获得焦点时
            # 就是桌面 / 面板。返回它等于把"你面前什么都没有"说成"你面前是个应用"。
            if not accepts_user_input(app):
                raise infrastructure_app_error(app, "(foreground)")
            return app
        raise RuntimeError(
            "No app was given and the foreground window could not be attributed to "
            "any application on the accessibility bus. Pass an app name, or call "
            "list_apps to see what is running.")
    last_error = None
    # 匹配上了、但只有基础设施窗口的应用。**不当作匹配，也不当作没找到**：
    # 继续找别的候选，全都找不到时才拿它来解释拒绝的理由——那句解释
    # 比一句 appNotFound 有用得多。
    infrastructure = None
    incomplete_inventory = False
    for attempt in range(RESOLVE_APP_ATTEMPTS):
        try:
            matches = []
            inventory = iter_apps()
            incomplete_inventory = not inventory_complete(inventory)
            for app in inventory:
                if matches_app_bootstrap_query(app, query):
                    if accepts_user_input(app):
                        matches.append(app)
                        continue
                    infrastructure = app
            if incomplete_inventory:
                last_error = RuntimeError(
                    "accessibility application inventory was incomplete"
                )
                if attempt + 1 < RESOLVE_APP_ATTEMPTS:
                    time.sleep(RESOLVE_APP_RETRY_SECONDS)
                    continue
                break
            if len(matches) == 1:
                return matches[0]
            if len(matches) > 1:
                labels = sorted({
                    node_name(app) or "(unnamed application)"
                    for app in matches
                })
                raise OperationFailure(
                    'appTargetAmbiguous("{}"): {} input-capable accessibility '
                    'applications match this bootstrap query: {}. No action was '
                    'attempted. Observe applications and use app_ref.'.format(
                        query, len(matches), ", ".join(labels[:10])),
                    failure_code="ax_app_ambiguous",
                    failure_phase="precondition",
                    input_events_emitted=False,
                )
            last_error = None
        except Exception as error:      # 枚举本身炸了也算这一轮没找到
            if isinstance(error, OperationFailure):
                raise
            last_error = error
        if attempt + 1 < RESOLVE_APP_ATTEMPTS:
            time.sleep(RESOLVE_APP_RETRY_SECONDS)
    if infrastructure is not None:
        raise infrastructure_app_error(infrastructure, query)
    if last_error is not None:
        raise OperationFailure(
            'accessibilityChannelUnavailable("{}") after {} attempts: the accessibility '
            'bus could not enumerate the requested application (last error: {}). This '
            'does not prove the application is closed or absent from the visual desktop. '
            'No action was attempted; inspect gui with actions=[] and retry accessibility '
            'after the window settles.'.format(
                query, RESOLVE_APP_ATTEMPTS, last_error),
            failure_code="ax_app_unavailable",
            failure_phase="precondition",
            input_events_emitted=False,
            provider_reset_required=True,
        )
    # 失败时**把在跑的应用列出来**。
    #
    # 轨迹证据：13 条里 12 条的开局是 `list_apps → get_app_state`——agent 不
    # 敢直接猜应用名，于是每道题都先花一步去列。把候选写进错误里，猜错的代价
    # 就从"再来一轮"降为"这一轮就知道该叫什么"，那一步开销可以省掉。
    #
    # 一条死路错误和一条带候选的错误，对 agent 是两件完全不同的事。
    running = input_capable_app_labels()
    if running:
        raise OperationFailure(
            'appNotVisibleToAccessibility("{}"): no current accessibility application '
            'matched this query. This does not prove the application is not running or '
            'visible in GUI, and no action was attempted. Applications currently visible to the '
            'accessibility bus, with their process names in parentheses: {}. '
            'Names are matched case- and separator-insensitively against the '
            'application display name and process name, so both '
            '"google-chrome" and "file-roller" resolve.'.format(
                query, ", ".join(sorted(set(running))[:20])),
            failure_code="ax_app_unavailable",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    raise OperationFailure(
        'appNotVisibleToAccessibility("{}"): no input-capable application is currently '
        'visible on the accessibility bus. This does not prove the application is not '
        'running or visible in GUI, and no action was attempted. Inspect gui with '
        'actions=[] before deciding the application is absent.'.format(query),
        failure_code="ax_app_unavailable",
        failure_phase="precondition",
        input_events_emitted=False,
    )


def resolve_app_identity(identity):
    from .ax_actions import RESOLVE_APP_ATTEMPTS, RESOLVE_APP_RETRY_SECONDS

    provider_bus = str((identity or {}).get("providerBusName") or "").strip()
    generation = str((identity or {}).get("processGeneration") or "").strip()
    if not provider_bus or not generation:
        raise OperationFailure(
            "invalid_app_ref: the server-owned application identity is incomplete; "
            "no action was attempted.",
            failure_code="invalid_target",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    final_complete = False
    for attempt in range(RESOLVE_APP_ATTEMPTS):
        try:
            inventory = iter_apps()
        except Exception:
            final_complete = False
            if attempt + 1 < RESOLVE_APP_ATTEMPTS:
                time.sleep(RESOLVE_APP_RETRY_SECONDS)
                continue
            break
        final_complete = inventory_complete(inventory)
        matches = []
        for app in inventory:
            if (
                provider_bus_name(app) == provider_bus
                and process_generation(node_pid(app)) == generation
            ):
                matches.append(app)
        if len(matches) == 1:
            if not accepts_user_input(matches[0]):
                raise infrastructure_app_error(matches[0], "app_ref")
            return matches[0]
        if len(matches) > 1:
            raise OperationFailure(
                "ambiguous_app_ref: the exact provider identity resolved to multiple "
                "applications; no action was attempted.",
                failure_code="ambiguous_target",
                failure_phase="precondition",
                input_events_emitted=False,
            )
        if attempt + 1 < RESOLVE_APP_ATTEMPTS:
            time.sleep(RESOLVE_APP_RETRY_SECONDS)
    if not final_complete:
        raise OperationFailure(
            "observation_incomplete: the accessibility application inventory "
            "could not be read completely after bounded readiness retries; no "
            "action was attempted.",
            failure_code="observation_incomplete",
            failure_phase="precondition",
            input_events_emitted=False,
            failure_subject="app",
            provider_reset_required=True,
        )
    raise OperationFailure(
        "stale_app_ref: the application process generation no longer exists; "
        "no action was attempted. Observe applications again.",
        failure_code="stale_app_ref",
        failure_phase="precondition",
        input_events_emitted=False,
        failure_subject="app",
    )


def resolve_operation_app(operation):
    identity = operation.get("app_identity")
    if identity is not None:
        return resolve_app_identity(identity)
    return resolve_app(operation.get("app", ""))
