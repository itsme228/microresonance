#!/usr/bin/env python3
"""
Features-only classifier: features (17) + reaction (16) = 33 inputs.
Simplified model for real-time inference when full shape data is not available.

Architecture: MLP with dropout and batch normalization.
"""

import argparse
import json
import os
import sys
from collections import defaultdict
from typing import Any, Dict, List, Set, Tuple

import numpy as np
import tensorflow as tf
from tensorflow import keras
from keras import layers

# Constants
FEATURES_DIM = 17
REACTION_DIM = 16
DATASET_DIR = "data/trainer_dataset"
OUTPUT_DIR = "data/saved_model_features"


def load_dataset(dataset_dir: str) -> Tuple[List[Dict], List[int], List[str], Dict[int, str]]:
    """Load events from trainer_dataset. Returns (events, labels, run_ids, id_to_name)."""
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
                if not fname.endswith(".json") or fname == "meta.json":
                    continue
                path = os.path.join(root, fname)
                # Extract run_id from path: include action dir so 1_LEFT/run_001 != 2_RIGHT/run_001
                run_id = f"{dirname}/run_000"
                for part in path.replace("\\", "/").split("/"):
                    if part.startswith("run_"):
                        run_id = f"{dirname}/{part}"
                        break
                try:
                    with open(path, "r", encoding="utf-8") as f:
                        s = json.load(f)
                except (json.JSONDecodeError, OSError):
                    continue

                features = s.get("features", [])
                reaction = s.get("reaction", [])

                # Require features
                if len(features) < 10:
                    continue

                # Pad/truncate to fixed sizes
                features = _pad_or_trunc(features, FEATURES_DIM)
                reaction = _pad_or_trunc(reaction, REACTION_DIM)

                events.append({
                    "features": features,
                    "reaction": reaction,
                })
                labels.append(aid)
                run_ids.append(run_id)

    return events, labels, run_ids, id_to_name


def _pad_or_trunc(arr: List[float], size: int) -> List[float]:
    """Pad with zeros or truncate to fixed size."""
    arr = list(arr)[:size]
    while len(arr) < size:
        arr.append(0.0)
    return arr


def stratified_split_runs(
    labels: List[int],
    run_ids: List[str],
    val_frac: float,
    seed: int,
) -> Tuple[Set[str], Set[str]]:
    """Stratified split by run: val_frac of runs per class go to val."""
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


def build_arrays(
    events: List[Dict],
    labels: List[int],
) -> Tuple[np.ndarray, np.ndarray, np.ndarray]:
    """Convert events to numpy arrays."""
    n = len(events)
    feat_arr = np.zeros((n, FEATURES_DIM), dtype=np.float32)
    react_arr = np.zeros((n, REACTION_DIM), dtype=np.float32)
    y_labels = np.array(labels, dtype=np.int32)

    for i, ev in enumerate(events):
        feat_arr[i] = ev["features"]
        react_arr[i] = ev["reaction"]

    return feat_arr, react_arr, y_labels


def normalize_features(
    train_feat: np.ndarray,
    val_feat: np.ndarray,
) -> Tuple[np.ndarray, np.ndarray, np.ndarray, np.ndarray]:
    """Z-score normalization per feature (computed on train, applied to val).
    Returns (train_norm, val_norm, mean, std) for saving to meta.
    """
    mean = train_feat.mean(axis=0, keepdims=True)
    std = train_feat.std(axis=0, keepdims=True) + 1e-8
    train_norm = (train_feat - mean) / std
    val_norm = (val_feat - mean) / std
    return train_norm, val_norm, mean.flatten(), std.flatten()


def build_model(num_classes: int) -> keras.Model:
    """
    Build features-only classifier:
    - Features (17) → normalized → MLP
    - Reaction (16) → MLP
    - Fusion → classifier
    """
    inp_feat = layers.Input(shape=(FEATURES_DIM,), name="features")
    inp_react = layers.Input(shape=(REACTION_DIM,), name="reaction")

    # Features branch
    ft = layers.Dense(64, activation="relu")(inp_feat)
    ft = layers.BatchNormalization()(ft)
    ft = layers.Dropout(0.3)(ft)
    ft = layers.Dense(32, activation="relu")(ft)
    ft = layers.Dropout(0.2)(ft)

    # Reaction branch
    rt = layers.Dense(32, activation="relu")(inp_react)
    rt = layers.BatchNormalization()(rt)
    rt = layers.Dropout(0.2)(rt)
    rt = layers.Dense(16, activation="relu")(rt)

    # Fusion
    fused = layers.Concatenate()([ft, rt])  # 32 + 16 = 48
    fused = layers.Dense(32, activation="relu")(fused)
    fused = layers.BatchNormalization()(fused)
    fused = layers.Dropout(0.3)(fused)
    out = layers.Dense(num_classes, activation="softmax", name="output")(fused)

    return keras.Model(
        inputs=[inp_feat, inp_react],
        outputs=out,
        name="features_classifier",
    )


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dataset", default=DATASET_DIR, help="Dataset directory")
    ap.add_argument("--output", default=OUTPUT_DIR, help="Output model directory")
    ap.add_argument("--epochs", type=int, default=100)
    ap.add_argument("--batch", type=int, default=32)
    ap.add_argument("--val-frac", type=float, default=0.2)
    ap.add_argument("--seed", type=int, default=42)
    args = ap.parse_args()

    print(f"Loading dataset from {args.dataset}...")
    events, labels, run_ids, id_to_name = load_dataset(args.dataset)
    if not events:
        print("No events in dataset. Run calibration first.", file=sys.stderr)
        sys.exit(1)

    print(f"Loaded {len(events)} events, {len(set(labels))} classes")
    for aid, name in sorted(id_to_name.items()):
        count = sum(1 for l in labels if l == aid)
        print(f"  {aid}: {name} ({count} events)")

    # Stratified split by run
    train_runs, val_runs = stratified_split_runs(labels, run_ids, args.val_frac, args.seed)
    print(f"Train runs: {len(train_runs)}, Val runs: {len(val_runs)}")

    train_events = [e for e, r in zip(events, run_ids) if r in train_runs]
    train_labels = [l for l, r in zip(labels, run_ids) if r in train_runs]
    val_events = [e for e, r in zip(events, run_ids) if r in val_runs]
    val_labels = [l for l, r in zip(labels, run_ids) if r in val_runs]

    print(f"Train: {len(train_events)}, Val: {len(val_events)}")

    # Build arrays
    tr_feat, tr_react, tr_labels = build_arrays(train_events, train_labels)
    va_feat, va_react, va_labels = build_arrays(val_events, val_labels)

    # Normalize features (z-score на train, применить к val)
    tr_feat, va_feat, feat_mean, feat_std = normalize_features(tr_feat, va_feat)

    # Map labels to indices
    unique_actions = sorted(set(labels))
    num_classes = len(unique_actions)
    label_to_idx = {a: i for i, a in enumerate(unique_actions)}
    tr_y_idx = np.array([label_to_idx[y] for y in tr_labels], dtype=np.int32)
    va_y_idx = np.array([label_to_idx[y] for y in va_labels], dtype=np.int32)

    # Build model
    model = build_model(num_classes)
    model.summary()

    model.compile(
        optimizer=keras.optimizers.Adam(1e-3),
        loss="sparse_categorical_crossentropy",
        metrics=["accuracy"],
    )

    # Callbacks
    os.makedirs(args.output, exist_ok=True)
    keras_path = os.path.join(args.output, "model.keras")
    callbacks = [
        keras.callbacks.ModelCheckpoint(
            keras_path,
            monitor="val_accuracy",
            save_best_only=True,
            mode="max",
            verbose=1,
        ),
        keras.callbacks.EarlyStopping(
            monitor="val_accuracy",
            patience=15,
            restore_best_weights=True,
            mode="max",
            verbose=1,
        ),
        keras.callbacks.ReduceLROnPlateau(
            monitor="val_loss",
            factor=0.5,
            patience=5,
            min_lr=1e-6,
            verbose=1,
        ),
    ]

    # Class weights for imbalanced data (computed manually)
    unique_classes, class_counts = np.unique(tr_y_idx, return_counts=True)
    n_samples = len(tr_y_idx)
    n_classes_actual = len(unique_classes)
    class_weight_dict = {
        i: n_samples / (n_classes_actual * count) for i, count in zip(unique_classes, class_counts)
    }
    print(f"Class weights: {class_weight_dict}")

    # Train
    history = model.fit(
        [tr_feat, tr_react],
        tr_y_idx,
        validation_data=([va_feat, va_react], va_y_idx),
        epochs=args.epochs,
        batch_size=args.batch,
        callbacks=callbacks,
        shuffle=True,
        class_weight=class_weight_dict,
    )

    # Save final model and meta (including normalization params)
    model.save(keras_path)
    meta = {
        "format": "features_classifier",
        "num_classes": num_classes,
        "idx_to_id": unique_actions,
        "id_to_name": {str(k): v for k, v in id_to_name.items()},
        "features_mean": feat_mean.tolist(),
        "features_std": feat_std.tolist(),
    }
    with open(os.path.join(args.output, "meta.json"), "w", encoding="utf-8") as f:
        json.dump(meta, f, indent=2)

    print(f"\nSaved to {args.output}")
    print(f"Best val_accuracy: {max(history.history.get('val_accuracy', [0])):.4f}")


if __name__ == "__main__":
    main()
