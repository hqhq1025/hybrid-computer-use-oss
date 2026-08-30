import importlib.util
import json
import pathlib
import unittest


PATH = pathlib.Path(__file__).with_name("convert.py")
SPEC = importlib.util.spec_from_file_location("legacy_trace_converter", PATH)
CONVERTER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CONVERTER)
FIXTURES = PATH.with_name("testdata")


class LegacyTraceConverterTests(unittest.TestCase):
    def test_observe_conversion(self):
        request = CONVERTER.convert_operation(
            {
                "tool": "get_app_state",
                "app": "Editor",
                "targetWindowHandle": "w2:fixture",
                "max_tree_nodes": 1200,
            },
            "req-1",
        )
        self.assertEqual(request["operation"], "observe")
        self.assertEqual(request["observe"]["protocol_version"], 2)
        self.assertEqual(request["observe"]["window_handle"], "w2:fixture")

    def test_action_conversion(self):
        request = CONVERTER.convert_operation(
            {
                "tool": "click",
                "app": "Editor",
                "semanticTarget": {
                    "selector": 'button "Save"',
                    "role": "button",
                    "name": "Save",
                    "hasName": True,
                },
                "click_count": 1,
            },
            "req-2",
        )
        self.assertEqual(request["operation"], "resolve_and_act")
        self.assertEqual(request["action"]["intent"], "click")
        self.assertEqual(request["action"]["input"]["click_count"], 1)

    def test_unknown_record_fails(self):
        with self.assertRaises(ValueError):
            CONVERTER.convert_record({"response": {"ok": True}}, 1)

    def test_historical_request_corpus_converts_to_v2(self):
        manifest = json.loads((FIXTURES / "manifest.json").read_text())
        expected_operations = {
            "accessibility-observe": "observe",
            "accessibility-action": "resolve_and_act",
            "accessibility-find-sample": "observe",
            "accessibility-assert-sample": "observe",
            "accessibility-keyboard": "resolve_and_act",
            "desktop-gui": "desktop_execute_batch",
            "desktop-full-batch": "desktop_execute_batch",
            "window-list": "window_list",
            "window-activate": "window_action",
            "window-close": "window_action",
        }
        request_fixtures = [
            fixture
            for fixture in manifest["fixtures"]
            if fixture["direction"] == "request"
        ]
        self.assertEqual(
            {fixture["name"] for fixture in request_fixtures},
            set(expected_operations),
        )
        for sequence, fixture in enumerate(request_fixtures, 1):
            with self.subTest(fixture=fixture["name"]):
                operation = json.loads(
                    (FIXTURES / fixture["file"]).read_text()
                )
                converted = CONVERTER.convert_operation(
                    operation, "fixture-{}".format(sequence)
                )
                self.assertEqual(converted["protocol"], "osw-provider-v2")
                self.assertEqual(converted["schema_version"], 2)
                self.assertEqual(
                    converted["operation"],
                    expected_operations[fixture["name"]],
                )


if __name__ == "__main__":
    unittest.main()
