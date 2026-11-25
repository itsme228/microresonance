#!/usr/bin/env python3
"""
State-Space + Event Transformer model training.

Converts data/trainer_dataset/ (event JSON) to ml_queue format:
- FrameSummary: simulated from events (band_energy by band_idx, total_energy)
- EventGlyph: features → protoSig (8 from shape/features), axisSig (u1,u2,e1,e2)
- EventReaction: reaction (16 phases)

Output: data/saved_model/ + meta.json (format: "ml_queue")
"""

import argparse
import json
import os
import sys
from collections import defaultdict
from typing import Any, Dict, List, Optional, Set, Tuple

import numpy as np
import tensorflow as tf

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from state_space_event_model import (
    build_trainable_pipeline,
    NUM_BANDS,
    CSSM_BAND_DIM,
    AXIS_RATIO_DIM,
    PROTO_SIG_DIM,
    AXIS_SIG_DIM,
    W_SHORT_EVENTS,
)

DATASET_DIR = "data/trainer_dataset"
OUTPUT_DIR = "data/saved_model"
EVENT_DIM = 17
REACTION_DIM = 16


def _run_id_from_path(path: str, action_dir: str) -> str:
    """Извлекает run_id из пути. Включает action_dir, чтобы 1_LEFT/run_001 != 2_RIGHT/run_001."""
    for part in path.replace("\\", "/").split("/"):
        if part.startswith("run_"):
            return f"{action_dir}/{part}"
    return f"{action_dir}/run_000"


def load_events_from_dataset(dataset_dir: str) -> Tuple[List[Dict], List[int], List[str], Dict[int, str]]:
    """Загружает события из trainer_dataset. Возвращает (events, labels, run_ids, id_to_name)."""
    events: List[Dict] = []
    labels: List[int] = []
    run_ids: List[str] = []
    id_to_name: Dict[int, str] = {}

    if not os.path.isdir(dataset_dir):
        return events, labels, run_ids, id_to_name

    for dirname in sorted(os.listdir(dataset_dir)):
        action_dir = os.path.join(dataset_dir, dirname)
        if not os.path.isdir(action_dir):
            continue
        parts = dirname.split("_", 1)
        try:
            aid = int(parts[0])
        except (ValueError, IndexError):
            continue
        name = (parts[1].strip() if len(parts) > 1 else "") or f"action_{aid}"
        id_to_name[aid] = name

        for root, _, files in os.walk(action_dir):
            for fname in files:
                if not fname.endswith(".json"):
                    continue
                path = os.path.join(root, fname)
                if fname == "meta.json":
                    continue  # run meta (action_id, prev_action_id), не event
                try:
                    with open(path, "r", encoding="utf-8") as f:
                        s = json.load(f)
                except (json.JSONDecodeError, OSError):
                    continue
                feat = s.get("features", s.get("vector", []))
                shape = s.get("shape", [])
                if len(shape) < 10 and len(feat) < 10:
                    continue  # не event (legacy points или мусор)
                meta = s.get("meta", {})
                ts = int(meta.get("ts", s.get("ts", 0)))
                band_idx = int(meta.get("band_idx", 0))
                reaction = s.get("reaction", [])
                reaction = (reaction[:REACTION_DIM] + [0.0] * REACTION_DIM)[:REACTION_DIM]

                feat = (feat[:EVENT_DIM] + [0.0] * EVENT_DIM)[:EVENT_DIM]
                if len(shape) >= 8:
                    proto = _shape_to_proto(shape)
                else:
                    proto = feat[:PROTO_SIG_DIM] if len(feat) >= PROTO_SIG_DIM else [0.0] * PROTO_SIG_DIM
                axis = feat[10:14] if len(feat) >= 14 else [0.0] * AXIS_SIG_DIM

                events.append({
                    "ts": ts,
                    "band_idx": band_idx,
                    "proto_sig": proto,
                    "axis_sig": axis,
                    "amp": 0.9,
                    "reaction": reaction,
                    "reaction_valid": s.get("reaction_valid", len(reaction) > 0),
                })
                labels.append(aid)
                run_ids.append(_run_id_from_path(path, dirname))

    return events, labels, run_ids, id_to_name


def _shape_to_proto(shape: List[float]) -> List[float]:
    """PeakProfile-like vector from shape (8 segments)."""
    n = len(shape)
    if n < 8:
        return [0.0] * PROTO_SIG_DIM
    seg = n // 8
    out = []
    for i in range(8):
        start = i * seg
        end = min(start + seg, n)
        if end > start:
            out.append(float(np.max(np.abs(shape[start:end]))))
        else:
            out.append(0.0)
    s = sum(out) or 1e-12
    return [x / s for x in out]


def _frame_features(band_energy: np.ndarray, total: float, exciter: int, am_freq: float) -> np.ndarray:
    """Один кадр → вектор для CSSM: band[0..6], total, quality, axis[0..6], exciter, am_freq."""
    band = np.array((band_energy[:CSSM_BAND_DIM].tolist() + [0.0] * CSSM_BAND_DIM)[:CSSM_BAND_DIM], dtype=np.float32)
    axis = np.zeros(AXIS_RATIO_DIM, dtype=np.float32)
    return np.concatenate([band, [total], [1.0], axis, [float(exciter)], [am_freq]], dtype=np.float32)


def build_training_batches(
    events: List[Dict],
    labels: List[int],
    run_ids: List[str],
    batch_size: int,
    max_events: int = W_SHORT_EVENTS,
    window_step: int = 4,
) -> Tuple[np.ndarray, np.ndarray, np.ndarray, np.ndarray, np.ndarray, np.ndarray, np.ndarray, np.ndarray]:
    """Groups events by (action, run), creates overlapping windows (window_step=4).
    Returns raw event features for end-to-end training.
    """
    by_action_run: Dict[Tuple[int, str], List[Tuple[Dict, int]]] = defaultdict(list)
    for ev, lab, rid in zip(events, labels, run_ids):
        by_action_run[(lab, rid)].append((ev, lab))

    frame_seq_arr: List[np.ndarray] = []
    proto_arr: List[np.ndarray] = []
    axis_arr: List[np.ndarray] = []
    amp_arr: List[np.ndarray] = []
    dt_arr: List[np.ndarray] = []
    band_idx_arr: List[np.ndarray] = []
    is_new_arr: List[np.ndarray] = []
    mask_arr: List[np.ndarray] = []
    y_arr: List[int] = []

    for (action_id, _), samples in by_action_run.items():
        samples = sorted(samples, key=lambda x: x[0]["ts"])
        for i in range(0, len(samples) - max_events + 1, window_step):
            chunk = samples[i : i + max_events]
            if not chunk:
                continue

            band_energy = np.zeros(NUM_BANDS, dtype=np.float32)
            total_energy = 0.0
            for ev, _ in chunk:
                b = min(NUM_BANDS - 1, max(0, ev["band_idx"]))
                band_energy[b] += 1.0
                total_energy += 1.0
            total_energy = max(total_energy, 1e-12)

            feat = _frame_features(band_energy, total_energy, 0, 0.7)
            frame_seq = feat.reshape(1, -1)

            proto_list: List[np.ndarray] = []
            axis_list: List[np.ndarray] = []
            amp_list: List[np.ndarray] = []
            dt_list: List[np.ndarray] = []
            band_list: List[np.ndarray] = []
            is_new_list: List[np.ndarray] = []
            prev_ts = 0
            for ev, _ in chunk:
                proto = np.array((ev.get("proto_sig", []) + [0.0] * PROTO_SIG_DIM)[:PROTO_SIG_DIM], dtype=np.float32)
                axis = np.array((ev.get("axis_sig", []) + [0.0] * AXIS_SIG_DIM)[:AXIS_SIG_DIM], dtype=np.float32)
                amp = np.array([ev.get("amp", 0.9)], dtype=np.float32)
                ts = ev.get("ts", 0)
                dt = np.array([max(0, (ts - prev_ts) / 1000.0)], dtype=np.float32)
                prev_ts = ts
                band_idx = np.array([min(NUM_BANDS, max(0, ev["band_idx"]))], dtype=np.int32)
                is_new = np.array([0.0], dtype=np.float32)
                proto_list.append(proto)
                axis_list.append(axis)
                amp_list.append(amp)
                dt_list.append(dt)
                band_list.append(band_idx)
                is_new_list.append(is_new)

            # Pad to max_events
            n_valid = len(chunk)
            while len(proto_list) < max_events:
                proto_list.append(np.zeros(PROTO_SIG_DIM, dtype=np.float32))
                axis_list.append(np.zeros(AXIS_SIG_DIM, dtype=np.float32))
                amp_list.append(np.array([0.0], dtype=np.float32))
                dt_list.append(np.array([0.0], dtype=np.float32))
                band_list.append(np.array([0], dtype=np.int32))
                is_new_list.append(np.array([0.0], dtype=np.float32))
            mask = np.array([1.0] * n_valid + [0.0] * (max_events - n_valid), dtype=np.float32)

            frame_seq_arr.append(frame_seq)
            proto_arr.append(np.array(proto_list[:max_events], dtype=np.float32))
            axis_arr.append(np.array(axis_list[:max_events], dtype=np.float32))
            amp_arr.append(np.array(amp_list[:max_events], dtype=np.float32).reshape(max_events, 1))
            dt_arr.append(np.array(dt_list[:max_events], dtype=np.float32).reshape(max_events, 1))
            band_idx_arr.append(np.array(band_list[:max_events], dtype=np.int32).reshape(max_events, 1))
            is_new_arr.append(np.array(is_new_list[:max_events], dtype=np.float32).reshape(max_events, 1))
            mask_arr.append(mask)
            y_arr.append(action_id)

    if not y_arr:
        feat_dim = CSSM_BAND_DIM + 1 + 1 + AXIS_RATIO_DIM + 2
        return (
            np.zeros((0, 1, feat_dim)),
            np.zeros((0, max_events, PROTO_SIG_DIM)),
            np.zeros((0, max_events, AXIS_SIG_DIM)),
            np.zeros((0, max_events, 1)),
            np.zeros((0, max_events, 1)),
            np.zeros((0, max_events, 1), dtype=np.int32),
            np.zeros((0, max_events, 1)),
            np.zeros((0, max_events)),
            np.zeros(0, dtype=np.int32),
        )

    frame_seq_np = np.array(frame_seq_arr, dtype=np.float32)
    proto_np = np.array(proto_arr, dtype=np.float32)
    axis_np = np.array(axis_arr, dtype=np.float32)
    amp_np = np.array(amp_arr, dtype=np.float32)
    dt_np = np.array(dt_arr, dtype=np.float32)
    band_idx_np = np.array(band_idx_arr, dtype=np.int32)
    is_new_np = np.array(is_new_arr, dtype=np.float32)
    mask_np = np.array(mask_arr, dtype=np.float32)
    y_np = np.array(y_arr, dtype=np.int32)

    return frame_seq_np, proto_np, axis_np, amp_np, dt_np, band_idx_np, is_new_np, mask_np, y_np


def _stratified_split_runs(
    labels: List[int],
    run_ids: List[str],
    val_frac: float,
    seed: int,
) -> Tuple[Set[str], Set[str]]:
    """Стратифицированный split по run: 80% run'ов в train, 20% в val (по каждому классу)."""
    runs_by_action: Dict[int, List[str]] = defaultdict(list)
    seen: Dict[int, Set[str]] = defaultdict(set)
    for lab, rid in zip(labels, run_ids):
        if rid not in seen[lab]:
            seen[lab].add(rid)
            runs_by_action[lab].append(rid)
    rng = np.random.default_rng(seed)
    train_runs: Set[str] = set()
    val_runs: Set[str] = set()
    for action_id, runs in runs_by_action.items():
        runs = sorted(runs)
        n_val = max(1, int(len(runs) * val_frac)) if len(runs) > 1 else 0
        if n_val > 0:
            val_idx = set(rng.choice(len(runs), size=n_val, replace=False).tolist())
            for i in val_idx:
                val_runs.add(runs[i])
            for i in range(len(runs)):
                if i not in val_idx:
                    train_runs.add(runs[i])
        else:
            train_runs.update(runs)
    assert train_runs.isdisjoint(val_runs), "run_id must be in train OR val, never both"
    return train_runs, val_runs


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dataset", default=DATASET_DIR, help="Dataset directory")
    ap.add_argument("--output", default=OUTPUT_DIR, help="Output model directory")
    ap.add_argument("--epochs", type=int, default=30)
    ap.add_argument("--batch", type=int, default=16)
    ap.add_argument("--window-step", type=int, default=4, help="Шаг окон (4 = плотная нарезка)")
    ap.add_argument("--val-frac", type=float, default=0.2, help="Доля run'ов для validation")
    ap.add_argument("--seed", type=int, default=42, help="Random seed")
    args = ap.parse_args()

    events, labels, run_ids, id_to_name = load_events_from_dataset(args.dataset)
    if not events:
        print("No events in dataset. Run calibration first.", file=sys.stderr)
        sys.exit(1)

    unique_actions = sorted(set(labels))
    num_classes = len(unique_actions)
    idx_to_id = unique_actions
    id_to_name_str = {str(k): v for k, v in id_to_name.items()}

    # Split по run (стратифицировано) — val не пересекается с train по сессиям
    train_runs, val_runs = _stratified_split_runs(labels, run_ids, args.val_frac, args.seed)
    train_mask = np.array([r in train_runs for r in run_ids])
    val_mask = np.array([r in val_runs for r in run_ids])

    train_events = [e for e, m in zip(events, train_mask) if m]
    train_labels = [l for l, m in zip(labels, train_mask) if m]
    train_run_ids = [r for r, m in zip(run_ids, train_mask) if m]
    val_events = [e for e, m in zip(events, val_mask) if m]
    val_labels = [l for l, m in zip(labels, val_mask) if m]
    val_run_ids = [r for r, m in zip(run_ids, val_mask) if m]

    (
        frame_seq_np,
        proto_np,
        axis_np,
        amp_np,
        dt_np,
        band_idx_np,
        is_new_np,
        mask_np,
        y_np,
    ) = build_training_batches(train_events, train_labels, train_run_ids, args.batch, window_step=args.window_step)
    if len(y_np) == 0:
        print("No train batches", file=sys.stderr)
        sys.exit(1)

    (
        val_frame_seq,
        val_proto,
        val_axis,
        val_amp,
        val_dt,
        val_band_idx,
        val_is_new,
        val_mask,
        val_y_np,
    ) = build_training_batches(val_events, val_labels, val_run_ids, args.batch, window_step=args.window_step)

    label_to_idx = {a: i for i, a in enumerate(unique_actions)}
    y_idx = np.array([label_to_idx[y] for y in y_np], dtype=np.int32)
    val_y_idx = np.array([label_to_idx[y] for y in val_y_np], dtype=np.int32) if len(val_y_np) > 0 else None

    # Shuffle train
    rng = np.random.default_rng(args.seed)
    idx = rng.permutation(len(y_np))
    frame_seq_np = frame_seq_np[idx]
    proto_np = proto_np[idx]
    axis_np = axis_np[idx]
    amp_np = amp_np[idx]
    dt_np = dt_np[idx]
    band_idx_np = band_idx_np[idx]
    is_new_np = is_new_np[idx]
    mask_np = mask_np[idx]
    y_idx = y_idx[idx]

    # End-to-end: CSSM + glyph_encoder + head — все обучаются
    model = build_trainable_pipeline(num_classes=num_classes)
    model.compile(
        optimizer=tf.keras.optimizers.Adam(1e-3),
        loss="sparse_categorical_crossentropy",
        metrics=["accuracy"],
    )

    os.makedirs(args.output, exist_ok=True)
    keras_path = os.path.join(args.output, "model.keras")

    callbacks: List[tf.keras.callbacks.Callback] = []
    if len(val_y_np) > 0:
        callbacks.append(
            tf.keras.callbacks.ModelCheckpoint(
                keras_path,
                monitor="val_accuracy",
                save_best_only=True,
                mode="max",
                verbose=1,
            )
        )
        callbacks.append(
            tf.keras.callbacks.EarlyStopping(
                monitor="val_accuracy",
                patience=8,
                restore_best_weights=True,
                mode="max",
                verbose=1,
            )
        )

    fit_kw: Dict[str, Any] = {
        "epochs": args.epochs,
        "batch_size": args.batch,
        "shuffle": True,
        "callbacks": callbacks,
    }
    if len(val_y_np) > 0:
        fit_kw["validation_data"] = (
            [val_frame_seq, val_proto, val_axis, val_amp, val_dt, val_band_idx, val_is_new, val_mask],
            val_y_idx,
        )
    else:
        fit_kw["validation_split"] = 0.2

    model.fit(
        [frame_seq_np, proto_np, axis_np, amp_np, dt_np, band_idx_np, is_new_np, mask_np],
        y_idx,
        **fit_kw,
    )

    # Сохранить финальные веса (если early stopping не сработал — best уже в model)
    model.save(keras_path)
    meta = {
        "format": "ml_queue",
        "num_classes": num_classes,
        "idx_to_id": idx_to_id,
        "id_to_name": id_to_name_str,
    }
    with open(os.path.join(args.output, "meta.json"), "w", encoding="utf-8") as f:
        json.dump(meta, f, indent=2)
    print(f"Saved to {args.output}")


if __name__ == "__main__":
    main()
