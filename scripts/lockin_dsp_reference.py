#!/usr/bin/env python3
"""
Microresonance TSF-Coherent Lock-In Demodulator & Wire Protocol Reference
=========================================================================

Reference Python implementation of the receiver -> host DSP pipeline:
  1. Unpacks 1460-byte `RawSyncPacket` (magic `0x4D58`) UDP frames streamed
     by `microresonance_receiver` (ESP32-S3 + ADXL355 + LSM6DSOX + sEMG).
  2. Performs TSF-coherent quadrature (I/Q) lock-in demodulation at the active
     probing frequency (e.g., 500 Hz or 311 Hz) across X, Y, Z axes.
  3. Computes single-sideband magnitude (g), wrapped/unwrapped acoustic phase,
     inter-axis differential phases (`d_xy`, `d_yz`, `d_xz`), circular phase
     standard deviation, and Approximate Entropy (`ApEn`) lock metrics.
  4. Emits JSONL telemetry records compatible with `scripts/analyze_session.py`.

Usage:
    # Run built-in mathematical verification of TSF-coherent I/Q demodulation:
    python3 scripts/lockin_dsp_reference.py --self-test

    # Decode a binary capture of RawSyncPacket frames to JSONL:
    python3 scripts/lockin_dsp_reference.py --bin capture.bin --freq 500.0 --out session.jsonl

    # Listen live on UDP port 5555 and write JSONL telemetry:
    python3 scripts/lockin_dsp_reference.py --udp 0.0.0.0:5555 --freq 500.0 --out live.jsonl
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
import math
from pathlib import Path
import socket
import struct
import sys
from typing import Dict, Iterator, List, Optional, Tuple

import numpy as np

# Wire protocol constants matching `microresonance_receiver/include/dsp_packet.h`
DSP_MAGIC_RAW = 0x4D58
DSP_ODR_NOMINAL = 4000.0
DSP_RAW_SAMPLES = 120
DSP_LSM_SLOTS = 30
DSP_RAW_Q_G = 16384.0
RAW_SYNC_PACKET_LEN = 1460

# Struct layout for RawSyncPacket (1460 bytes, packed little-endian)
_HEADER_FMT = "<HBBI"  # magic (u16), sensor_id (u8), reserved (u8), tsf_us_low/high handled via <HBBQI
_PACKET_FMT = (
    "<HBBQ"  # magic, sensor_id, reserved, tsf_us (uint64)
    "I"  # window_samples (uint32)
    f"{DSP_RAW_SAMPLES}h"  # raw_x (int16 * 120)
    f"{DSP_RAW_SAMPLES}h"  # raw_y (int16 * 120)
    f"{DSP_RAW_SAMPLES}h"  # raw_z (int16 * 120)
    "H"  # lsm_samples (uint16)
    f"{DSP_LSM_SLOTS}h"  # lsm_accel_x (int16 * 30)
    f"{DSP_LSM_SLOTS}h"  # lsm_accel_y (int16 * 30)
    f"{DSP_LSM_SLOTS}h"  # lsm_accel_z (int16 * 30)
    f"{DSP_LSM_SLOTS}h"  # lsm_gyro_x (int16 * 30)
    f"{DSP_LSM_SLOTS}h"  # lsm_gyro_y (int16 * 30)
    f"{DSP_LSM_SLOTS}h"  # lsm_gyro_z (int16 * 30)
    f"{DSP_LSM_SLOTS}I"  # lsm_ts (uint32 * 30)
    "H"  # emg_samples (uint16)
    f"{DSP_RAW_SAMPLES}H"  # emg (uint16 * 120)
)

assert struct.calcsize(_PACKET_FMT) == RAW_SYNC_PACKET_LEN, (
    f"Struct size mismatch: {struct.calcsize(_PACKET_FMT)} != {RAW_SYNC_PACKET_LEN}"
)


@dataclass
class RawSyncFrame:
    """Decoded representation of a single 30 ms `RawSyncPacket` (0x4D58)."""

    sensor_id: int
    tsf_us: int
    window_samples: int
    adxl_x_g: np.ndarray
    adxl_y_g: np.ndarray
    adxl_z_g: np.ndarray
    lsm_accel_g: np.ndarray  # shape (N_lsm, 3)
    lsm_gyro_dps: np.ndarray  # shape (N_lsm, 3)
    lsm_ts: np.ndarray  # shape (N_lsm,)
    emg_raw: np.ndarray  # shape (N_emg,)


def unpack_raw_sync_packet(buf: bytes) -> Optional[RawSyncFrame]:
    """Unpack a 1460-byte `RawSyncPacket` binary datagram."""
    if len(buf) < RAW_SYNC_PACKET_LEN:
        return None
    fields = struct.unpack(_PACKET_FMT, buf[:RAW_SYNC_PACKET_LEN])
    magic = fields[0]
    if magic != DSP_MAGIC_RAW:
        return None

    sensor_id = fields[1]
    tsf_us = fields[3]
    win_n = min(fields[4], DSP_RAW_SAMPLES)

    idx = 5
    raw_x = np.array(fields[idx : idx + DSP_RAW_SAMPLES], dtype=np.float64)[:win_n] / DSP_RAW_Q_G
    idx += DSP_RAW_SAMPLES
    raw_y = np.array(fields[idx : idx + DSP_RAW_SAMPLES], dtype=np.float64)[:win_n] / DSP_RAW_Q_G
    idx += DSP_RAW_SAMPLES
    raw_z = np.array(fields[idx : idx + DSP_RAW_SAMPLES], dtype=np.float64)[:win_n] / DSP_RAW_Q_G
    idx += DSP_RAW_SAMPLES

    lsm_n = min(fields[idx], DSP_LSM_SLOTS)
    idx += 1
    # LSM6DSOX sensitivity: ±2g -> 0.061 mg/LSB; ±250 dps -> 8.75 mdps/LSB
    lsm_ax = np.array(fields[idx : idx + DSP_LSM_SLOTS], dtype=np.float64)[:lsm_n] * 0.000061
    idx += DSP_LSM_SLOTS
    lsm_ay = np.array(fields[idx : idx + DSP_LSM_SLOTS], dtype=np.float64)[:lsm_n] * 0.000061
    idx += DSP_LSM_SLOTS
    lsm_az = np.array(fields[idx : idx + DSP_LSM_SLOTS], dtype=np.float64)[:lsm_n] * 0.000061
    idx += DSP_LSM_SLOTS

    lsm_gx = np.array(fields[idx : idx + DSP_LSM_SLOTS], dtype=np.float64)[:lsm_n] * 0.00875
    idx += DSP_LSM_SLOTS
    lsm_gy = np.array(fields[idx : idx + DSP_LSM_SLOTS], dtype=np.float64)[:lsm_n] * 0.00875
    idx += DSP_LSM_SLOTS
    lsm_gz = np.array(fields[idx : idx + DSP_LSM_SLOTS], dtype=np.float64)[:lsm_n] * 0.00875
    idx += DSP_LSM_SLOTS

    lsm_ts = np.array(fields[idx : idx + DSP_LSM_SLOTS], dtype=np.uint32)[:lsm_n]
    idx += DSP_LSM_SLOTS

    emg_n = min(fields[idx], DSP_RAW_SAMPLES)
    idx += 1
    emg_raw = np.array(fields[idx : idx + DSP_RAW_SAMPLES], dtype=np.float64)[:emg_n]

    lsm_accel = (
        np.column_stack([lsm_ax, lsm_ay, lsm_az]) if lsm_n > 0 else np.zeros((0, 3), dtype=np.float64)
    )
    lsm_gyro = (
        np.column_stack([lsm_gx, lsm_gy, lsm_gz]) if lsm_n > 0 else np.zeros((0, 3), dtype=np.float64)
    )

    return RawSyncFrame(
        sensor_id=sensor_id,
        tsf_us=tsf_us,
        window_samples=win_n,
        adxl_x_g=raw_x,
        adxl_y_g=raw_y,
        adxl_z_g=raw_z,
        lsm_accel_g=lsm_accel,
        lsm_gyro_dps=lsm_gyro,
        lsm_ts=lsm_ts,
        emg_raw=emg_raw,
    )


def normalize_deg_180(angle_deg: float) -> float:
    """Wrap angle into (-180, +180] degrees."""
    d = (angle_deg + 180.0) % 360.0 - 180.0
    return 180.0 if d == -180.0 else d


def circular_phase_std_deg(phases_deg: List[float]) -> float:
    """Compute circular standard deviation in degrees from angular samples."""
    if len(phases_deg) < 2:
        return 0.0
    rad = np.deg2rad(phases_deg)
    r_len = float(np.hypot(np.mean(np.sin(rad)), np.mean(np.cos(rad))))
    if r_len >= 1.0:
        return 0.0
    return float(math.sqrt(-2.0 * math.log(max(r_len, 1e-12))) * 180.0 / math.pi)


class CoherentLockInProcessor:
    """
    TSF-synchronized 3-axis quadrature (I/Q) lock-in demodulator.

    Matches the production demodulation pipeline:
      - Mixes incoming ADXL355 samples x(t), y(t), z(t) against a phase-continuous
        reference synthesized from the hardware 802.11 TSF clock `t_us * 1e-6`.
      - Computes per-window (30 ms) and multi-window (33 packets ≈ 1.0 s) coherent
        I/Q phasors to isolate sub-millig (< 0.005 g) acoustic carrier waves from
        postural and motion artifacts.
    """

    def __init__(self, carrier_freq_hz: float = 500.0, sample_rate_hz: float = DSP_ODR_NOMINAL) -> None:
        self.carrier_freq_hz = carrier_freq_hz
        self.sample_rate_hz = sample_rate_hz
        self.dt_us = 1e6 / sample_rate_hz

        self._accum_i = np.zeros(3, dtype=np.float64)
        self._accum_q = np.zeros(3, dtype=np.float64)
        self._accum_n = 0
        self._accum_pkts = 0

        self.axis_mag = np.zeros(3, dtype=np.float64)
        self.axis_phase_deg = np.zeros(3, dtype=np.float64)
        self.d_xy = 0.0
        self.d_yz = 0.0
        self.d_xz = 0.0
        self.hard_lock = False
        self.ap_en = 0.0

        self._phase_win_z: List[float] = []
        self._prev_tsf_us: Optional[int] = None
        self._prev_phase_deg: Optional[float] = None
        self.phase_vel_deg_s: float = 0.0

    def _demod_axis_window(self, samples: np.ndarray, tsf_start_us: int) -> Tuple[float, float, float, float]:
        """Return (I_sum, Q_sum, mag_g, phase_deg) for a single packet window."""
        n = len(samples)
        if n == 0:
            return 0.0, 0.0, 0.0, 0.0
        dc_removed = samples - np.mean(samples)
        t_sec = (tsf_start_us + np.arange(n, dtype=np.float64) * self.dt_us) * 1e-6
        angle = 2.0 * np.pi * self.carrier_freq_hz * t_sec
        i_sum = float(np.dot(dc_removed, np.sin(angle)))
        q_sum = float(np.dot(dc_removed, np.cos(angle)))
        mag = 2.0 * math.hypot(i_sum, q_sum) / n
        phase_deg = math.degrees(math.atan2(q_sum, i_sum)) % 360.0
        return i_sum, q_sum, mag, phase_deg

    def process_frame(self, frame: RawSyncFrame) -> Dict[str, object]:
        """Process one 30 ms `RawSyncFrame` and return a JSONL-serializable telemetry dict."""
        ix, qx, _, _ = self._demod_axis_window(frame.adxl_x_g, frame.tsf_us)
        iy, qy, _, _ = self._demod_axis_window(frame.adxl_y_g, frame.tsf_us)
        iz, qz, win_mag_z, win_phase_z = self._demod_axis_window(frame.adxl_z_g, frame.tsf_us)

        self._accum_i += np.array([ix, iy, iz])
        self._accum_q += np.array([qx, qy, qz])
        self._accum_n += frame.window_samples
        self._accum_pkts += 1

        # Instantaneous phase velocity tracker
        if self._prev_tsf_us is not None and self._prev_phase_deg is not None:
            dt_s = (frame.tsf_us - self._prev_tsf_us) * 1e-6
            if 0.0 < dt_s < 1.0:
                d_phi = normalize_deg_180(win_phase_z - self._prev_phase_deg)
                inst_vel = d_phi / dt_s
                self.phase_vel_deg_s = 0.05 * inst_vel + 0.95 * self.phase_vel_deg_s
        self._prev_tsf_us = frame.tsf_us
        self._prev_phase_deg = win_phase_z

        # 33-packet (~1.0 s) coherent vector integration for 3-axis differential stability
        if self._accum_pkts >= 33 and self._accum_n > 0:
            self.axis_mag = 2.0 * np.hypot(self._accum_i, self._accum_q) / self._accum_n
            self.axis_phase_deg = np.degrees(np.arctan2(self._accum_q, self._accum_i)) % 360.0
            self.d_xy = normalize_deg_180(float(self.axis_phase_deg[0] - self.axis_phase_deg[1]))
            self.d_yz = normalize_deg_180(float(self.axis_phase_deg[1] - self.axis_phase_deg[2]))
            self.d_xz = normalize_deg_180(float(self.axis_phase_deg[0] - self.axis_phase_deg[2]))
            self.hard_lock = bool(np.max(self.axis_mag) > 0.0005)

            self._phase_win_z.append(float(self.axis_phase_deg[2]))
            if len(self._phase_win_z) > 100:
                self._phase_win_z = self._phase_win_z[-100:]
            phase_std = circular_phase_std_deg(self._phase_win_z)
            self.ap_en = min(1.0, phase_std / 90.0)

            self._accum_i.fill(0.0)
            self._accum_q.fill(0.0)
            self._accum_n = 0
            self._accum_pkts = 0

        emg_tail = frame.emg_raw[-32:].tolist() if len(frame.emg_raw) > 0 else [0.0] * 32
        gyro = frame.lsm_gyro_dps
        accel = frame.lsm_accel_g

        return {
            "tsf_us": frame.tsf_us,
            "sensor_id": frame.sensor_id,
            "f1": {
                "freq": round(self.carrier_freq_hz, 3),
                "phase": round(win_phase_z, 5),
                "mag": round(win_mag_z, 9),
                "snr": 0.0,
            },
            "f2": {"freq": 0, "phase": 0, "mag": 0, "snr": 0},
            "d_phi_beat": 0.0,
            "bpm": 0.0,
            "hrv": 0.0,
            "ap_en": round(self.ap_en, 6),
            "phase_vel": round(self.phase_vel_deg_s, 6),
            "hard_lock": self.hard_lock,
            "jitter_us": 0.0,
            "drift_rate": 0.0,
            "adxl_xp": round(float(self.axis_phase_deg[0]), 5),
            "adxl_xm": round(float(self.axis_mag[0]), 9),
            "adxl_yp": round(float(self.axis_phase_deg[1]), 5),
            "adxl_ym": round(float(self.axis_mag[1]), 9),
            "adxl_zp": round(float(self.axis_phase_deg[2]), 5),
            "adxl_zm": round(float(self.axis_mag[2]), 9),
            "d_xy": round(self.d_xy, 5),
            "d_yz": round(self.d_yz, 5),
            "d_xz": round(self.d_xz, 5),
            "emg_tail": emg_tail,
            "lsm_accel_x_tail": accel[-32:, 0].tolist() if len(accel) > 0 else [0.0],
            "lsm_accel_y_tail": accel[-32:, 1].tolist() if len(accel) > 0 else [0.0],
            "lsm_accel_z_tail": accel[-32:, 2].tolist() if len(accel) > 0 else [0.0],
            "lsm_gyro_x_tail": gyro[-32:, 0].tolist() if len(gyro) > 0 else [0.0],
            "lsm_gyro_y_tail": gyro[-32:, 1].tolist() if len(gyro) > 0 else [0.0],
            "lsm_gyro_z_tail": gyro[-32:, 2].tolist() if len(gyro) > 0 else [0.0],
        }


def iter_frames_from_bin(path: Path) -> Iterator[RawSyncFrame]:
    """Yield `RawSyncFrame` objects from a binary capture file."""
    data = path.read_bytes()
    offset = 0
    n = len(data)
    while offset + RAW_SYNC_PACKET_LEN <= n:
        magic = struct.unpack_from("<H", data, offset)[0]
        if magic != DSP_MAGIC_RAW:
            offset += 1
            continue
        frame = unpack_raw_sync_packet(data[offset : offset + RAW_SYNC_PACKET_LEN])
        if frame is not None:
            yield frame
        offset += RAW_SYNC_PACKET_LEN


def run_self_test() -> int:
    """
    Verify TSF-coherent I/Q demodulation accuracy on a synthetic 500 Hz wave
    with known amplitude (0.0025 g), known phase shifts, and synthetic postural noise.
    """
    print("Running Microresonance TSF-Coherent Lock-In Demodulator self-test...")
    proc = CoherentLockInProcessor(carrier_freq_hz=500.0, sample_rate_hz=4000.0)
    rng = np.random.default_rng(42)

    true_mag_g = 0.0025
    true_phase_x_deg = 45.0
    true_phase_y_deg = 165.0  # true d_xy = -120.0 deg
    true_phase_z_deg = 280.0  # true d_yz = -115.0 deg

    tsf_base_us = 456_500_000_000
    dt_us = 250  # 4000 Hz -> 250 us per sample

    last_record: Dict[str, object] = {}
    for pkt_idx in range(35):
        pkt_tsf = tsf_base_us + pkt_idx * DSP_RAW_SAMPLES * dt_us
        t_sec = (pkt_tsf + np.arange(DSP_RAW_SAMPLES) * dt_us) * 1e-6
        w = 2.0 * np.pi * 500.0 * t_sec

        # Add low-frequency postural sway (8 Hz, 0.02 g — 8x stronger than the 500 Hz carrier!)
        sway = 0.02 * np.sin(2.0 * np.pi * 8.0 * t_sec)
        noise = rng.normal(0.0, 0.0002, size=(3, DSP_RAW_SAMPLES))

        sig_x = true_mag_g * np.sin(w + math.radians(true_phase_x_deg)) + sway + noise[0]
        sig_y = true_mag_g * np.sin(w + math.radians(true_phase_y_deg)) + sway + noise[1]
        sig_z = true_mag_g * np.sin(w + math.radians(true_phase_z_deg)) + sway + noise[2]

        qx = np.clip(np.round(sig_x * DSP_RAW_Q_G), -32768, 32767).astype(np.int16)
        qy = np.clip(np.round(sig_y * DSP_RAW_Q_G), -32768, 32767).astype(np.int16)
        qz = np.clip(np.round(sig_z * DSP_RAW_Q_G), -32768, 32767).astype(np.int16)

        zeros_lsm = [0] * DSP_LSM_SLOTS
        emg_sim = [1930] * DSP_RAW_SAMPLES
        raw_bytes = struct.pack(
            _PACKET_FMT,
            DSP_MAGIC_RAW,
            4,
            0,
            pkt_tsf,
            DSP_RAW_SAMPLES,
            *qx.tolist(),
            *qy.tolist(),
            *qz.tolist(),
            DSP_LSM_SLOTS,
            *zeros_lsm,
            *zeros_lsm,
            *zeros_lsm,
            *zeros_lsm,
            *zeros_lsm,
            *zeros_lsm,
            *zeros_lsm,
            DSP_RAW_SAMPLES,
            *emg_sim,
        )
        frame = unpack_raw_sync_packet(raw_bytes)
        assert frame is not None
        last_record = proc.process_frame(frame)

    f1 = last_record["f1"]
    assert isinstance(f1, dict)
    est_mag = float(last_record["adxl_zm"])
    est_phase_z = float(last_record["adxl_zp"])
    est_d_xy = float(last_record["d_xy"])
    est_d_yz = float(last_record["d_yz"])

    print(f"  * True Z Carrier Mag:   {true_mag_g:.6f} g   | Recovered: {est_mag:.6f} g")
    print(f"  * True Z Carrier Phase: {true_phase_z_deg:.2f} deg   | Recovered: {est_phase_z:.2f} deg")
    print(f"  * True Delta X-Y Phase: -120.00 deg | Recovered: {est_d_xy:.2f} deg")
    print(f"  * True Delta Y-Z Phase: -115.00 deg | Recovered: {est_d_yz:.2f} deg")
    print(f"  * Hard-Lock Achieved:   {last_record['hard_lock']}")

    assert abs(est_mag - true_mag_g) < 0.00015, "Magnitude recovery out of tolerance"
    assert abs(normalize_deg_180(est_phase_z - true_phase_z_deg)) < 1.0, "Phase recovery out of tolerance"
    assert abs(normalize_deg_180(est_d_xy - (-120.0))) < 1.0, "Delta XY out of tolerance"
    assert abs(normalize_deg_180(est_d_yz - (-115.0))) < 1.0, "Delta YZ out of tolerance"
    assert last_record["hard_lock"] is True, "Expected hard_lock=True"
    print("SELF-TEST PASSED: Sub-degree phase lock achieved under 8x stronger postural sway.")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Microresonance TSF-Coherent Lock-In Demodulator & RawSyncPacket Reference"
    )
    parser.add_argument("--self-test", action="store_true", help="Run synthetic verification self-test")
    parser.add_argument("--bin", type=Path, help="Decode binary capture file of RawSyncPacket frames")
    parser.add_argument("--udp", type=str, help="Listen for live RawSyncPacket frames on host:port (e.g., 0.0.0.0:5555)")
    parser.add_argument("--freq", type=float, default=500.0, help="Active acoustic carrier frequency in Hz (default: 500.0)")
    parser.add_argument("--out", type=Path, help="Output JSONL file path")
    args = parser.parse_args()

    if args.self_test or (not args.bin and not args.udp):
        return run_self_test()

    proc = CoherentLockInProcessor(carrier_freq_hz=args.freq)
    out_fp = args.out.open("w", encoding="utf-8") if args.out else sys.stdout

    try:
        if args.bin:
            count = 0
            for frame in iter_frames_from_bin(args.bin):
                rec = proc.process_frame(frame)
                out_fp.write(json.dumps(rec) + "\n")
                count += 1
            if args.out:
                print(f"Decoded {count} frames from {args.bin} -> {args.out}")
        elif args.udp:
            host, port_s = args.udp.rsplit(":", 1)
            sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            sock.bind((host, int(port_s)))
            print(f"Listening for RawSyncPacket (0x4D58) on {host}:{port_s} at f1={args.freq} Hz...")
            while True:
                data, _ = sock.recvfrom(2048)
                frame = unpack_raw_sync_packet(data)
                if frame is not None:
                    rec = proc.process_frame(frame)
                    out_fp.write(json.dumps(rec) + "\n")
                    out_fp.flush()
    except KeyboardInterrupt:
        print("\nStopped.")
    finally:
        if args.out and out_fp is not sys.stdout:
            out_fp.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
