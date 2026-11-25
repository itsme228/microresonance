#!/usr/bin/env python3
"""Export glyph_classifier (model.keras) to ONNX for Go inference.

Требования (macOS): tensorflow 2.19.x (2.20 падает), numpy<2.
  pip install -r requirements-export.txt
"""

import argparse
import os
import subprocess
import sys

# До импорта TF
os.environ["TF_CPP_MIN_LOG_LEVEL"] = "3"
os.environ["OMP_NUM_THREADS"] = "1"


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--model-dir", default="data/saved_model", help="Directory with model.keras")
    ap.add_argument("--output", default=None, help="Output .onnx path (default: model_dir/model.onnx)")
    args = ap.parse_args()

    keras_path = os.path.join(args.model_dir, "model.keras")
    if not os.path.isfile(keras_path):
        print(f"Model not found: {keras_path}", file=sys.stderr)
        sys.exit(1)

    out_path = args.output or os.path.join(args.model_dir, "model.onnx")
    savedmodel_dir = os.path.join(args.model_dir, "saved_model_export")

    print(f"Loading {keras_path}...", flush=True)
    from tensorflow import keras
    import tensorflow as tf
    model = keras.models.load_model(keras_path)
    print("Saving SavedModel...", flush=True)
    tf.saved_model.save(model, savedmodel_dir)

    print(f"Converting to ONNX {out_path}...", flush=True)
    r = subprocess.run(
        [sys.executable, "-m", "tf2onnx.convert", "--saved-model", savedmodel_dir, "--output", out_path, "--opset", "14"],
        capture_output=True,
        text=True,
        timeout=120,
    )
    if r.returncode != 0:
        print(r.stderr or r.stdout, file=sys.stderr)
        print("Tip: pip install -r requirements-export.txt", file=sys.stderr)
        sys.exit(1)

    if os.path.isfile(out_path):
        print(f"OK: {out_path}")
    else:
        print("Export failed: no output file", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
