"""Apply the calibration fit, and decide whether it may be used for a click.

E6's calibration asks whether the model's coordinates live in the frame's pixel
space. The answer is a linear map plus a residual, and the residual is what decides
whether a model coordinate may be turned into a mouse event at all:

    corrected rms <= 16 px   the map is a correction, use it
    corrected rms >  16 px   the map is a description of noise, do not click

The 16 px threshold is half the experiment's 32 px hit radius: a correction whose
average error is already half the tolerance cannot make a point trustworthy.

Every measurement keeps both numbers — `raw` as the model sent it, `corrected` as
the fit maps it — so a later reader can see what the correction did, rather than
trusting a single transformed value.
"""

from __future__ import annotations

import json
import os

RAW_USABLE_RMS_PX = 16.0


def load(path: str) -> dict:
    """Read calibration/fit.json and return the fit itself.

    The file keeps the fit under `fit` next to the run metadata (`frame`,
    `model_url`, `reps`, `stable`, `runs`), because the per-rep rows are the
    evidence for the coefficients. Callers want the coefficients, so they get
    those, with `status` carried onto them.
    """
    with open(path, encoding="utf-8") as fh:
        data = json.load(fh)
    fit = dict(data.get("fit") or data)
    if "status" in data:
        fit.setdefault("status", data["status"])
    if "stable" in data:
        fit.setdefault("stable", data["stable"])
    return fit


def _unwrap(fit: dict | None) -> dict:
    """Accept either the bare fit or the file it lives in."""
    if isinstance(fit, dict) and isinstance(fit.get("fit"), dict):
        inner = dict(fit["fit"])
        for key in ("status", "stable"):
            if key in fit:
                inner.setdefault(key, fit[key])
        return inner
    return fit or {}


def usable(fit: dict, threshold: float = RAW_USABLE_RMS_PX) -> bool:
    """Whether a corrected point may be dispatched as a click.

    Both the recorded status and the corrected residual are required to agree. A
    fit whose status says `unusable` is not rescued by a lucky residual, and a fit
    with a bad residual is not rescued by a stale status string.
    """
    fit = _unwrap(fit)
    status = (fit or {}).get("status")
    corrected = ((fit or {}).get("corrected") or {}).get("rms_px")
    if status == "unusable" or corrected is None:
        return False
    return float(corrected) <= threshold


def apply(fit: dict, x: float, y: float) -> dict:
    """Map a reported point into viewport space, keeping the raw value.

    Returns `{raw, corrected, usable, corrected_rms_px}`. `corrected` is None when
    the fit carries no usable coefficients, which is how the controller knows not to
    click on a coordinate it cannot trust.
    """
    fit = _unwrap(fit)
    fx = fit.get("x") or {}
    fy = fit.get("y") or {}
    a, b = fx.get("a"), fx.get("b")
    c, d = fy.get("c"), fy.get("d")
    corrected = None
    if all(isinstance(v, (int, float)) for v in (a, b, c, d)):
        corrected = (round(a * x + b, 2), round(c * y + d, 2))
    return {
        "raw": (x, y),
        "corrected": corrected,
        "usable": usable(fit),
        "corrected_rms_px": ((fit.get("corrected") or {}).get("rms_px")),
    }


def inside(point: tuple[float, float] | None, width: int = 1280, height: int = 600) -> bool:
    """Whether a point is inside the frame. A corrected point can land outside."""
    if point is None:
        return False
    x, y = point
    return 0 <= x <= width - 1 and 0 <= y <= height - 1


def describe(fit: dict) -> str:
    """One line for the run log: coefficients, residuals, verdict."""
    fit = _unwrap(fit)
    fx, fy = fit.get("x") or {}, fit.get("y") or {}
    raw = (fit.get("raw") or {}).get("rms_px")
    corrected = (fit.get("corrected") or {}).get("rms_px")
    return (
        f"calibration: x={fx.get('a')}*px+{fx.get('b')} "
        f"y={fy.get('c')}*py+{fy.get('d')} "
        f"raw_rms={raw}px corrected_rms={corrected}px "
        f"status={fit.get('status')} usable_for_click={usable(fit)}"
    )


def default_path(here: str) -> str:
    return os.path.join(here, "..", "calibration", "fit.json")
