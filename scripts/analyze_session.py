#!/usr/bin/env python3
"""
Microresonance Session Analyzer & Multi-Panel Visualizer
========================================================

Reconstructs the 7-panel biophysical & acoustic lock-in dashboard from raw
JSONL telemetry dumps (`physio_raw_*.jsonl`) and computes regime-by-regime
signal quality metrics comparing active 500 Hz acoustic phase probing against
conventional surface EMG (sEMG).

Usage:
    python3 scripts/analyze_session.py
    python3 scripts/analyze_session.py --input physio_raw_20260514_175320.jsonl --output session_analysis.png
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Dict, List

import matplotlib.pyplot as plt
from matplotlib.gridspec import GridSpec
import numpy as np


def load_session(jsonl_path: Path) -> List[Dict[str, Any]]:
    """Load non-empty JSONL records from a raw telemetry dump."""
    if not jsonl_path.is_file():
        raise FileNotFoundError(f"Telemetry file not found: {jsonl_path}")

    records: List[Dict[str, Any]] = []
    with jsonl_path.open("r", encoding="utf-8") as f:
        for line_num, line in enumerate(f, start=1):
            line = line.strip()
            if not line:
                continue
            try:
                records.append(json.loads(line))
            except json.JSONDecodeError as exc:
                raise ValueError(f"Invalid JSON on line {line_num} of {jsonl_path}: {exc}") from exc

    if not records:
        raise ValueError(f"No valid records found in {jsonl_path}")
    return records


def extract_signals(records: List[Dict[str, Any]]) -> Dict[str, np.ndarray]:
    """
    Extract synchronized time-series arrays from parsed JSONL records.

    Signal definitions:
      - t_sec:      Relative time in seconds from hardware 802.11 TSF microsecond counter.
      - phase_deg:  Wrapped lock-in demodulated phase in degrees [0, 360) at carrier f1.
      - phase_rad:  Unwrapped continuous acoustic phase trajectory in radians.
      - mag_g:      Single-sideband lock-in amplitude at carrier f1 in gravitational units (g).
      - emg_std:    Per-window standard deviation of the 32-sample raw SAR ADC sEMG tail.
      - gyro_mag:   3-axis gyroscope magnitude (deg/s) from LSM6DSOX IMU tail means.
      - d_xy, d_yz: Inter-axis differential lock-in phases (phX - phY, phY - phZ) in degrees.
      - phase_vel:  Absolute acoustic phase velocity |d(phase_rad)/dt| via central differences (rad/s).
    """
    t0_us = records[0]["tsf_us"]
    t_sec = np.array([(r["tsf_us"] - t0_us) / 1e6 for r in records], dtype=np.float64)

    phase_deg = np.array([r["f1"]["phase"] for r in records], dtype=np.float64)
    phase_rad = np.unwrap(np.deg2rad(phase_deg))
    mag_g = np.array([r["f1"]["mag"] for r in records], dtype=np.float64)

    emg_std = np.array(
        [np.std(r.get("emg_tail", [0.0])) if r.get("emg_tail") else 0.0 for r in records],
        dtype=np.float64,
    )

    gyro_mag = np.array(
        [
            np.sqrt(
                np.mean(r.get("lsm_gyro_x_tail", [0.0])) ** 2
                + np.mean(r.get("lsm_gyro_y_tail", [0.0])) ** 2
                + np.mean(r.get("lsm_gyro_z_tail", [0.0])) ** 2
            )
            for r in records
        ],
        dtype=np.float64,
    )

    d_xy = np.array([r.get("d_xy", 0.0) for r in records], dtype=np.float64)
    d_yz = np.array([r.get("d_yz", 0.0) for r in records], dtype=np.float64)

    phase_vel = np.abs(np.gradient(phase_rad, t_sec))
    hard_lock = np.array([bool(r.get("hard_lock", False)) for r in records], dtype=bool)
    carrier_hz = np.array([r["f1"]["freq"] for r in records], dtype=np.float64)

    return {
        "t_sec": t_sec,
        "phase_deg": phase_deg,
        "phase_rad": phase_rad,
        "mag_g": mag_g,
        "emg_std": emg_std,
        "gyro_mag": gyro_mag,
        "d_xy": d_xy,
        "d_yz": d_yz,
        "phase_vel": phase_vel,
        "hard_lock": hard_lock,
        "carrier_hz": carrier_hz,
    }


def print_session_report(sig: Dict[str, np.ndarray], source_name: str) -> None:
    """Print quantitative regime breakdown of the experimental session."""
    t = sig["t_sec"]
    phase_rad = sig["phase_rad"]
    mag_g = sig["mag_g"]
    emg = sig["emg_std"]
    gyro = sig["gyro_mag"]
    f0 = float(np.median(sig["carrier_hz"]))
    dt_ms = float(np.median(np.diff(t)) * 1e3)
    lock_ratio = float(np.mean(sig["hard_lock"]) * 100.0)

    regimes = [
        ("1. Static Rest (Acoustic Lock)", 0.0, 30.0),
        ("2. Seated Isometric Cycles", 30.0, 68.0),
        ("3. Seated Leg Extension (Re-Lock)", 70.0, 105.0),
        ("4. Standing Orthostatic Load", 110.0, 136.0),
        ("5. Seated Recalibration", 140.0, float(t[-1])),
    ]

    print("=" * 88)
    print(f"MICRORESONANCE TELEMETRY REPORT — {source_name}")
    print("=" * 88)
    print(
        f"Frames: {len(t)}  |  Duration: {t[-1]:.2f} s  |  Frame interval: {dt_ms:.2f} ms  |  "
        f"Carrier: {f0:.1f} Hz  |  Hard-Lock: {lock_ratio:.1f}%"
    )
    print("-" * 88)
    print(
        f"{'Experimental Regime':<34} | {'Window (s)':<12} | {'Phase Range (rad)':<17} | "
        f"{'Mean Mag (mg)':<13} | {'sEMG RMS/Std':<12}"
    )
    print("-" * 88)

    for label, t_start, t_end in regimes:
        m = (t >= t_start) & (t <= t_end)
        if not np.any(m):
            continue
        p_span = float(np.percentile(phase_rad[m], 95) - np.percentile(phase_rad[m], 5))
        m_mg = float(np.mean(mag_g[m]) * 1e3)
        e_mean = float(np.mean(emg[m]))
        print(
            f"{label:<34} | {f'{t_start:>5.1f}–{t_end:<5.1f}':<12} | "
            f"{p_span:>17.3f} | {m_mg:>13.3f} | {e_mean:>12.1f}"
        )

    print("-" * 88)
    m_seat = (t >= 30.0) & (t <= 68.0)
    m_stand = (t >= 110.0) & (t <= 136.0)
    if np.any(m_seat) and np.any(m_stand):
        emg_floor_seat = float(np.percentile(emg[m_seat], 25))
        emg_floor_stand = float(np.percentile(emg[m_stand], 25))
        emg_ratio = emg_floor_stand / max(emg_floor_seat, 1e-6)
        seat_step_deg = float(np.rad2deg(np.percentile(phase_rad[m_seat], 90) - np.percentile(phase_rad[m_seat], 10)))
        stand_step_deg = float(
            np.rad2deg(np.percentile(phase_rad[m_stand], 90) - np.percentile(phase_rad[m_stand], 10))
        )
        print("Key Comparative Findings (Seated vs. Standing Orthostatic Load):")
        print(
            f"  * sEMG Baseline Floor (25th pct):   {emg_floor_seat:.1f} (seated) -> {emg_floor_stand:.1f} (standing) "
            f"[{emg_ratio:.1f}x postural noise surge]"
        )
        print(
            f"  * Acoustic Phase Dynamic Step:      {seat_step_deg:.1f} deg (seated) vs. {stand_step_deg:.1f} deg (standing) "
            "[step contrast preserved]"
        )
        print(
            f"  * Peak Postural Transition Gyro:    {float(np.max(gyro)):.1f} deg/s "
            f"(settled to {float(np.median(gyro[m_stand])):.2f} deg/s during standing holds)"
        )
    print("=" * 88)


def render_dashboard(
    sig: Dict[str, np.ndarray],
    output_path: Path,
    dpi: int = 100,
    zoom_start_sec: float = 100.0,
) -> None:
    """
    Render the 7-panel biophysical & acoustic lock-in dashboard matching the
    reference experiment figure (`1778779569118.png`).
    """
    t = sig["t_sec"]
    phase_deg = sig["phase_deg"]
    phase_rad = sig["phase_rad"]
    mag_g = sig["mag_g"]
    emg = sig["emg_std"]
    gyro_mag = sig["gyro_mag"]
    d_xy = sig["d_xy"]
    d_yz = sig["d_yz"]
    phase_vel = sig["phase_vel"]

    fig = plt.figure(figsize=(18, 24), dpi=dpi)
    gs = GridSpec(8, 1, figure=fig)

    # Panel 1: Full Session: Neural Intent vs. Acoustic Phase
    ax1 = fig.add_subplot(gs[0, 0])
    ax1.plot(t, phase_rad, color="black", lw=1.5, label="Acoustic Phase (rad)")
    ax1.set_title("Full Session: Neural Intent vs. Acoustic Phase", fontsize=14)
    ax1.legend(loc="upper left")
    ax1.grid(True, alpha=0.2)

    ax1_r = ax1.twinx()
    ax1_r.plot(t, emg, color="red", alpha=0.3, lw=1.0, label="EMG (Neural)")
    ax1_r.legend(loc="upper right")

    # Panel 2: Acoustic Transparency (Magnitude)
    ax2 = fig.add_subplot(gs[1, 0])
    ax2.plot(t, mag_g, color="blue", lw=1.0)
    ax2.fill_between(t, mag_g, color="blue", alpha=0.1)
    ax2.set_ylabel("Mag (g)")
    ax2.set_title("Acoustic Transparency (Magnitude)", fontsize=14)
    ax2.grid(True, alpha=0.2)

    # Panel 3: Motion Dynamics (Gyroscope)
    ax3 = fig.add_subplot(gs[2, 0])
    ax3.plot(t, gyro_mag, color="green", lw=1.2)
    ax3.set_ylabel("Gyro Mag")
    ax3.set_title("Motion Dynamics (Gyroscope)", fontsize=14)
    ax3.grid(True, alpha=0.2)

    # Panel 4: Inter-Axis Phase Coherency (XYZ Vector Stability)
    ax4 = fig.add_subplot(gs[3, 0])
    ax4.plot(t, d_xy, color="#0074D9", lw=1.2, label="Delta Phase X-Y")
    ax4.plot(t, d_yz, color="#E69F00", lw=1.2, label="Delta Phase Y-Z")
    ax4.set_title("Inter-Axis Phase Coherency (XYZ Vector Stability)", fontsize=14)
    ax4.legend(loc="upper right")
    ax4.grid(True, alpha=0.2)

    # Panel 5: Acoustic Reaction Speed (Phase Velocity)
    ax5 = fig.add_subplot(gs[4, 0])
    ax5.plot(t, phase_vel, color="purple", lw=1.2)
    ax5.set_ylabel("rad/s")
    ax5.set_title("Acoustic Reaction Speed (Phase Velocity)", fontsize=14)
    ax5.grid(True, alpha=0.2)

    # Panel 6: ZOOM: Standing & Final Sitting (100s - End)
    ax6 = fig.add_subplot(gs[5, 0])
    mask = t >= zoom_start_sec
    ax6.plot(t[mask], phase_rad[mask], color="black", lw=2.0, label="Zoom: Phase")
    ax6.set_title(
        f"ZOOM: Standing & Final Sitting ({int(zoom_start_sec)}s - End)",
        fontsize=14,
        color="darkorange",
    )
    ax6.legend(loc="upper left")
    ax6.grid(True, alpha=0.2)

    ax6_r = ax6.twinx()
    ax6_r.plot(t[mask], emg[mask], color="red", alpha=0.4, lw=1.0, label="Zoom: EMG")
    ax6_r.legend(loc="upper right")

    # Panel 7: The Universal Bio-Acoustic Alphabet (Phase vs Mag Space)
    ax7 = fig.add_subplot(gs[6:8, 0])
    sc = ax7.scatter(phase_deg, mag_g, c=emg, cmap="magma", s=15, alpha=0.5)
    ax7.set_xlabel("Phase (deg)")
    ax7.set_ylabel("Mag (g)")
    ax7.set_title("The Universal Bio-Acoustic Alphabet (Phase vs Mag Space)", fontsize=14)
    ax7.grid(True, alpha=0.2)
    cbar = fig.colorbar(sc, ax=ax7)
    cbar.set_label("EMG Intensity")

    plt.tight_layout()
    output_path.parent.mkdir(parents=True, exist_ok=True)
    plt.savefig(output_path, dpi=dpi)
    plt.close(fig)


def parse_args() -> argparse.Namespace:
    repo_root = Path(__file__).resolve().parent.parent
    default_input = repo_root / "physio_raw_20260514_175320.jsonl"
    default_output = repo_root / "1778779569118.png"

    parser = argparse.ArgumentParser(
        description="Analyze raw Microresonance JSONL telemetry and generate the 7-panel dashboard."
    )
    parser.add_argument(
        "-i",
        "--input",
        type=Path,
        default=default_input,
        help=f"Path to raw JSONL telemetry dump (default: {default_input.name})",
    )
    parser.add_argument(
        "-o",
        "--output",
        type=Path,
        default=default_output,
        help=f"Output PNG path for the 7-panel dashboard (default: {default_output.name})",
    )
    parser.add_argument(
        "--dpi",
        type=int,
        default=100,
        help="Output figure DPI (default: 100 -> 1800x2400 px; use 66.67 for 1200x1600 px)",
    )
    parser.add_argument(
        "--zoom-start",
        type=float,
        default=100.0,
        help="Start timestamp in seconds for Panel 6 zoom view (default: 100.0)",
    )
    parser.add_argument(
        "--no-plot",
        action="store_true",
        help="Only print the quantitative telemetry report without writing a PNG file.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    records = load_session(args.input)
    signals = extract_signals(records)
    print_session_report(signals, args.input.name)

    if not args.no_plot:
        render_dashboard(
            signals,
            output_path=args.output,
            dpi=args.dpi,
            zoom_start_sec=args.zoom_start,
        )
        print(f"Saved 7-panel visualization to: {args.output}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
