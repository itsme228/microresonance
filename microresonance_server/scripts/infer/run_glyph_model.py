#!/usr/bin/env python3
"""
Glyph classifier inference: reads events from stdin JSON, outputs predictions.

Input format (one JSON per line):
{
    "shape": [64 floats],
    "x": [64 floats],
    "y": [64 floats],
    "z": [64 floats],
    "features": [17 floats],
    "reaction": [16 floats]
}

Output format (one JSON per line):
{
    "action_id": int,
    "action_name": "LEFT",
    "confidence": 0.95,
    "probs": [0.02, 0.95, 0.03]
}
"""

import json
import math
import os
import sys
from typing import Any, Dict, List, Optional

import numpy as np

# Suppress TF warnings
os.environ["TF_CPP_MIN_LOG_LEVEL"] = "2"

import tensorflow as tf
from tensorflow import keras

# Constants (must match train_glyph_model.py)
SHAPE_DIM = 64
AXES_DIM_DEFAULT = 32  # 32 or 64 from meta.json
FEATURES_DIM = 17
REACTION_DIM = 16


def pad_or_trunc(arr: List[float], size: int) -> List[float]:
    """Pad with zeros or truncate to fixed size. Must match train_glyph_model._pad_or_trunc."""
    arr = list(arr)[:size]
    while len(arr) < size:
        arr.append(0.0)
    return arr


def load_model(model_dir: str) -> tuple:
    """Load model and metadata."""
    keras_path = os.path.join(model_dir, "model.keras")
    meta_path = os.path.join(model_dir, "meta.json")

    if not os.path.isfile(keras_path):
        raise FileNotFoundError(f"Model not found: {keras_path}")

    model = keras.models.load_model(keras_path)

    meta = {}
    if os.path.isfile(meta_path):
        with open(meta_path, "r", encoding="utf-8") as f:
            meta = json.load(f)

    return model, meta


def preprocess_event(
    ev: Dict[str, Any],
    feat_mean: Optional[np.ndarray] = None,
    feat_std: Optional[np.ndarray] = None,
    axes_dim: int = AXES_DIM_DEFAULT,
) -> Dict[str, np.ndarray]:
    """Convert event dict to model inputs."""
    shape = pad_or_trunc(ev.get("shape", []), SHAPE_DIM)
    x = pad_or_trunc(ev.get("x", [])[:axes_dim], axes_dim)
    y = pad_or_trunc(ev.get("y", [])[:axes_dim], axes_dim)
    z = pad_or_trunc(ev.get("z", [])[:axes_dim], axes_dim)
    features = pad_or_trunc(ev.get("features", []), FEATURES_DIM)
    reaction = pad_or_trunc(ev.get("reaction", []), REACTION_DIM)

    features_arr = np.array([features], dtype=np.float32)
    # Z-score normalization (feat_mean/feat_std from meta include train's +1e-8)
    if feat_mean is not None and feat_std is not None:
        features_arr = (features_arr - feat_mean) / feat_std

    return {
        "shape": np.array([shape], dtype=np.float32),
        "axis_x": np.array([x], dtype=np.float32),
        "axis_y": np.array([y], dtype=np.float32),
        "axis_z": np.array([z], dtype=np.float32),
        "features": features_arr,
        "reaction": np.array([reaction], dtype=np.float32),
    }


def main() -> None:
    # model_dir: argv[1] (как run_ml_queue_model.py) или MODEL_DIR env
    model_dir = "data/saved_model"
    if len(sys.argv) >= 2:
        model_dir = os.path.abspath(sys.argv[1])
    else:
        model_dir = os.environ.get("MODEL_DIR", model_dir)

    try:
        model, meta = load_model(model_dir)
    except FileNotFoundError as e:
        print(json.dumps({"error": str(e)}), file=sys.stderr)
        sys.exit(1)

    idx_to_id = meta.get("idx_to_id", [])
    id_to_name = meta.get("id_to_name", {})
    axes_dim = int(meta.get("axes_dim", AXES_DIM_DEFAULT))

    # Load normalization params
    feat_mean = None
    feat_std = None
    if "features_mean" in meta and "features_std" in meta:
        feat_mean = np.array(meta["features_mean"], dtype=np.float32).reshape(1, -1)
        feat_std = np.array(meta["features_std"], dtype=np.float32).reshape(1, -1)

    # Read events from stdin
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            ev = json.loads(line)
        except json.JSONDecodeError as e:
            print(json.dumps({"error": f"JSON parse error: {e}"}))
            sys.stdout.flush()
            continue

        # Поддержка event_glyph от Go (shape, x, y, z, features вложены)
        if ev.get("type") == "event_glyph":
            ev = {
                "shape": ev.get("shape", []),
                "x": ev.get("x", []),
                "y": ev.get("y", []),
                "z": ev.get("z", []),
                "features": ev.get("features", []),
                "reaction": ev.get("reaction", []),  # обычно пусто от Go
                "dt": ev.get("dt", 0.0),  # секунды с предыдущего события
            }
            # Features 14-16: AgeSincePrevEvent, EventIndexInRun, RunLength.
            # Подставляем только если пришли нули (production: Go их не задаёт).
            # Иначе используем реальные данные из JSON/датасета.
            feat = ev.get("features", [])
            if len(feat) >= 17 and feat[14] == 0 and feat[15] == 0 and feat[16] == 0:
                dt_sec = float(ev.get("dt", 0.0))
                feat = list(feat)
                feat[14] = math.log1p(dt_sec * 1000)
                feat[15] = 1.0
                feat[16] = 1.0
                ev["features"] = feat

        inputs = preprocess_event(ev, feat_mean, feat_std, axes_dim)

        try:
            probs = model.predict(inputs, verbose=0)[0]
        except Exception as e:
            print(json.dumps({"error": f"Prediction error: {e}"}))
            sys.stdout.flush()
            continue

        pred_idx = int(np.argmax(probs))
        confidence = float(probs[pred_idx])

        action_id = idx_to_id[pred_idx] if pred_idx < len(idx_to_id) else pred_idx
        action_name = id_to_name.get(str(action_id), f"action_{action_id}")

        result = {
            "action_id": action_id,
            "action_name": action_name,
            "confidence": round(confidence, 4),
            "probs": [round(float(p), 4) for p in probs],
        }
        print(json.dumps(result))
        sys.stdout.flush()


if __name__ == "__main__":
    main()
