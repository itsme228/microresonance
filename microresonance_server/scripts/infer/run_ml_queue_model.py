#!/usr/bin/env python3
"""
State-Space + Event Transformer model inference.

Reads JSON lines from stdin (frame_summary, event_glyph, event_reaction).
Buffers, collects state + events, calls model, outputs prediction.
"""

import json
import os
import sys
from collections import deque
from typing import Any, Dict, List, Optional, Tuple

import numpy as np
import tensorflow as tf

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from state_space_event_model import (
    build_cssm,
    build_glyph_encoder,
    build_reaction_encoder,
    build_full_model,
    build_trainable_pipeline,
    NUM_BANDS,
    CSSM_BAND_DIM,
    AXIS_RATIO_DIM,
    PROTO_SIG_DIM,
    AXIS_SIG_DIM,
    PHASE_VEC_DIM,
    D_EVENT,
    W_SHORT_EVENTS,
    W_SHORT_SEC,
    W_MID_FRAMES,
)


def load_meta(model_dir: str) -> Dict[str, Any]:
    path = os.path.join(model_dir, "meta.json")
    if not os.path.isfile(path):
        return {}
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def to_arr(x: List[float], size: int, default: float = 0.0) -> np.ndarray:
    a = np.array((x or [])[:size], dtype=np.float32)
    if len(a) < size:
        a = np.pad(a, (0, size - len(a)), constant_values=default)
    return a


def main() -> None:
    if len(sys.argv) < 2:
        print("Usage: run_ml_queue_model.py <model_dir>", file=sys.stderr)
        sys.exit(1)
    model_dir = os.path.abspath(sys.argv[1])
    if not os.path.isdir(model_dir):
        print(f"Not a directory: {model_dir}", file=sys.stderr)
        sys.exit(1)

    meta = load_meta(model_dir)
    format_type = meta.get("format", "ml_queue")
    num_classes = int(meta.get("num_classes", 2))
    idx_to_id = meta.get("idx_to_id", [0, 1])
    id_to_name = meta.get("id_to_name", {"0": "rest", "1": "active"})

    # Load model
    model = None
    cssm = None
    glyph_enc = None
    reaction_enc = None
    use_raw_inputs = False  # True = trainable_pipeline (proto, axis, ...), False = head only (state, events, mask)

    if format_type != "ml_queue":
        print(f"Model format '{format_type}' is not ml_queue. Retrain: python3 train_ml_queue_model.py", file=sys.stderr)

    keras_path = os.path.join(model_dir, "model.keras")
    if os.path.isfile(keras_path):
        try:
            loaded = tf.keras.models.load_model(keras_path)
            if hasattr(loaded, "input_names") and loaded.input_names:
                if "proto_sig" in loaded.input_names:
                    model = loaded
                    use_raw_inputs = True
                elif "state" in loaded.input_names or "events" in loaded.input_names:
                    model = loaded
        except Exception:
            pass

    if model is None:
        cssm = build_cssm()
        glyph_enc = build_glyph_encoder()
        reaction_enc = build_reaction_encoder()
        model = build_full_model(num_classes=num_classes)
        if format_type == "ml_queue":
            weights_path = os.path.join(model_dir, "variables", "variables")
            if os.path.isfile(weights_path) or os.path.isfile(weights_path + ".index"):
                try:
                    model.load_weights(weights_path)
                except Exception:
                    pass

    if model is None:
        print("No model", file=sys.stderr)
        sys.exit(1)

    if use_raw_inputs:
        cssm = None
        glyph_enc = None
        reaction_enc = None

    # Buffers: W_mid for FrameSummary, W_short by time for events
    frame_buffer: deque = deque(maxlen=W_MID_FRAMES)  # (t1, frame_dict)
    events: deque = deque(maxlen=256)  # (ts, "glyph"|"reaction", data) — time filter in run_inference
    last_ts = 0.0

    def frame_to_cssm_features(f: Dict) -> np.ndarray:
        """One frame → vector for CSSM: band[0..6], total, quality, axis[0..6], exciter, am_freq."""
        band = to_arr(f.get("band_energy", []), CSSM_BAND_DIM)
        total = float(f.get("total_energy", 0.0)) + 1e-12
        quality = float(f.get("quality", 1.0))
        axis = to_arr(f.get("axis_ratio", []), AXIS_RATIO_DIM)
        exciter = float(min(15, max(0, int(f.get("exciter_id", 0)))))
        am_freq = float(f.get("am_freq_hz", 0.0))
        return np.concatenate([band, [total], [quality], axis, [exciter], [am_freq]], dtype=np.float32)

    def run_inference() -> Dict:
        if len(frame_buffer) == 0 and len(events) == 0:
            return {"action_id": 0, "confidence": 0.0, "action_name": ""}

        # Events in W_short (4s) — by time, not by count
        now_ms = last_ts
        cutoff_ms = now_ms - W_SHORT_SEC * 1000
        event_list = [e for e in events if e[0] >= cutoff_ms]
        event_list = sorted(event_list, key=lambda x: x[0])[-W_SHORT_EVENTS:]

        # Frame sequence for CSSM (or for full pipeline)
        if len(frame_buffer) > 0:
            frame_seq = np.array([frame_to_cssm_features(f) for _, f in frame_buffer], dtype=np.float32)
            frame_seq = frame_seq.reshape(1, len(frame_seq), -1)
        else:
            feat_dim = CSSM_BAND_DIM + 1 + 1 + AXIS_RATIO_DIM + 2
            frame_seq = np.zeros((1, 1, feat_dim), dtype=np.float32)

        if use_raw_inputs:
            # Trainable pipeline: frame_seq + raw events (proto, axis, amp, dt, band_idx, is_new, mask)
            proto_list = []
            axis_list = []
            amp_list = []
            dt_list = []
            band_list = []
            is_new_list = []
            prev_ts = 0
            for ts, etype, data in event_list:
                if etype == "glyph":
                    proto_list.append(to_arr(data.get("proto_sig", []), PROTO_SIG_DIM))
                    axis_list.append(to_arr(data.get("axis_sig", []), AXIS_SIG_DIM))
                    amp_list.append(data.get("amp", 0.9))
                    dt_list.append(max(0, (ts - prev_ts) / 1000.0))
                    band_list.append(min(NUM_BANDS, max(0, int(data.get("band_idx", 0)))))
                    is_new_list.append(1.0 if data.get("is_new_id") else 0.0)
                else:
                    proto_list.append(np.zeros(PROTO_SIG_DIM, dtype=np.float32))
                    axis_list.append(np.zeros(AXIS_SIG_DIM, dtype=np.float32))
                    amp_list.append(0.9)
                    dt_list.append(max(0, (ts - prev_ts) / 1000.0))
                    band_list.append(min(NUM_BANDS, max(0, int(data.get("band_idx", 0)))))
                    is_new_list.append(0.0)
                prev_ts = ts
            n = len(event_list)
            while len(proto_list) < W_SHORT_EVENTS:
                proto_list.append(np.zeros(PROTO_SIG_DIM, dtype=np.float32))
                axis_list.append(np.zeros(AXIS_SIG_DIM, dtype=np.float32))
                amp_list.append(0.0)
                dt_list.append(0.0)
                band_list.append(0)
                is_new_list.append(0.0)
            proto_np = np.array(proto_list[:W_SHORT_EVENTS], dtype=np.float32).reshape(1, W_SHORT_EVENTS, -1)
            axis_np = np.array(axis_list[:W_SHORT_EVENTS], dtype=np.float32).reshape(1, W_SHORT_EVENTS, -1)
            amp_np = np.array(amp_list[:W_SHORT_EVENTS], dtype=np.float32).reshape(1, W_SHORT_EVENTS, 1)
            dt_np = np.array(dt_list[:W_SHORT_EVENTS], dtype=np.float32).reshape(1, W_SHORT_EVENTS, 1)
            band_idx_np = np.array(band_list[:W_SHORT_EVENTS], dtype=np.int32).reshape(1, W_SHORT_EVENTS, 1)
            is_new_np = np.array(is_new_list[:W_SHORT_EVENTS], dtype=np.float32).reshape(1, W_SHORT_EVENTS, 1)
            mask_np = np.array([1.0] * n + [0.0] * (W_SHORT_EVENTS - n), dtype=np.float32).reshape(1, W_SHORT_EVENTS)
            try:
                probs = model.predict(
                    [frame_seq, proto_np, axis_np, amp_np, dt_np, band_idx_np, is_new_np, mask_np],
                    verbose=0,
                )[0]
            except Exception as e:
                print(f"Inference error: {e}", file=sys.stderr)
                return {"action_id": 0, "confidence": 0.0, "action_name": ""}
        else:
            # Legacy: state + event embeddings + mask
            if cssm is not None and len(frame_buffer) > 0:
                state = cssm(frame_seq)
            else:
                state = np.zeros((1, 64), dtype=np.float32)
            event_embs = []
            for _, etype, data in event_list:
                if etype == "glyph":
                    proto = to_arr(data.get("proto_sig", []), PROTO_SIG_DIM)
                    axis = to_arr(data.get("axis_sig", []), AXIS_SIG_DIM)
                    amp = np.array([[data.get("amp", 0.0)]], dtype=np.float32)
                    dt = np.array([[data.get("dt", 0.0)]], dtype=np.float32)
                    band_idx = np.array([[min(NUM_BANDS, max(0, int(data.get("band_idx", 0))))]], dtype=np.int32)
                    is_new = np.array([[1.0 if data.get("is_new_id") else 0.0]], dtype=np.float32)
                    if glyph_enc is not None:
                        emb = glyph_enc([proto.reshape(1, -1), axis.reshape(1, -1), amp, dt, band_idx, is_new])
                    else:
                        emb = np.zeros((1, D_EVENT), dtype=np.float32)
                    event_embs.append(emb.numpy()[0] if hasattr(emb, 'numpy') else emb[0])
                else:
                    phase = to_arr(data.get("phase_vec16", []), PHASE_VEC_DIM)
                    energy = np.array([[data.get("total_energy", 0.0) + 1e-12]], dtype=np.float32)
                    dt = np.array([[data.get("dt", 0.0)]], dtype=np.float32)
                    band_idx = np.array([[min(NUM_BANDS, max(0, int(data.get("band_idx", 0))))]], dtype=np.int32)
                    exc = np.array([[min(15, max(0, int(data.get("exciter_id", 0))))]], dtype=np.int32)
                    if reaction_enc is not None:
                        emb = reaction_enc([phase.reshape(1, -1), energy, dt, band_idx, exc])
                    else:
                        emb = np.zeros((1, D_EVENT), dtype=np.float32)
                    event_embs.append(emb.numpy()[0] if hasattr(emb, 'numpy') else emb[0])
            n = len(event_embs)
            events_arr = np.zeros((1, W_SHORT_EVENTS, D_EVENT), dtype=np.float32)
            mask_arr = np.zeros((1, W_SHORT_EVENTS), dtype=np.float32)
            for i, emb in enumerate(event_embs):
                events_arr[0, i, :] = emb
                mask_arr[0, i] = 1.0
            state_in = state.numpy() if hasattr(state, 'numpy') else state
            if len(state_in.shape) == 1:
                state_in = state_in.reshape(1, -1)
            try:
                if hasattr(model, 'predict'):
                    probs = model.predict([state_in, events_arr, mask_arr], verbose=0)[0]
                else:
                    out = model(
                        state=tf.constant(state_in, dtype=tf.float32),
                        events=tf.constant(events_arr, dtype=tf.float32),
                        event_mask=tf.constant(mask_arr, dtype=tf.float32),
                    )
                    probs = out[next(iter(out))].numpy()[0]
            except Exception as e:
                print(f"Inference error: {e}", file=sys.stderr)
                return {"action_id": 0, "confidence": 0.0, "action_name": ""}

        idx = int(np.argmax(probs))
        conf = float(probs[idx])
        action_id = idx_to_id[idx] if idx < len(idx_to_id) else idx
        action_name = id_to_name.get(str(action_id), id_to_name.get(action_id, f"action_{action_id}"))
        return {"action_id": action_id, "confidence": conf, "action_name": action_name}

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            continue

        msg_type = req.get("type", "")
        if msg_type == "frame_summary":
            t1 = req.get("t1", req.get("t0", 0) + 1000)
            frame_buffer.append((t1, req))
            last_ts = t1
        elif msg_type == "event_glyph":
            events.append((req.get("t", last_ts), "glyph", req))
            last_ts = req.get("t", last_ts)
        elif msg_type == "event_reaction":
            events.append((req.get("t_cycle_end", last_ts), "reaction", req))
            last_ts = req.get("t_cycle_end", last_ts)
        else:
            out = {"action_id": 0, "confidence": 0.0, "action_name": ""}
            print(json.dumps(out), flush=True)
            continue

        out = run_inference()
        print(json.dumps(out), flush=True)


if __name__ == "__main__":
    main()
