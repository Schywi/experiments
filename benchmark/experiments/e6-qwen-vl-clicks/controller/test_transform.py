"""Tests for the calibration transform and its usability rule.

    cd benchmark/experiments/e6-qwen-vl-clicks/controller && python3 -m unittest -v test_transform
"""

import unittest

import transform


def fit(a=1.0, b=0.0, c=1.0, d=0.0, *, status="ok", corrected_rms=4.0, raw_rms=5.0):
    return {
        "status": status,
        "x": {"a": a, "b": b},
        "y": {"c": c, "d": d},
        "raw": {"rms_px": raw_rms, "max_px": raw_rms * 2},
        "corrected": {"rms_px": corrected_rms, "max_px": corrected_rms * 2},
    }


class Apply(unittest.TestCase):
    def test_identity(self):
        out = transform.apply(fit(), 100, 200)
        self.assertEqual(out["raw"], (100, 200))
        self.assertEqual(out["corrected"], (100.0, 200.0))

    def test_scale_and_offset(self):
        out = transform.apply(fit(a=2.0, b=10.0, c=0.5, d=-5.0), 100, 200)
        self.assertEqual(out["corrected"], (210.0, 95.0))

    def test_missing_coefficients_give_none(self):
        broken = {"x": {"a": 1.0}, "y": {"c": 1.0, "d": 0.0}, "corrected": {"rms_px": 1.0}}
        self.assertIsNone(transform.apply(broken, 10, 10)["corrected"])

    def test_none_fit_is_safe(self):
        out = transform.apply(None, 10, 10)
        self.assertIsNone(out["corrected"])
        self.assertFalse(out["usable"])


class Usable(unittest.TestCase):
    def test_good_fit_is_usable(self):
        self.assertTrue(transform.usable(fit(corrected_rms=8.0)))

    def test_residual_above_threshold_is_not_usable(self):
        self.assertFalse(transform.usable(fit(corrected_rms=17.0)))

    def test_status_unusable_overrides_a_good_residual(self):
        self.assertFalse(transform.usable(fit(status="unusable", corrected_rms=2.0)))

    def test_missing_residual_is_not_usable(self):
        self.assertFalse(transform.usable({"status": "ok", "x": {"a": 1}, "y": {"c": 1}}))

    def test_threshold_is_inclusive(self):
        self.assertTrue(transform.usable(fit(corrected_rms=16.0)))

    def test_e6_measured_fit_is_not_usable(self):
        # the real fit from calibration/fit.json: corrected rms 214.28 px
        measured = fit(a=1.441683, b=62.538, c=2.639863, d=-20.863,
                       status="unusable", corrected_rms=214.28, raw_rms=381.42)
        self.assertFalse(transform.usable(measured))


class Inside(unittest.TestCase):
    def test_inside(self):
        self.assertTrue(transform.inside((0, 0)))
        self.assertTrue(transform.inside((1279, 599)))

    def test_outside(self):
        self.assertFalse(transform.inside((1280, 0)))
        self.assertFalse(transform.inside((-1, 10)))
        self.assertFalse(transform.inside(None))


class Describe(unittest.TestCase):
    def test_line_carries_coefficients_and_verdict(self):
        line = transform.describe(fit(a=2.0, c=3.0, corrected_rms=99.0, status="unusable"))
        self.assertIn("x=2.0", line)
        self.assertIn("y=3.0", line)
        self.assertIn("corrected_rms=99.0px", line)
        self.assertIn("usable_for_click=False", line)


if __name__ == "__main__":
    unittest.main()
