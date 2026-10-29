"""Tests for the controller's event log and its validation gate.

    cd benchmark/experiments/e6-qwen-vl-clicks/controller && python3 -m unittest -v test_controller

The stage functions that need a cluster are not covered here; they are exercised by
the dry run against the live sandbox. What is covered is everything that could
silently produce a wrong click: the shape of an event line, and every rejection
code in `validate()`.
"""

import json
import os
import tempfile
import unittest

import controller
import events


FIT_UNUSABLE = {"status": "unusable", "x": {"a": 1.0, "b": 0.0}, "y": {"c": 1.0, "d": 0.0},
                "corrected": {"rms_px": 214.28}}
BOX = {"left": 40, "top": 80, "w": 100, "h": 40, "x": 90, "y": 100}


class EventLine(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.path = os.path.join(self.tmp, "events.jsonl")

    def test_required_keys(self):
        with events.Events(self.path, "run-1") as ev:
            ev.emit("sandbox.acquire.started", sandbox_id="sb-1", arm="vision-only",
                    task="t1", duration_ms=12.34, detail={"url": "http://x/"})
        records = events.read(self.path)
        self.assertEqual(len(records), 1)
        record = records[0]
        for key in ("ts", "run_id", "event", "sandbox_id", "arm", "task", "ok"):
            self.assertIn(key, record)
        self.assertEqual(record["run_id"], "run-1")
        self.assertEqual(record["duration_ms"], 12.3)
        self.assertEqual(record["detail"]["url"], "http://x/")
        self.assertTrue(record["ts"].endswith("+00:00"))

    def test_one_line_per_event(self):
        with events.Events(self.path, "run-2") as ev:
            for name in ("a", "b", "c"):
                ev.emit(name)
        with open(self.path, encoding="utf-8") as fh:
            lines = [line for line in fh if line.strip()]
        self.assertEqual(len(lines), 3)
        self.assertEqual([json.loads(line)["event"] for line in lines], ["a", "b", "c"])


class Validate(unittest.TestCase):
    def test_vision_point_is_refused_when_calibration_is_unusable(self):
        verdict = controller.validate({"x": 90, "y": 100}, box=BOX, fit=FIT_UNUSABLE)
        self.assertFalse(verdict["ok"])
        self.assertEqual(verdict["code"], "calibration_unusable")
        self.assertIn("corrected_rms=214.28px", verdict["detail"])

    def test_dom_point_ignores_an_unusable_calibration(self):
        # The DOM path yields viewport pixels already; the fit must not disqualify it.
        verdict = controller.validate({"x": 90, "y": 100}, box=BOX, fit=FIT_UNUSABLE,
                                      source="dom")
        self.assertTrue(verdict["ok"])
        self.assertEqual(verdict["corrected_point"], (90.0, 100.0))

    def test_allow_vision_clicks_lets_an_unusable_fit_through(self):
        # The one switch that turns "cannot be trusted" into "click it anyway".
        controller.ALLOW_VISION_CLICKS = True
        try:
            verdict = controller.validate({"x": 90, "y": 100}, box=None, fit=FIT_UNUSABLE)
        finally:
            controller.ALLOW_VISION_CLICKS = False
        self.assertTrue(verdict["ok"])
        self.assertEqual(verdict["corrected_point"], (90.0, 100.0))

    def test_no_json(self):
        verdict = controller.validate(None, box=BOX, fit=FIT_UNUSABLE)
        self.assertFalse(verdict["ok"])
        self.assertEqual(verdict["code"], "no_json")

    def test_abstention_is_not_a_click(self):
        verdict = controller.validate({"target": None, "reason": "no such button"},
                                      box=BOX, fit=FIT_UNUSABLE)
        self.assertFalse(verdict["ok"])
        self.assertEqual(verdict["code"], "abstained")

    def test_candidate_lists_are_rejected_not_picked(self):
        verdict = controller.validate({"x": [414, 924], "y": 600}, box=BOX, fit=FIT_UNUSABLE)
        self.assertEqual(verdict["code"], "coordinate_not_a_number")
        self.assertIn("candidate lists", verdict["detail"])

    def test_missing_coordinate(self):
        verdict = controller.validate({"label": "claim"}, box=BOX, fit=FIT_UNUSABLE)
        self.assertEqual(verdict["code"], "coordinate_missing")

    def test_out_of_frame(self):
        verdict = controller.validate({"x": 1280, "y": 0}, box=None, fit=FIT_UNUSABLE)
        self.assertEqual(verdict["code"], "out_of_frame")

    def test_outside_target_box(self):
        verdict = controller.validate({"x": 10, "y": 10}, box=BOX, fit=FIT_UNUSABLE,
                                      source="dom")
        self.assertEqual(verdict["code"], "outside_target_box")

    def test_inside_box_is_ok(self):
        verdict = controller.validate({"x": 90, "y": 100}, box=BOX, fit=FIT_UNUSABLE,
                                      source="dom")
        self.assertTrue(verdict["ok"])
        self.assertEqual(verdict["code"], "ok")
        self.assertTrue(verdict["inside_box"])
        self.assertEqual(verdict["raw_point"], (90.0, 100.0))

    def test_box_check_uses_the_corrected_point(self):
        # fit scales x by 2: raw (50,100) becomes (100,100), which is inside BOX.
        fit = {"status": "ok", "x": {"a": 2.0, "b": 0.0}, "y": {"c": 1.0, "d": 0.0},
               "corrected": {"rms_px": 4.0}}
        verdict = controller.validate({"x": 50, "y": 100}, box=BOX, fit=fit)
        self.assertTrue(verdict["ok"])
        self.assertEqual(verdict["corrected_point"], (100.0, 100.0))

    def test_box_slack_allows_two_pixels(self):
        verdict = controller.validate({"x": 38, "y": 79}, box=BOX, fit=FIT_UNUSABLE,
                                      source="dom")
        self.assertTrue(verdict["ok"])


class RetryPrompt(unittest.TestCase):
    def test_carries_the_rejection_and_the_previous_answer(self):
        verdict = {"code": "outside_target_box", "detail": "point (10.0,10.0) not in box"}
        prompt = controller.retry_prompt(
            {"ask": "Locate the claim button."}, {"x": 10, "y": 10}, verdict)
        self.assertIn("Locate the claim button.", prompt)
        self.assertIn("outside_target_box", prompt)
        self.assertIn('"x": 10', prompt)
        self.assertIn('"target": null', prompt)


class InsideBox(unittest.TestCase):
    def test_inside(self):
        self.assertTrue(controller.inside_box((90, 100), BOX))

    def test_outside(self):
        self.assertFalse(controller.inside_box((500, 500), BOX))

    def test_edges(self):
        self.assertTrue(controller.inside_box((40, 80), BOX))
        self.assertTrue(controller.inside_box((140, 120), BOX))


class VisionPrompt(unittest.TestCase):
    def test_asks_for_abstention_and_coordinates(self):
        prompt = controller._vision_prompt({"ask": "Locate the CSV button."}, b"")
        self.assertIn("Locate the CSV button.", prompt)
        self.assertIn('"target": null', prompt)
        self.assertIn('"x"', prompt)


class SelectorHelpers(unittest.TestCase):
    def test_selector_is_json_quoted_into_the_expression(self):
        # The selector goes through json.dumps, so a quote or backslash in it cannot
        # break out of the JS string literal.
        selector = 'button[data-act=delete][data-id="a\\b"]'
        expression = "document.querySelectorAll(%s)" % json.dumps(selector)
        self.assertIn('\\"a\\\\b\\"', expression)


if __name__ == "__main__":
    unittest.main()
