#!/usr/bin/env python3
"""
train_glyph_model.py

Multitask sequence trainer:
- run-level action classification (LEFT/RIGHT/BOTH)
- next-token prediction (next glyph_id) as self-supervision

Key fixes:
1) Validation dataset is FINITE (stable real val metrics).
2) Train dataset repeats, but each epoch sees a full pass of (runs * samples_per_run).
3) Added --samples-per-run to avoid tiny steps_per_epoch.
4) If run has > max_events, we random-crop a contiguous window.
5) Confusion-matrix eval iterates over full finite val_ds.
6) Keras 3: callbacks set mode explicitly.
7) Added --input-mode and --drop-leaky-features.
   NOTE: In Keras Functional, ALL model inputs must be connected to outputs.
   We use a zero-connection trick (inside Lambda) to satisfy Keras, without changing numerics.
"""

import argparse
import json
import os
import random
from dataclasses import dataclass
from glob import glob
from typing import Any, Dict, List, Tuple

import numpy as np
import tensorflow as tf


FEATURES_ORDER = [
    "A1", "A2", "A3",
    "DeltaA12", "DeltaA23",
    "T12", "T23",
    "Symmetry",
    "FreqCenter", "Bandwidth",
    "U1", "U2",
    "E1", "E2",
    "AgeSincePrevEvent",
    "EventIndexInRun",
    "RunLength",
]

IDX_AGE = FEATURES_ORDER.index("AgeSincePrevEvent")
IDX_EVENT_IDX = FEATURES_ORDER.index("EventIndexInRun")
IDX_RUNLEN = FEATURES_ORDER.index("RunLength")


@dataclass
class RunSample:
    action_id: int
    action_name: str
    run_path: str
    events: List[str]


def set_seeds(seed: int) -> None:
    random.seed(seed)
    np.random.seed(seed)
    tf.random.set_seed(seed)


def safe_float(x: Any, default: float = 0.0) -> float:
    try:
        return float(x)
    except Exception:
        return default


def safe_int(x: Any, default: int = 0) -> int:
    try:
        return int(x)
    except Exception:
        return default


def parse_action_folder_name(folder: str) -> Tuple[int, str]:
    base = os.path.basename(folder.rstrip("/"))
    if "_" not in base:
        raise ValueError(f"Action folder must be like '1_LEFT': got {base}")
    raw_id_str, name = base.split("_", 1)
    return int(raw_id_str), name


def read_event_json(path: str) -> Dict[str, Any]:
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def order_events_in_run(event_files: List[str]) -> List[str]:
    def key_fn(p: str) -> Tuple[int, float, int]:
        try:
            d = read_event_json(p)
        except Exception:
            base = os.path.splitext(os.path.basename(p))[0]
            return (10**9, 10**18, safe_int(base, 10**9))

        features = d.get("features", None)
        ev_idx = 10**9
        if isinstance(features, list) and len(features) >= len(FEATURES_ORDER):
            ev_idx = safe_int(features[IDX_EVENT_IDX], 10**9)

        meta = d.get("meta", {}) if isinstance(d.get("meta", {}), dict) else {}
        ts = safe_float(meta.get("ts", 10**18), 10**18)

        base = os.path.splitext(os.path.basename(p))[0]
        fidx = safe_int(base, 10**9)

        return (ev_idx, ts, fidx)

    return sorted(event_files, key=key_fn)


def list_runs(dataset_root: str) -> List[RunSample]:
    action_folders = sorted([p for p in glob(os.path.join(dataset_root, "*")) if os.path.isdir(p)])
    runs: List[RunSample] = []
    for action_dir in action_folders:
        raw_id, name = parse_action_folder_name(action_dir)
        run_dirs = sorted([p for p in glob(os.path.join(action_dir, "run_*")) if os.path.isdir(p)])
        for run_dir in run_dirs:
            event_files = sorted(glob(os.path.join(run_dir, "*", "*.json")))
            event_files = [p for p in event_files if os.path.basename(p).lower() != "meta.json"]
            if not event_files:
                continue
            runs.append(RunSample(action_id=raw_id, action_name=name, run_path=run_dir, events=event_files))
    if not runs:
        raise RuntimeError(f"No runs found in {dataset_root}")
    return runs


def normalize_action_ids(runs: List[RunSample]) -> Tuple[List[RunSample], List[str]]:
    raw_ids = sorted(set(r.action_id for r in runs))
    raw_to_norm = {raw: i for i, raw in enumerate(raw_ids)}

    raw_to_name: Dict[int, str] = {}
    for r in runs:
        raw_to_name.setdefault(r.action_id, r.action_name)
    class_names = [raw_to_name.get(raw, f"class_{raw}") for raw in raw_ids]

    out: List[RunSample] = []
    for r in runs:
        out.append(RunSample(
            action_id=raw_to_norm[r.action_id],
            action_name=r.action_name,
            run_path=r.run_path,
            events=r.events,
        ))
    return out, class_names


def find_max_glyph_id(runs: List[RunSample]) -> int:
    max_gid = 0
    for r in runs:
        for gdir in glob(os.path.join(r.run_path, "*")):
            if os.path.isdir(gdir):
                name = os.path.basename(gdir)
                if name.isdigit():
                    max_gid = max(max_gid, int(name))
    return int(max_gid)


def build_run_tensors(
    run: RunSample,
    max_events: int,
    max_glyph_id: int,
    augment: bool,
    rng: np.random.Generator,
    drop_leaky_features: bool,
) -> Tuple[Dict[str, np.ndarray], np.ndarray, np.ndarray, int]:
    event_files = order_events_in_run(run.events)

    # random crop contiguous window if too long
    if len(event_files) > max_events:
        start = int(rng.integers(0, len(event_files) - max_events + 1))
        event_files = event_files[start:start + max_events]
    else:
        event_files = event_files[:max_events]

    T = len(event_files)

    signals = []
    reactions = []
    feats = []
    meta2 = []
    glyph_ids = []
    dt_list = []

    for p in event_files:
        d = read_event_json(p)

        shape = np.asarray(d.get("shape", [0.0] * 64), dtype=np.float32)
        x = np.asarray(d.get("x", [0.0] * 64), dtype=np.float32)
        y = np.asarray(d.get("y", [0.0] * 64), dtype=np.float32)
        z = np.asarray(d.get("z", [0.0] * 64), dtype=np.float32)

        if shape.shape[0] != 64:
            shape = np.resize(shape, (64,)).astype(np.float32)
        if x.shape[0] != 64:
            x = np.resize(x, (64,)).astype(np.float32)
        if y.shape[0] != 64:
            y = np.resize(y, (64,)).astype(np.float32)
        if z.shape[0] != 64:
            z = np.resize(z, (64,)).astype(np.float32)

        sig = np.stack([shape, x, y, z], axis=-1)  # (64,4)

        reaction_valid = bool(d.get("reaction_valid", False))
        reaction = d.get("reaction", None)
        if reaction is None or (isinstance(reaction, list) and len(reaction) == 0):
            reaction = [0.0] * 16
        reaction = np.asarray(reaction, dtype=np.float32)
        if reaction.shape[0] != 16:
            reaction = np.resize(reaction, (16,)).astype(np.float32)

        f = d.get("features", [0.0] * 17)
        f = np.asarray(f, dtype=np.float32)
        if f.shape[0] != 17:
            f = np.resize(f, (17,)).astype(np.float32)

        if drop_leaky_features:
            # classic "leak" sources (position + total length)
            f[IDX_EVENT_IDX] = 0.0
            f[IDX_RUNLEN] = 0.0

        meta = d.get("meta", {}) if isinstance(d.get("meta", {}), dict) else {}
        band_idx = safe_float(meta.get("band_idx", 0.0), 0.0)
        freq_center = safe_float(meta.get("freq_center", f[FEATURES_ORDER.index("FreqCenter")]), 0.0)
        bandwidth = safe_float(meta.get("bandwidth", f[FEATURES_ORDER.index("Bandwidth")]), 0.0)
        reaction_ts = safe_float(d.get("reaction_ts", 0.0), 0.0)
        age = safe_float(f[IDX_AGE], 0.0)

        m2 = np.asarray(
            [band_idx, freq_center, bandwidth, 1.0 if reaction_valid else 0.0, reaction_ts, age],
            dtype=np.float32
        )

        # dt input: we use AgeSincePrevEvent as a proxy (already log1p(ms) in your pipeline)
        dt = np.asarray([age], dtype=np.float32)  # (1,)

        # glyph_id
        if isinstance(meta, dict) and "glyph_id" in meta:
            gid = safe_int(meta["glyph_id"], 0)
        else:
            parent = os.path.basename(os.path.dirname(p))
            gid = safe_int(parent, 0)
        gid = int(max(0, min(gid, max_glyph_id)))

        signals.append(sig)
        reactions.append(reaction)
        feats.append(f)
        meta2.append(m2)
        glyph_ids.append(gid)
        dt_list.append(dt)

    signals = np.asarray(signals, dtype=np.float32)     # (T,64,4)
    reactions = np.asarray(reactions, dtype=np.float32) # (T,16)
    feats = np.asarray(feats, dtype=np.float32)         # (T,17)
    meta2 = np.asarray(meta2, dtype=np.float32)         # (T,6)
    glyph_ids = np.asarray(glyph_ids, dtype=np.int32)   # (T,)
    dt_arr = np.asarray(dt_list, dtype=np.float32)      # (T,1)

    # next-token targets (shift by 1), invalid = -1
    y_next = np.full((T,), -1, dtype=np.int32)
    if T >= 2:
        y_next[:-1] = glyph_ids[1:]

    # weights for next-token: 1 for positions with a target, else 0
    w_next = np.zeros((T,), dtype=np.float32)
    if T >= 2:
        w_next[:-1] = 1.0

    # augmentation (light)
    if augment:
        gain = rng.normal(loc=1.0, scale=0.03)
        noise = rng.normal(loc=0.0, scale=0.005, size=signals.shape).astype(np.float32)
        signals = signals * np.float32(gain) + noise

        feat_jitter = rng.normal(loc=0.0, scale=0.01, size=feats.shape).astype(np.float32)
        feat_jitter[:, IDX_EVENT_IDX] = 0.0
        feat_jitter[:, IDX_RUNLEN] = 0.0
        feats = feats + feat_jitter

        dt_noise = rng.normal(loc=0.0, scale=0.01, size=dt_arr.shape).astype(np.float32)
        dt_arr = dt_arr + dt_noise

    # pad to max_events
    pad = max_events - T
    if pad > 0:
        signals = np.pad(signals, ((0, pad), (0, 0), (0, 0)), mode="constant")
        reactions = np.pad(reactions, ((0, pad), (0, 0)), mode="constant")
        feats = np.pad(feats, ((0, pad), (0, 0)), mode="constant")
        meta2 = np.pad(meta2, ((0, pad), (0, 0)), mode="constant")
        glyph_ids = np.pad(glyph_ids, ((0, pad),), mode="constant", constant_values=0)
        dt_arr = np.pad(dt_arr, ((0, pad), (0, 0)), mode="constant")

        y_next = np.pad(y_next, ((0, pad),), mode="constant", constant_values=-1)
        w_next = np.pad(w_next, ((0, pad),), mode="constant", constant_values=0.0)
        mask = np.concatenate([np.ones((T,), dtype=np.float32), np.zeros((pad,), dtype=np.float32)], axis=0)
    else:
        mask = np.ones((T,), dtype=np.float32)

    inputs = {
        "signal": signals,
        "reaction": reactions,
        "features": feats,
        "meta2": meta2,
        "glyph_id": glyph_ids,
        "dt": dt_arr,
        "mask": mask,
    }
    return inputs, y_next, w_next, T


def make_dataset(
    runs: List[RunSample],
    num_classes: int,
    max_events: int,
    max_glyph_id: int,
    batch_size: int,
    shuffle: bool,
    augment: bool,
    seed: int,
    repeat: bool,
    samples_per_run: int,
    drop_leaky_features: bool,
) -> tf.data.Dataset:
    """
    Generates ONE FULL PASS over runs (finite), optionally repeated by ds.repeat().
    Each run can produce multiple samples (samples_per_run) to avoid steps_per_epoch=1.
    """
    rng = np.random.default_rng(seed)

    def gen_one_pass():
        idxs = list(range(len(runs)))
        if shuffle:
            rng.shuffle(idxs)

        for i in idxs:
            r = runs[i]
            for _ in range(max(1, int(samples_per_run))):
                x, y_next, w_next, _T = build_run_tensors(
                    run=r,
                    max_events=max_events,
                    max_glyph_id=max_glyph_id,
                    augment=augment,
                    rng=rng,
                    drop_leaky_features=drop_leaky_features,
                )
                y_action = np.zeros((num_classes,), dtype=np.float32)
                y_action[r.action_id] = 1.0

                yield x, {"action": y_action, "next_glyph": y_next}, {"action": 1.0, "next_glyph": w_next}

    output_signature = (
        {
            "signal": tf.TensorSpec(shape=(max_events, 64, 4), dtype=tf.float32),
            "reaction": tf.TensorSpec(shape=(max_events, 16), dtype=tf.float32),
            "features": tf.TensorSpec(shape=(max_events, 17), dtype=tf.float32),
            "meta2": tf.TensorSpec(shape=(max_events, 6), dtype=tf.float32),
            "glyph_id": tf.TensorSpec(shape=(max_events,), dtype=tf.int32),
            "dt": tf.TensorSpec(shape=(max_events, 1), dtype=tf.float32),
            "mask": tf.TensorSpec(shape=(max_events,), dtype=tf.float32),
        },
        {
            "action": tf.TensorSpec(shape=(num_classes,), dtype=tf.float32),
            "next_glyph": tf.TensorSpec(shape=(max_events,), dtype=tf.int32),
        },
        {
            "action": tf.TensorSpec(shape=(), dtype=tf.float32),
            "next_glyph": tf.TensorSpec(shape=(max_events,), dtype=tf.float32),
        },
    )

    ds = tf.data.Dataset.from_generator(gen_one_pass, output_signature=output_signature)

    if repeat:
        ds = ds.repeat()

    ds = ds.batch(batch_size, drop_remainder=False)
    ds = ds.prefetch(tf.data.AUTOTUNE)
    return ds


def masked_softmax(logits: tf.Tensor, mask: tf.Tensor, axis: int = 1) -> tf.Tensor:
    logits2 = tf.squeeze(logits, axis=-1)  # (B,T)
    neg = tf.constant(-1e9, dtype=logits2.dtype)
    logits2 = tf.where(mask > 0.0, logits2, neg)
    return tf.nn.softmax(logits2, axis=axis)


def build_model(
    max_events: int,
    num_classes: int,
    max_glyph_id: int,
    input_mode: str,
    embed_dim: int = 16,
    event_dim: int = 128,
    rnn_dim: int = 128,
    dropout: float = 0.2,
) -> tf.keras.Model:
    """
    Builds a model for input_mode but keeps ALL inputs connected (Keras requirement).
    input_mode:
      - signal_only
      - signal_dt
      - signal_reaction
      - signal_reaction_dt
      - all
    """
    inp_signal = tf.keras.Input(shape=(max_events, 64, 4), name="signal")
    inp_reaction = tf.keras.Input(shape=(max_events, 16), name="reaction")
    inp_features = tf.keras.Input(shape=(max_events, 17), name="features")
    inp_meta2 = tf.keras.Input(shape=(max_events, 6), name="meta2")
    inp_gid = tf.keras.Input(shape=(max_events,), dtype=tf.int32, name="glyph_id")
    inp_dt = tf.keras.Input(shape=(max_events, 1), dtype=tf.float32, name="dt")
    inp_mask = tf.keras.Input(shape=(max_events,), dtype=tf.float32, name="mask")

    # event signal encoder (64x4 -> vec)
    sig_in = tf.keras.Input(shape=(64, 4), name="sig_event")
    x = tf.keras.layers.Conv1D(32, 5, padding="same")(sig_in)
    x = tf.keras.layers.BatchNormalization()(x)
    x = tf.keras.layers.Activation("relu")(x)
    x = tf.keras.layers.MaxPool1D(2)(x)

    x = tf.keras.layers.Conv1D(64, 5, padding="same")(x)
    x = tf.keras.layers.BatchNormalization()(x)
    x = tf.keras.layers.Activation("relu")(x)
    x = tf.keras.layers.MaxPool1D(2)(x)

    x = tf.keras.layers.Conv1D(64, 3, padding="same")(x)
    x = tf.keras.layers.BatchNormalization()(x)
    x = tf.keras.layers.Activation("relu")(x)

    a = tf.keras.layers.GlobalAveragePooling1D()(x)
    m = tf.keras.layers.GlobalMaxPooling1D()(x)
    sig_vec = tf.keras.layers.Concatenate()([a, m])  # 128
    sig_vec = tf.keras.layers.Dense(event_dim, activation="relu")(sig_vec)
    sig_vec = tf.keras.layers.Dropout(dropout)(sig_vec)

    event_encoder = tf.keras.Model(sig_in, sig_vec, name="event_signal_encoder")
    sig_encoded = tf.keras.layers.TimeDistributed(event_encoder, name="td_sig")(inp_signal)  # (B,T,event_dim)

    parts = [sig_encoded]

    if input_mode in ("signal_reaction", "signal_reaction_dt", "all"):
        r = tf.keras.layers.Dense(32, activation="relu")(inp_reaction)
        r = tf.keras.layers.BatchNormalization()(r)
        parts.append(r)

    if input_mode in ("signal_dt", "signal_reaction_dt", "all"):
        dt_proj = tf.keras.layers.Dense(8, activation="relu")(inp_dt)
        parts.append(dt_proj)

    vocab = int(max_glyph_id) + 1
    if input_mode == "all":
        f = tf.keras.layers.Concatenate()([inp_features, inp_meta2])  # (B,T,23)
        f = tf.keras.layers.Dense(64, activation="relu")(f)
        f = tf.keras.layers.BatchNormalization()(f)
        parts.append(f)

        gid_emb = tf.keras.layers.Embedding(input_dim=vocab, output_dim=embed_dim, name="glyph_embedding")(inp_gid)
        parts.append(gid_emb)

    ev = tf.keras.layers.Concatenate()(parts)
    ev = tf.keras.layers.Dense(256, activation="relu")(ev)
    ev = tf.keras.layers.Dropout(dropout)(ev)

    seq = tf.keras.layers.Bidirectional(
        tf.keras.layers.GRU(rnn_dim, return_sequences=True),
        name="bigru"
    )(ev)
    seq = tf.keras.layers.Dropout(dropout)(seq)

    # attention pooling for action
    att_logits = tf.keras.layers.Dense(1, name="att_logits")(seq)  # (B,T,1)
    att_w = tf.keras.layers.Lambda(lambda z: masked_softmax(z[0], z[1]), name="att_softmax")([att_logits, inp_mask])
    att_w_exp = tf.keras.layers.Lambda(lambda w: tf.expand_dims(w, axis=-1), name="att_expand")(att_w)

    run_vec = tf.keras.layers.Multiply()([seq, att_w_exp])
    run_vec = tf.keras.layers.Lambda(lambda t: tf.reduce_sum(t, axis=1), name="att_sum")(run_vec)
    run_vec = tf.keras.layers.Dense(128, activation="relu")(run_vec)
    run_vec = tf.keras.layers.Dropout(dropout)(run_vec)

    # --- zero-connection trick: connect ALL inputs to outputs (Keras requirement) ---
    # IMPORTANT: do NOT use tf.cast(inp_gid, ...) outside a Layer.
    def zero_dep(x):
        x = tf.cast(x, tf.float32)
        x = tf.reshape(x, (tf.shape(x)[0], -1))   # (B, N)
        s = tf.reduce_sum(x, axis=1, keepdims=True)  # (B,1)
        return s * 0.0

    z_list = [
        tf.keras.layers.Lambda(zero_dep, name="z_reaction")(inp_reaction),  # (B,1)
        tf.keras.layers.Lambda(zero_dep, name="z_features")(inp_features),
        tf.keras.layers.Lambda(zero_dep, name="z_meta2")(inp_meta2),
        tf.keras.layers.Lambda(zero_dep, name="z_dt")(inp_dt),
        tf.keras.layers.Lambda(zero_dep, name="z_gid")(inp_gid),
        tf.keras.layers.Lambda(zero_dep, name="z_mask")(inp_mask),
    ]
    z = tf.keras.layers.Add(name="z_add")(z_list)  # (B,1)
    z128 = tf.keras.layers.Dense(
        128,
        use_bias=False,
        kernel_initializer="zeros",
        name="zero_connector"
    )(z)  # (B,128), exactly zeros but connected
    run_vec = tf.keras.layers.Add(name="run_plus_zero")([run_vec, z128])

    out_action = tf.keras.layers.Dense(num_classes, activation="softmax", name="action")(run_vec)

    # next glyph head
    ng = tf.keras.layers.Dense(256, activation="relu")(seq)
    ng = tf.keras.layers.Dropout(dropout)(ng)
    out_next = tf.keras.layers.Dense(vocab, activation="softmax", name="next_glyph")(ng)

    return tf.keras.Model(
        inputs={
            "signal": inp_signal,
            "reaction": inp_reaction,
            "features": inp_features,
            "meta2": inp_meta2,
            "glyph_id": inp_gid,
            "dt": inp_dt,
            "mask": inp_mask,
        },
        outputs={"action": out_action, "next_glyph": out_next},
        name=f"glyph_sequence_multitask_{input_mode}",
    )


class MaskedSparseCE(tf.keras.losses.Loss):
    """Sparse categorical CE that ignores y_true == -1."""
    def __init__(self, from_logits: bool = False, name: str = "masked_sparse_ce"):
        super().__init__(name=name, reduction=tf.keras.losses.Reduction.NONE)
        self._ce = tf.keras.losses.SparseCategoricalCrossentropy(from_logits=from_logits, reduction="none")

    def call(self, y_true, y_pred):
        y_true = tf.cast(y_true, tf.int32)
        valid = tf.not_equal(y_true, -1)
        y_safe = tf.where(valid, y_true, tf.zeros_like(y_true))
        loss = self._ce(y_safe, y_pred)  # (B,T)
        loss = tf.where(valid, loss, tf.zeros_like(loss))
        return loss


def confusion_matrix(y_true: np.ndarray, y_pred: np.ndarray, num_classes: int) -> np.ndarray:
    cm = np.zeros((num_classes, num_classes), dtype=np.int64)
    for t, p in zip(y_true, y_pred):
        cm[int(t), int(p)] += 1
    return cm


def per_class_report(cm: np.ndarray, class_names: List[str]) -> None:
    print("\n=== CONFUSION MATRIX (rows=true, cols=pred) ===")
    header = " " * 14 + "".join([f"{i:>10d}" for i in range(cm.shape[1])])
    print(header)
    for i in range(cm.shape[0]):
        row = "".join([f"{cm[i, j]:>10d}" for j in range(cm.shape[1])])
        print(f"{i:>2d}:{class_names[i]:<10s}{row}")

    print("\n=== PER-CLASS METRICS ===")
    for i in range(cm.shape[0]):
        tp = cm[i, i]
        fp = cm[:, i].sum() - tp
        fn = cm[i, :].sum() - tp
        support = cm[i, :].sum()
        p = tp / (tp + fp + 1e-9)
        r = tp / (tp + fn + 1e-9)
        f1 = 2 * p * r / (p + r + 1e-9)
        print(f"- {i:>2d}:{class_names[i]:<10s} support={support:5d}  P={p:.3f}  R={r:.3f}  F1={f1:.3f}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dataset", default="data/trainer_dataset")
    ap.add_argument("--epochs", type=int, default=30)
    ap.add_argument("--batch-size", type=int, default=16)
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--val-runs", type=int, default=3)
    ap.add_argument("--max-events", type=int, default=256)
    ap.add_argument("--no-augment", action="store_true")
    ap.add_argument("--smoothing", type=float, default=0.05)
    ap.add_argument("--lr", type=float, default=1e-3)
    ap.add_argument("--dropout", type=float, default=0.2)
    ap.add_argument("--lambda-next", type=float, default=0.35)
    ap.add_argument("--monitor", choices=["val_action_loss", "val_action_accuracy"], default="val_action_loss")

    ap.add_argument("--samples-per-run", type=int, default=4,
                    help="How many training examples to generate per run per epoch (random-crop + aug).")

    ap.add_argument(
        "--input-mode",
        choices=["signal_only", "signal_dt", "signal_reaction", "signal_reaction_dt", "all"],
        default="signal_only",
        help="Which inputs to actually use (others are connected with zero trick)."
    )

    ap.add_argument(
        "--drop-leaky-features",
        action="store_true",
        help="Zero-out EventIndexInRun and RunLength in features to reduce leakage."
    )

    args = ap.parse_args()

    set_seeds(args.seed)

    print(f"TF {tf.__version__} | Keras {tf.keras.__version__}")

    runs = list_runs(args.dataset)
    runs, class_names = normalize_action_ids(runs)
    num_classes = len(class_names)

    print(f"Found {len(runs)} runs | classes={num_classes}: {class_names}")

    max_gid = find_max_glyph_id(runs)
    vocab = max_gid + 1
    print(f"Max glyph_id (from folders): {max_gid} | vocab={vocab}")

    by_class: Dict[int, List[RunSample]] = {}
    for r in runs:
        by_class.setdefault(r.action_id, []).append(r)
    for cid in by_class:
        random.shuffle(by_class[cid])

    val_runs: List[RunSample] = []
    train_runs: List[RunSample] = []

    k = max(1, int(args.val_runs))
    for cid, items in by_class.items():
        kk = k if len(items) > k else max(1, len(items) // 3)
        val_runs.extend(items[:kk])
        train_runs.extend(items[kk:])

    def counts(rs: List[RunSample]) -> Dict[int, int]:
        out: Dict[int, int] = {}
        for r in rs:
            out[r.action_id] = out.get(r.action_id, 0) + 1
        return out

    print(f"Train runs: {len(train_runs)}  per-class: {counts(train_runs)}")
    print(f"Val runs:   {len(val_runs)}  per-class: {counts(val_runs)}")

    if len(train_runs) < num_classes:
        print("\nWARNING: Very small train set. You may get unstable training.")
        print("         Collect more runs; ideally >= 10 runs per class.\n")

    augment = not args.no_augment

    print(
        f"Mode={args.input_mode} | drop_leaky_features={args.drop_leaky_features} | "
        f"Augmentation: {'OFF' if args.no_augment else 'ON'} | LR: {args.lr} | "
        f"Smoothing(action): {args.smoothing} | lambda_next: {args.lambda_next} | samples_per_run: {args.samples_per_run}"
    )

    train_ds = make_dataset(
        runs=train_runs,
        num_classes=num_classes,
        max_events=args.max_events,
        max_glyph_id=max_gid,
        batch_size=args.batch_size,
        shuffle=True,
        augment=augment,
        seed=args.seed,
        repeat=True,
        samples_per_run=max(1, int(args.samples_per_run)),
        drop_leaky_features=bool(args.drop_leaky_features),
    )

    val_ds = make_dataset(
        runs=val_runs,
        num_classes=num_classes,
        max_events=args.max_events,
        max_glyph_id=max_gid,
        batch_size=args.batch_size,
        shuffle=False,
        augment=False,
        seed=args.seed + 999,
        repeat=False,
        samples_per_run=1,
        drop_leaky_features=bool(args.drop_leaky_features),
    )

    train_examples = max(1, len(train_runs) * max(1, int(args.samples_per_run)))
    train_steps = max(1, int(np.ceil(train_examples / max(1, args.batch_size))))
    val_examples = max(1, len(val_runs))
    val_batches = max(1, int(np.ceil(val_examples / max(1, args.batch_size))))
    print(f"steps_per_epoch={train_steps} | (val batches ~ {val_batches})")

    model = build_model(
        max_events=args.max_events,
        num_classes=num_classes,
        max_glyph_id=max_gid,
        input_mode=args.input_mode,
        embed_dim=16,
        event_dim=128,
        rnn_dim=128,
        dropout=args.dropout,
    )
    model.summary()

    loss_action = tf.keras.losses.CategoricalCrossentropy(label_smoothing=float(args.smoothing))
    loss_next = MaskedSparseCE(from_logits=False)
    opt = tf.keras.optimizers.Adam(learning_rate=args.lr)

    os.makedirs("data/saved_model", exist_ok=True)
    ckpt_path = os.path.join("data/saved_model", "model_multitask.keras")

    monitor = args.monitor
    mode = "min" if monitor.endswith("_loss") else "max"

    cbs = [
        tf.keras.callbacks.ModelCheckpoint(
            ckpt_path,
            monitor=monitor,
            mode=mode,
            save_best_only=True,
            save_weights_only=False,
            verbose=1,
        ),
        tf.keras.callbacks.EarlyStopping(
            monitor=monitor,
            mode=mode,
            patience=10,
            restore_best_weights=True,
            verbose=1,
        ),
        tf.keras.callbacks.ReduceLROnPlateau(
            monitor=monitor,
            mode=mode,
            factor=0.5,
            patience=3,
            min_lr=1e-6,
            verbose=1,
        ),
    ]

    model.compile(
        optimizer=opt,
        loss={"action": loss_action, "next_glyph": loss_next},
        loss_weights={"action": 1.0, "next_glyph": float(args.lambda_next)},
        metrics={"action": ["accuracy"]},
    )

    model.fit(
        train_ds,
        validation_data=val_ds,
        epochs=args.epochs,
        steps_per_epoch=train_steps,
        callbacks=cbs,
        verbose=1,
    )

    # confusion matrix on full finite val_ds
    y_true: List[int] = []
    y_pred: List[int] = []

    for xb, yb, _wb in val_ds:
        probs = model.predict(xb, verbose=0)["action"]
        y_true.extend(np.argmax(yb["action"].numpy(), axis=-1).tolist())
        y_pred.extend(np.argmax(probs, axis=-1).tolist())

    y_true_arr = np.asarray(y_true, dtype=np.int32)
    y_pred_arr = np.asarray(y_pred, dtype=np.int32)

    cm = confusion_matrix(y_true_arr, y_pred_arr, num_classes=num_classes)
    per_class_report(cm, class_names)

    print("\nSaved best to:", ckpt_path)


if __name__ == "__main__":
    main()
