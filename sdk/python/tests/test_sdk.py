"""SDK tests against a running daemon.

computer-use's Go test TestPythonSDKAgainstDaemon starts a daemon with a
fake desktop and runs this file with HCU_SOCKET set. Run it by hand against
any daemon the same way.
"""

import asyncio
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import hcu_sdk as hcu  # noqa: E402
from hcu_sdk import ptc  # noqa: E402
from test_ptc import PTCAdapterTests, PTCMessagesTests  # noqa: E402,F401


def recording_transport(calls):
    def transport(method, params):
        calls.append((method, params))
        return {"ok": True, "op": "test", "timing_ms": {}}
    return transport


class SDKAgainstDaemon(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.desktop = hcu.Desktop(hcu.SocketTransport(path=os.environ["HCU_SOCKET"], session="sdk", start=False))
        cls.schema = cls.desktop.schema()

    @classmethod
    def tearDownClass(cls):
        cls.desktop.close()

    def test_every_helper_builds_a_call_the_core_accepts(self):
        # Drift guard: each helper's arguments go through the core's own
        # decoder and published schema; none may come back invalid_argument.
        calls = []
        builder = hcu.Desktop(recording_transport(calls))
        builder.apps()
        builder.observe("w1", root="focused", scope="content", observe="diff")
        builder.wait({"window": "Saved", "timeout_ms": 1})
        builder.wait({"ref": "r1", "state": "checked"}, window_ref="w1")
        builder.find("w1", role="button", name_has="Sa", text="x", state="checked", limit=5)
        builder.find(cursor="f1", limit=5)
        builder.read(["r1", "r2"])
        builder.act([{"type": "click", "ref": "r1"}, {"type": "wait", "ref": "r1", "timeout_ms": 1}], window_ref="w1")
        builder.click("r1", wait={"ref": "r1", "state": "checked", "timeout_ms": 1})
        builder.type("hello", ref="r2")
        builder.type("hello")
        builder.key("ctrl+s")
        builder.set_value("r2", "42")
        builder.scroll("r2", "down", pages=2)
        builder.activate("w1")
        builder.activate("Sheet")
        builder.close_window("w9")
        builder.screenshot()
        builder.gui([{"type": "click", "x": 1, "y": 2}], settle_ms=0)
        self.assertEqual(len(calls), 19)
        for _method, params in calls:
            raw = self.desktop.transport("call", dict(params, request_id=None))
            self.assertNotEqual(raw.get("code"), "invalid_argument",
                                "%s rejected: %s" % (json.dumps(params["args"]), raw.get("message")))

    def test_errors_are_classes_named_by_code(self):
        for code in self.schema.errors:
            cls = getattr(hcu, hcu._class_name(code))
            self.assertIs(cls, hcu.error_class(code))
            self.assertEqual(cls.code, code)
            self.assertTrue(issubclass(cls, hcu.HcuError))
        with self.assertRaises(hcu.StaleRef) as caught:
            self.desktop.click("r9999")
        self.assertFalse(caught.exception.input_emitted)
        self.assertIn("hint", str(caught.exception))
        with self.assertRaises(hcu.InvalidArgument) as caught:
            self.desktop.call("AX", {"mode": "act", "actions": [{"action": "click", "ref": "r1"}]})
        self.assertEqual(caught.exception.path, "actions[0].action")

    def test_records_flow_refs_and_paging(self):
        apps = self.desktop.apps()
        self.assertEqual(apps.windows[0].ref, "w1")
        tree = self.desktop.observe("w1")
        self.assertTrue(tree.snapshot_id)
        button = next(e for e in tree.elements if e.role == "button" and e.ref)
        matches = list(self.desktop.find_all("w1", page_size=1, role="button"))
        self.assertIn(button.ref, [m.ref for m in matches])
        read = self.desktop.read([button.ref])
        self.assertTrue(read.read[0].ok)
        again = self.desktop.observe("w1", since=tree.snapshot_id)
        self.assertTrue(again.diff.unchanged)
        acted = self.desktop.click(button.ref)
        self.assertTrue(acted.ok and acted.request_id and acted.input_emitted)
        self.assertIn("provider", acted.timing_ms)

    def test_request_id_is_reused_on_retry(self):
        tree = self.desktop.observe("w1")
        button = next(e for e in tree.elements if e.role == "button" and e.ref)
        first = self.desktop.click(button.ref, request_id="sdk-retry-1")
        second = self.desktop.click(button.ref, request_id="sdk-retry-1")
        self.assertFalse(first.replayed)
        self.assertTrue(second.replayed)

    def test_reconnect_after_a_dropped_connection(self):
        desktop = hcu.Desktop(hcu.SocketTransport(path=os.environ["HCU_SOCKET"], session="sdk-drop", start=False))
        desktop.apps()
        desktop.transport._sock.close()  # the next send fails
        self.assertTrue(desktop.apps().ok)
        desktop.close()

    def test_async_desktop_over_the_socket(self):
        async def main():
            desktop = hcu.AsyncDesktop(hcu.SocketTransport(path=os.environ["HCU_SOCKET"], session="sdk-async", start=False))
            apps = await desktop.apps()
            matches = await desktop.find_all("w1", page_size=1, role="button")
            return apps, matches
        apps, matches = asyncio.run(main())
        self.assertTrue(apps.ok and matches)

    def test_ptc_definitions_and_forwarder(self):
        tools = ptc.tool_definitions(self.schema)
        by_name = {tool["name"]: tool for tool in tools}
        self.assertEqual(set(by_name), {"hcu_ax", "hcu_gui"})
        self.assertEqual(by_name["hcu_ax"]["allowed_callers"], [ptc.CODE_EXECUTION_CALLER])
        self.assertNotIn("allowed_callers", by_name["hcu_gui"])
        self.assertNotIn("$ref", json.dumps(tools))
        self.assertNotIn("$defs", json.dumps(tools))
        for tool in tools:
            self.assertLess(len(json.dumps(tool["input_schema"])), 16000)

        # A synthetic code-execution tool_use block, forwarded to the daemon.
        block = {"type": "tool_use", "id": "toolu_01", "name": "hcu_ax",
                 "input": {"mode": "find", "window_ref": "w1", "query": {"role": "button"}, "limit": 1},
                 "caller": {"type": ptc.CODE_EXECUTION_CALLER, "tool_id": "srvtoolu_01"}}
        self.desktop.apps()
        result = ptc.forward(block, self.desktop)
        self.assertEqual(result["tool_use_id"], "toolu_01")
        record = json.loads(result["content"])
        self.assertTrue(record["ok"], record)
        self.assertEqual(record["page"]["returned"], 1)

    def test_ptc_container_script_end_to_end(self):
        # The container side: async tool functions that the harness answers
        # by forwarding each tool_use to the daemon.
        counter = {"n": 0}

        def make(name):
            async def tool(arguments):
                counter["n"] += 1
                block = {"type": "tool_use", "id": "toolu_%d" % counter["n"], "name": name, "input": arguments,
                         "caller": {"type": ptc.CODE_EXECUTION_CALLER}}
                return ptc.forward(block, self.desktop)["content"]
            return tool

        hcu_ax, hcu_gui = make("hcu_ax"), make("hcu_gui")

        async def container():
            d = hcu.AsyncDesktop(hcu.ptc_transport(hcu_ax, hcu_gui))
            await d.apps()
            buttons = await d.find_all("w1", page_size=1, role="button")
            acted = await d.click(buttons[0].ref)
            return buttons, acted

        buttons, acted = asyncio.run(container())
        self.assertTrue(buttons and acted.ok and acted.request_id)
        self.assertGreaterEqual(counter["n"], 3)

    def test_ptc_event_replay_against_daemon(self):
        forwarder = ptc.Forwarder(self.desktop)
        self.desktop.apps()
        tree = self.desktop.observe("w1")
        button = next(e for e in tree.elements if e.role == "button" and e.ref)
        block = {"type": "tool_use", "id": "toolu_live_repeat", "name": "hcu_ax",
                 "input": {"mode": "act", "actions": [{"type": "click", "ref": button.ref}]}}
        first = forwarder(block)
        repeated = forwarder(block)
        record = json.loads(first["content"])
        self.assertEqual(first, repeated)
        self.assertTrue(record["ok"] and record["request_id"].startswith("ptc-"))
        # The shared daemon can recover the same input by its generated id too.
        raw = self.desktop.call("AX", block["input"], request_id=record["request_id"])
        self.assertTrue(raw.replayed)


if __name__ == "__main__":
    unittest.main(verbosity=1)
