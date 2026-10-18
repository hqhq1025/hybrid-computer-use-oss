#!/usr/bin/env python3
"""Event-driven dirty bit, tree reuse, and event-driven settle wait tests.

The fakes are deterministic: a fake clock advances only when code sleeps,
and fake events are delivered by pump() once the clock reaches their time.
"""

from runtime_test_support import *  # noqa: F401,F403


class _ScheduledMonitor(ui_events.EventMonitor):
    """EventMonitor whose pump() delivers scheduled events from a fake clock.

    ``schedule`` holds ``(time, bus)`` pairs. ``undrained`` makes the next
    pump report a backlog it could not drain.
    """

    def __init__(self, clock, schedule=(), registered_at=-10.0):
        super().__init__(clock=clock.monotonic, sleep=clock.sleep)
        self.fake_clock = clock
        self.schedule = sorted(schedule)
        self.active = True
        self.registered_at = registered_at
        self.undrained = False

    def pump(self, budget_seconds=ui_events.PUMP_BUDGET_SECONDS):
        if self.undrained:
            self.undrained = False
            self._global_generation += 1
            return False
        while self.schedule and self.schedule[0][0] <= self.fake_clock.now:
            _at, bus = self.schedule.pop(0)
            self.record(bus)
        return True


class EventMonitorTests(unittest.TestCase):
    def test_event_from_one_app_does_not_dirty_another(self):
        clock = _FakeClock()
        monitor = _ScheduledMonitor(clock)
        calc = monitor.mark(":1.26")
        gimp = monitor.mark(":1.77")
        monitor.record(":1.26")

        self.assertFalse(monitor.unchanged_since(":1.26", calc))
        self.assertTrue(monitor.unchanged_since(":1.77", gimp))

    def test_event_with_unknown_source_dirties_every_app(self):
        clock = _FakeClock()
        monitor = _ScheduledMonitor(clock)
        token = monitor.mark(":1.77")
        monitor.record(None)

        self.assertFalse(monitor.unchanged_since(":1.77", token))

    def test_undrained_backlog_dirties_every_app(self):
        clock = _FakeClock()
        monitor = _ScheduledMonitor(clock)
        token = monitor.mark(":1.77")
        monitor.undrained = True

        self.assertFalse(monitor.pump())
        self.assertFalse(monitor.unchanged_since(":1.77", token))

    def test_inactive_monitor_never_reports_unchanged(self):
        monitor = ui_events.EventMonitor()
        self.assertFalse(monitor.unchanged_since(":1.26", monitor.mark(":1.26")))

    def test_wait_for_event_returns_at_the_event(self):
        clock = _FakeClock()
        monitor = _ScheduledMonitor(clock, [(0.03, ":1.26")])

        self.assertTrue(monitor.wait_for_event(":1.26", 0.5))
        self.assertLess(clock.now, 0.04)

    def test_wait_for_event_times_out_without_events(self):
        clock = _FakeClock()
        monitor = _ScheduledMonitor(clock, [(0.03, ":1.77")])

        self.assertFalse(monitor.wait_for_event(":1.26", 0.2))
        self.assertAlmostEqual(clock.now, 0.2, places=6)


class TreeReuseCacheTests(unittest.TestCase):
    """A stored tree is reused only when every check passes."""

    BUS = ":1.26"
    KEY = ("bus", "/window", 0)
    PRINT = ("bounds", "title", 3, ())
    REFS = {"0.1": {"index": 4, "role": "push button", "name": "OK"}}

    def _cache(self, schedule=(), registered_at=-10.0):
        clock = _FakeClock()
        monitor = _ScheduledMonitor(clock, schedule, registered_at)
        # The application has delivered events before, so its events are
        # known to reach the listener.
        monitor.record(self.BUS)
        return clock, monitor, tree_reuse.TreeReuseCache(
            monitor, clock=clock.monotonic, max_age=10.0)

    def _store(self, cache, known=None):
        indexer = tree_reuse.RecordingIndexer(known or self.REFS)
        indexer.index_for([0, 1], "push button", "OK")
        indexer.index_for([0, 2], "push button", "Cancel")
        walk = cache.begin_walk(self.BUS)
        cache.store(self.KEY, self.PRINT, walk, indexer.calls, "tree")
        return walk

    def test_unchanged_window_reuses_the_tree(self):
        clock, _monitor, cache = self._cache()
        self._store(cache)
        clock.sleep(2.0)

        self.assertEqual(
            cache.lookup(self.BUS, self.KEY, self.PRINT, self.REFS), "tree")

    def test_event_after_the_walk_began_forces_a_walk(self):
        clock, _monitor, cache = self._cache([(1.0, BUS_CALC)])
        self._store(cache)
        clock.sleep(2.0)

        self.assertIsNone(
            cache.lookup(self.BUS, self.KEY, self.PRINT, self.REFS))

    def test_change_without_event_is_caught_by_the_fingerprint(self):
        """LibreOffice emits no event when its window is resized."""
        clock, _monitor, cache = self._cache()
        self._store(cache)
        clock.sleep(1.0)
        resized = ("other bounds",) + self.PRINT[1:]

        self.assertIsNone(cache.lookup(self.BUS, self.KEY, resized, self.REFS))

    def test_old_tree_is_not_reused(self):
        clock, _monitor, cache = self._cache()
        self._store(cache)
        clock.sleep(10.5)

        self.assertIsNone(
            cache.lookup(self.BUS, self.KEY, self.PRINT, self.REFS))

    def test_walk_inside_the_registration_grace_is_not_reused(self):
        clock, _monitor, cache = self._cache(registered_at=0.0)
        self._store(cache)
        clock.sleep(2.0)

        self.assertIsNone(
            cache.lookup(self.BUS, self.KEY, self.PRINT, self.REFS))

    def test_app_that_never_emitted_an_event_is_not_trusted(self):
        clock = _FakeClock()
        monitor = _ScheduledMonitor(clock)
        cache = tree_reuse.TreeReuseCache(monitor, clock=clock.monotonic)
        self._store(cache)
        clock.sleep(1.0)

        self.assertIsNone(
            cache.lookup(self.BUS, self.KEY, self.PRINT, self.REFS))

    def test_different_known_refs_force_a_walk(self):
        """Indices depend on the caller's refs; a mismatch must not be reused."""
        clock, _monitor, cache = self._cache()
        self._store(cache)
        clock.sleep(1.0)
        other = {"0.1": {"index": 9, "role": "push button", "name": "OK"}}

        self.assertIsNone(cache.lookup(self.BUS, self.KEY, self.PRINT, other))

    def test_walk_that_began_with_an_undrained_backlog_is_not_stored(self):
        clock, monitor, cache = self._cache()
        monitor.undrained = True
        self._store(cache)
        clock.sleep(1.0)

        self.assertIsNone(
            cache.lookup(self.BUS, self.KEY, self.PRINT, self.REFS))

    def test_undrained_backlog_at_lookup_forces_a_walk(self):
        clock, monitor, cache = self._cache()
        self._store(cache)
        clock.sleep(1.0)
        monitor.undrained = True

        self.assertIsNone(
            cache.lookup(self.BUS, self.KEY, self.PRINT, self.REFS))

    def test_inactive_monitor_stores_nothing(self):
        clock = _FakeClock()
        monitor = ui_events.EventMonitor(clock=clock.monotonic)
        cache = tree_reuse.TreeReuseCache(monitor, clock=clock.monotonic)
        cache.store(self.KEY, self.PRINT, (monitor.mark(self.BUS), 0.0),
                    [], "tree")

        self.assertIsNone(
            cache.lookup(self.BUS, self.KEY, self.PRINT, self.REFS))

    def test_walk_cut_by_time_or_read_errors_is_not_reusable(self):
        def omission(reason):
            return [{"kind": "provider_omission", "reason": reason}]

        self.assertTrue(runtime.walk_is_reusable(omission("filtered,budget")))
        self.assertFalse(runtime.walk_is_reusable(omission("filtered,time")))
        self.assertFalse(
            runtime.walk_is_reusable(omission("provider_probe_error")))


BUS_CALC = TreeReuseCacheTests.BUS


class _BusApp(_ScriptedApp):
    class _App:
        bus_name = ":1.26"

    app = _App()


class EventSettleTests(AtspiPatchedTestCase):
    """Settle wait driven by application events instead of a fixed sleep."""

    def _wait(self, app, before, clock, schedule):
        monitor = _ScheduledMonitor(clock, schedule)
        return runtime.wait_for_ui_to_settle(
            app, before, clock=clock.monotonic, sleep=clock.sleep,
            monitor=monitor)

    def _app(self, clock, timeline=None):
        main = _WindowNode("frame", "Calc", active=True)
        return _BusApp(clock, timeline or [(0.0, [main])])

    def test_app_without_events_waits_the_old_minimum(self):
        clock = _FakeClock()
        app = self._app(clock)
        before = runtime.window_identity_set(app)

        self.assertIsNone(self._wait(app, before, clock, []))
        self.assertAlmostEqual(clock.now, runtime.SETTLE_MIN_SECONDS, places=3)

    def test_short_burst_ends_the_wait_at_the_floor(self):
        clock = _FakeClock()
        app = self._app(clock)
        before = runtime.window_identity_set(app)
        burst = [(0.005, ":1.26"), (0.006, ":1.26"), (0.010, ":1.26")]

        self.assertIsNone(self._wait(app, before, clock, burst))
        self.assertGreaterEqual(clock.now, runtime.SETTLE_EVENT_FLOOR_SECONDS)
        self.assertLess(clock.now, runtime.SETTLE_MIN_SECONDS)

    def test_continuing_events_are_capped_at_the_old_minimum(self):
        clock = _FakeClock()
        app = self._app(clock)
        before = runtime.window_identity_set(app)
        stream = [(0.01 * step, ":1.26") for step in range(1, 40)]

        self.assertIsNone(self._wait(app, before, clock, stream))
        self.assertLess(
            clock.now,
            runtime.SETTLE_MIN_SECONDS + ui_events.WAIT_STEP_SECONDS + 1e-6)

    def test_other_apps_events_do_not_shorten_the_wait(self):
        clock = _FakeClock()
        app = self._app(clock)
        before = runtime.window_identity_set(app)

        self.assertIsNone(
            self._wait(app, before, clock, [(0.005, ":1.77")]))
        self.assertAlmostEqual(clock.now, runtime.SETTLE_MIN_SECONDS, places=3)

    def test_new_window_still_waits_for_focus(self):
        """The window-change guarantee is unchanged by the event wait."""
        clock = _FakeClock()
        main_active = _WindowNode("frame", "Calc", active=True)
        main_idle = _WindowNode("frame", "Calc")
        dialog_idle = _WindowNode("dialog", "Format Cells")
        dialog_active = _WindowNode("dialog", "Format Cells", active=True)
        app = self._app(clock, [
            (0.0, [main_active]),
            (0.030, [main_active, dialog_idle]),
            (0.300, [main_idle, dialog_active]),
        ])
        before = runtime.window_identity_set(app)
        burst = [(0.010 + 0.005 * step, ":1.26") for step in range(5)]

        self.assertIsNone(self._wait(app, before, clock, burst))
        self.assertGreaterEqual(clock.now, 0.300)
        _, chosen = runtime.main_window(app)
        self.assertEqual(runtime.node_name(chosen), "Format Cells")

    def test_window_that_never_takes_focus_still_reports_the_timeout(self):
        clock = _FakeClock()
        main = _WindowNode("frame", "Calc", active=True)
        tooltip = _WindowNode("window", "")
        app = self._app(clock, [(0.0, [main]), (0.02, [main, tooltip])])
        before = runtime.window_identity_set(app)

        note = self._wait(app, before, clock, [(0.01, ":1.26")])

        self.assertIsNotNone(note)
        self.assertIn("target ref", note)
        self.assertLess(
            clock.now,
            runtime.SETTLE_MIN_SECONDS
            + runtime.SETTLE_FOCUS_TIMEOUT_SECONDS + 0.05)


if __name__ == "__main__":
    unittest.main()
