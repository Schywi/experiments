"""Unit tests for the E5 runner's parsing and scoring.

    cd benchmark/experiments/e5-vlm-grounding && python3 -m unittest -v test_runner
"""

import unittest

import runner


TOL = 32


class FirstJsonObject(unittest.TestCase):
    def test_plain_object(self):
        self.assertEqual(runner.first_json_object('{"label": "x", "x": 1, "y": 2}'),
                         {"label": "x", "x": 1, "y": 2})

    def test_fenced_and_narrated(self):
        text = 'Sure. Here it is:\n```json\n{"label": "refresh", "x": 1180, "y": 88}\n```\nDone.'
        self.assertEqual(runner.first_json_object(text)["x"], 1180)

    def test_first_of_two(self):
        text = '{"label": "a", "x": 1, "y": 2} and also {"label": "b"}'
        self.assertEqual(runner.first_json_object(text)["label"], "a")

    def test_braces_inside_strings(self):
        text = '{"label": "the {weird} button", "x": 3, "y": 4}'
        self.assertEqual(runner.first_json_object(text)["label"], "the {weird} button")

    def test_escaped_quote_inside_string(self):
        text = '{"label": "say \\"hi\\"", "x": 3, "y": 4}'
        self.assertEqual(runner.first_json_object(text)["x"], 3)

    def test_none_when_absent(self):
        self.assertIsNone(runner.first_json_object("I cannot see the page."))

    def test_none_when_malformed(self):
        self.assertIsNone(runner.first_json_object('{"label": "x", "x": }'))

    def test_none_when_not_an_object(self):
        self.assertIsNone(runner.first_json_object('["x", 1, 2]'))


class Normalise(unittest.TestCase):
    def test_case_and_punctuation(self):
        self.assertEqual(runner.normalise("Refresh Button"), runner.normalise("refresh-button"))

    def test_none(self):
        self.assertEqual(runner.normalise(None), "")


class AsPoint(unittest.TestCase):
    def test_floats(self):
        self.assertEqual(runner.as_point({"x": "12.5", "y": 3}), (12.5, 3.0))

    def test_missing(self):
        self.assertIsNone(runner.as_point({"label": "x"}))


class Score(unittest.TestCase):
    def test_point_inside_tolerance(self):
        task = {"kind": "point"}
        truth = {"point": {"x": 100, "y": 100}}
        row = runner.score(task, truth, {"label": "a", "x": 120, "y": 100}, TOL)
        self.assertTrue(row["point_ok"])
        self.assertAlmostEqual(row["distance_px"], 20.0)
        self.assertTrue(row["ok"])

    def test_point_outside_tolerance_but_inside_band(self):
        task = {"kind": "point"}
        truth = {"point": {"x": 100, "y": 100}}
        row = runner.score(task, truth, {"label": "a", "x": 150, "y": 100}, TOL)
        self.assertFalse(row["point_ok"])
        self.assertTrue(row["point_ok_64"])
        self.assertFalse(row["ok"])

    def test_label_only(self):
        # Accepted answers are curated aliases in labels.json; matching is
        # exact after normalisation, so a shorter word only counts when it is
        # listed.
        task = {"kind": "label"}
        truth = {"labels": ["Fullscreen link", "fullscreen", "expanded"]}
        self.assertTrue(runner.score(task, truth, {"label": "Fullscreen"}, TOL)["ok"])
        self.assertFalse(runner.score(task, truth, {"label": "settings"}, TOL)["ok"])

    def test_label_is_not_a_substring_match(self):
        task = {"kind": "label"}
        truth = {"labels": ["Fullscreen link"]}
        self.assertFalse(runner.score(task, truth, {"label": "full"}, TOL)["ok"])

    def test_label_and_point_needs_both(self):
        task = {"kind": "label+point"}
        truth = {"labels": ["refresh"], "point": {"x": 10, "y": 10}}
        good = runner.score(task, truth, {"label": "refresh", "x": 12, "y": 12}, TOL)
        self.assertTrue(good["ok"])
        wrong_label = runner.score(task, truth, {"label": "back", "x": 12, "y": 12}, TOL)
        self.assertFalse(wrong_label["ok"])
        far = runner.score(task, truth, {"label": "refresh", "x": 900, "y": 500}, TOL)
        self.assertFalse(far["ok"])

    def test_unparsed_answer_scores_zero(self):
        task = {"kind": "point"}
        truth = {"point": {"x": 10, "y": 10}}
        row = runner.score(task, truth, None, TOL)
        self.assertFalse(row["parsed"])
        self.assertFalse(row["ok"])
        self.assertIsNone(row["distance_px"])

    def test_missing_truth_point(self):
        row = runner.score({"kind": "point"}, {}, {"label": "a", "x": 1, "y": 1}, TOL)
        self.assertFalse(row["ok"])


class BuildPrompt(unittest.TestCase):
    def test_point_prompt_asks_for_coordinates(self):
        prompt = runner.build_prompt({"kind": "point", "ask": "Find the refresh button."}, 1280, 600)
        self.assertIn("1280x600", prompt)
        self.assertIn("Find the refresh button.", prompt)
        self.assertIn('"x"', prompt)

    def test_label_prompt_omits_coordinates(self):
        prompt = runner.build_prompt({"kind": "label", "ask": "Name the button."}, 1280, 600)
        self.assertNotIn('"x"', prompt)

    def test_completion_prompt_carries_the_media_marker(self):
        prompt = runner.build_completion_prompt("Locate the button.", "<__media_abc__>")
        self.assertIn("<__media_abc__>", prompt)
        self.assertIn("Locate the button.", prompt)
        self.assertTrue(prompt.startswith("USER: "))
        self.assertTrue(prompt.endswith(" ASSISTANT:"))


class ReplyText(unittest.TestCase):
    def test_string_content(self):
        body = {"choices": [{"message": {"content": "hi"}}]}
        self.assertEqual(runner.reply_text(body), "hi")

    def test_content_parts(self):
        body = {"choices": [{"message": {"content": [{"text": "a"}, {"text": "b"}]}}]}
        self.assertEqual(runner.reply_text(body), "a b")

    def test_malformed_body(self):
        self.assertEqual(runner.reply_text({"choices": []}), "")


if __name__ == "__main__":
    unittest.main()
