"""Reuse of an unchanged window tree across observe requests.

An observe or find on an unchanged window used to walk the whole tree
again (about 0.5 s on Calc). The reuse rule is conservative. A tree walked
earlier is returned again only if all of these hold:

1. The application emitted no AT-SPI event since just before that walk
   began (ui_events.EventMonitor; events are a dirty bit only).
2. The walk began after the listener had time to reach every application
   (REGISTRATION_GRACE_SECONDS), and the application has delivered at
   least one event to this process, which shows that its events reach
   the listener at all.
3. The request asks for the same window, application process and
   rendering parameters.
4. A cheap fingerprint read fresh for this request is unchanged: the
   selected window's frame, title and child count, and every top-level
   window's role, title, states and frame. This catches changes that
   arrive without events; LibreOffice, for example, emits none when its
   window is resized.
5. The tree is younger than SNAPSHOT_REUSE_MAX_AGE_SECONDS, a backstop
   for applications that change state without emitting events.
6. Assigning element indices with the caller's current known refs gives
   the same indices as the cached walk. Indices depend on the refs the
   caller passes, so the earlier walk's calls to StableIndexer are
   recorded and replayed against the new refs.

When any check fails, the caller walks the tree again. Nothing is ever
updated from event payloads.
"""

import time

from .ax_records import StableIndexer

# Maximum age of a reused tree. Agent turns are about 7 s apart, so this
# lets a find or observe in the next turn reuse the tree of the previous
# action, and it bounds how long a change that emitted no event can stay
# hidden.
SNAPSHOT_REUSE_MAX_AGE_SECONDS = 10.0

# Trees kept at once. Observe and find use different text limits, so they
# need separate entries.
MAX_ENTRIES = 8


class RecordingIndexer(StableIndexer):
    """StableIndexer that records every assignment for later replay."""

    def __init__(self, known):
        super().__init__(known)
        self.calls = []

    def index_for(self, path, role, name, name_hash=""):
        index = super().index_for(path, role, name, name_hash)
        self.calls.append((tuple(path), role, name, name_hash, index))
        return index


def replay_matches(calls, known_refs):
    """Return whether ``known_refs`` reproduces the recorded indices.

    A walk that failed on a node still consumed indices for that node,
    and those calls are recorded too, so the replay sees the same state.
    """
    indexer = StableIndexer(known_refs)
    for path, role, name, name_hash, index in calls:
        if indexer.index_for(list(path), role, name, name_hash) != index:
            return False
    return True


def window_fingerprint(bounds, title, child_count, window_selection):
    """Return the cheap facts that must match before a tree is reused."""
    candidates = tuple(
        (
            candidate.get("index"),
            candidate.get("role"),
            candidate.get("title"),
            candidate.get("active"),
            candidate.get("showing"),
            candidate.get("visible"),
            candidate.get("modal"),
            tuple(sorted((candidate.get("bounds") or {}).items())),
        )
        for candidate in (window_selection or {}).get("candidates", [])
    )
    return (
        tuple(sorted((bounds or {}).items())),
        title,
        child_count,
        candidates,
    )


class TreeEntry:
    __slots__ = (
        "key", "fingerprint", "token", "walked_at", "index_calls", "tree",
    )

    def __init__(self, key, fingerprint, token, walked_at, index_calls, tree):
        self.key = key
        self.fingerprint = fingerprint
        self.token = token
        self.walked_at = walked_at
        self.index_calls = index_calls
        self.tree = tree


class TreeReuseCache:
    """Recent window trees, each with the event token taken before its walk."""

    def __init__(self, monitor, clock=time.monotonic,
                 max_age=SNAPSHOT_REUSE_MAX_AGE_SECONDS):
        self.monitor = monitor
        self.clock = clock
        self.max_age = max_age
        self._entries = {}

    def begin_walk(self, bus):
        """Drain queued events and return a token for a walk, or None.

        None means the backlog could not be drained, so events emitted
        before the walk may still be queued (or dropped by the bus); such a
        walk is never stored for reuse.
        """
        if not self.monitor.pump():
            return None
        return self.monitor.mark(bus), self.clock()

    def lookup(self, bus, key, fingerprint, known_refs):
        """Return the cached tree for ``key`` if every reuse check passes.

        The caller must read the fingerprint before calling, so that the
        pump here dispatches every event the application emitted before it
        answered those reads (D-Bus keeps one sender's messages in order).
        """
        entry = self._entries.get(key)
        if entry is None or not self.monitor.active:
            return None
        if (
            not self.monitor.pump()
            # An application that has never delivered an event to this
            # process gives no evidence that its events reach the listener.
            or self.monitor.last_event_at(bus) is None
            or entry.fingerprint != fingerprint
            or not self.monitor.unchanged_since(bus, entry.token)
            or not self.monitor.trusted_after(entry.walked_at)
            or self.clock() - entry.walked_at > self.max_age
            or not replay_matches(entry.index_calls, known_refs)
        ):
            return None
        return entry.tree

    def store(self, key, fingerprint, walk, index_calls, tree):
        if walk is None or not self.monitor.active:
            return
        token, walked_at = walk
        self._entries.pop(key, None)
        self._entries[key] = TreeEntry(
            key, fingerprint, token, walked_at, list(index_calls), tree)
        while len(self._entries) > MAX_ENTRIES:
            self._entries.pop(next(iter(self._entries)))
