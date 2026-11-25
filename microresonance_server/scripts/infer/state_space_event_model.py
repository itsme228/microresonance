#!/usr/bin/env python3
"""
State-Space + Event Transformer — ML architecture.

Level 1: CSSM (FrameSummary → state_t).
Level 2: Event Transformer (open-vocab by protoSig/phaseVec) + head.
"""

from typing import Any, Dict, List, Optional, Tuple

import numpy as np
import tensorflow as tf

NUM_BANDS = 8  # for encoders (band_idx 0..7)
CSSM_BAND_DIM = 7  # bandEnergy[0..6] per spec
AXIS_RATIO_DIM = 7  # axis_ratio by bands
PROTO_SIG_DIM = 8
AXIS_SIG_DIM = 4
PHASE_VEC_DIM = 16
D_STATE = 64
D_EVENT = 64
D_HEAD = 32
NUM_HEADS = 4
W_SHORT_EVENTS = 32
W_SHORT_SEC = 4  # event window, seconds
W_MID_SEC = 30  # FrameSummary window, seconds
W_MID_FRAMES = 30  # ~1 frame/sec


def build_cssm(
    band_dim: int = CSSM_BAND_DIM,
    axis_dim: int = AXIS_RATIO_DIM,
    d_state: int = D_STATE,
) -> tf.keras.Model:
    """
    Continuous State Model: GRU over FrameSummary sequence.
    Input: (batch, seq_len, bandEnergy[0..6]+totalEnergy+quality+axisRatio+exciter+am_freq).
    Output: state_t (mode vector).
    """
    feat_dim = band_dim + 1 + 1 + axis_dim + 2  # band, total, quality, axis, exciter, am_freq
    inp_seq = tf.keras.layers.Input(shape=(None, feat_dim), name="frame_sequence")
    x = tf.keras.layers.Lambda(lambda t: tf.math.log1p(tf.maximum(t, 0.0)))(inp_seq[:, :, : band_dim + 1])
    rest = inp_seq[:, :, band_dim + 1 :]
    x = tf.keras.layers.Concatenate(axis=-1)([x, rest])

    gru_out, state = tf.keras.layers.GRU(d_state, return_sequences=False, return_state=True)(x)
    out = tf.keras.layers.LayerNormalization()(state)
    return tf.keras.Model(inputs=inp_seq, outputs=out, name="cssm")


def build_glyph_encoder(proto_dim: int = PROTO_SIG_DIM, axis_dim: int = AXIS_SIG_DIM, d_event: int = D_EVENT) -> tf.keras.Model:
    inp_proto = tf.keras.layers.Input(shape=(proto_dim,), name="proto_sig")
    inp_axis = tf.keras.layers.Input(shape=(axis_dim,), name="axis_sig")
    inp_amp = tf.keras.layers.Input(shape=(1,), name="amp")
    inp_dt = tf.keras.layers.Input(shape=(1,), name="dt")
    inp_band = tf.keras.layers.Input(shape=(1,), dtype=tf.int32, name="band_idx")
    inp_is_new = tf.keras.layers.Input(shape=(1,), name="is_new_id")

    p = tf.keras.layers.Dense(32, activation="relu")(inp_proto)
    p = tf.keras.layers.Dense(d_event // 2, activation="relu")(p)
    a = tf.keras.layers.Dense(16, activation="relu")(inp_axis)
    a = tf.keras.layers.Dense(d_event // 4, activation="relu")(a)
    m = tf.keras.layers.Concatenate()([tf.keras.layers.Lambda(lambda t: tf.math.log1p(t))(inp_amp), inp_dt])
    m = tf.keras.layers.Dense(16, activation="relu")(m)
    b_emb = tf.keras.layers.Embedding(NUM_BANDS + 1, 8)(tf.keras.layers.Lambda(lambda t: tf.clip_by_value(t, 0, NUM_BANDS))(inp_band[:, 0]))
    b_emb = tf.keras.layers.Flatten()(b_emb)

    fused = tf.keras.layers.Concatenate()([p, a, m, b_emb, inp_is_new])
    out = tf.keras.layers.Dense(d_event, activation="relu")(fused)
    return tf.keras.Model(
        inputs=[inp_proto, inp_axis, inp_amp, inp_dt, inp_band, inp_is_new],
        outputs=out,
        name="glyph_encoder",
    )


def build_batched_glyph_encoder(
    proto_dim: int = PROTO_SIG_DIM,
    axis_dim: int = AXIS_SIG_DIM,
    d_event: int = D_EVENT,
    max_events: int = W_SHORT_EVENTS,
    num_bands: int = NUM_BANDS,
) -> tf.keras.Model:
    """
    Batched glyph encoder for (batch, max_events, ...) inputs.
    Output: (batch, max_events, d_event).
    Uses per-event processing via TimeDistributed-compatible structure.
    """
    inp_proto = tf.keras.layers.Input(shape=(max_events, proto_dim), name="proto_sig")
    inp_axis = tf.keras.layers.Input(shape=(max_events, axis_dim), name="axis_sig")
    inp_amp = tf.keras.layers.Input(shape=(max_events, 1), name="amp")
    inp_dt = tf.keras.layers.Input(shape=(max_events, 1), name="dt")
    inp_band = tf.keras.layers.Input(shape=(max_events, 1), dtype=tf.int32, name="band_idx")
    inp_is_new = tf.keras.layers.Input(shape=(max_events, 1), name="is_new_id")

    band_emb = tf.keras.layers.Embedding(num_bands + 1, 8, name="band_embedding")(tf.keras.layers.Lambda(lambda t: tf.clip_by_value(t[:, :, 0], 0, num_bands))(inp_band))
    amp_log = tf.keras.layers.Lambda(lambda t: tf.math.log1p(tf.maximum(t, 0.0)))(inp_amp)
    raw = tf.keras.layers.Concatenate(axis=-1)([inp_proto, inp_axis, amp_log, inp_dt, inp_is_new])
    raw = tf.keras.layers.Concatenate(axis=-1)([raw, band_emb])
    out = tf.keras.layers.Dense(64, activation="relu")(raw)
    out = tf.keras.layers.Dense(d_event, activation="relu")(out)
    return tf.keras.Model(
        inputs=[inp_proto, inp_axis, inp_amp, inp_dt, inp_band, inp_is_new],
        outputs=out,
        name="batched_glyph_encoder",
    )


def build_reaction_encoder(phase_dim: int = PHASE_VEC_DIM, d_event: int = D_EVENT) -> tf.keras.Model:
    inp_phase = tf.keras.layers.Input(shape=(phase_dim,), name="phase_vec")
    inp_energy = tf.keras.layers.Input(shape=(1,), name="total_energy")
    inp_dt = tf.keras.layers.Input(shape=(1,), name="dt")
    inp_band = tf.keras.layers.Input(shape=(1,), dtype=tf.int32, name="band_idx")
    inp_exciter = tf.keras.layers.Input(shape=(1,), dtype=tf.int32, name="exciter_id")

    p = tf.keras.layers.Dense(32, activation="relu")(inp_phase)
    p = tf.keras.layers.Dense(d_event // 2, activation="relu")(p)
    e = tf.keras.layers.Concatenate()([tf.keras.layers.Lambda(lambda t: tf.math.log1p(t))(inp_energy), inp_dt])
    e = tf.keras.layers.Dense(16, activation="relu")(e)
    b_emb = tf.keras.layers.Embedding(NUM_BANDS + 1, 8)(tf.keras.layers.Lambda(lambda t: tf.clip_by_value(t, 0, NUM_BANDS))(inp_band[:, 0]))
    b_emb = tf.keras.layers.Flatten()(b_emb)
    exc_emb = tf.keras.layers.Embedding(16, 8)(tf.keras.layers.Lambda(lambda t: tf.clip_by_value(t, 0, 15))(inp_exciter[:, 0]))
    exc_emb = tf.keras.layers.Flatten()(exc_emb)

    fused = tf.keras.layers.Concatenate()([p, e, b_emb, exc_emb])
    out = tf.keras.layers.Dense(d_event, activation="relu")(fused)
    return tf.keras.Model(
        inputs=[inp_phase, inp_energy, inp_dt, inp_band, inp_exciter],
        outputs=out,
        name="reaction_encoder",
    )


def build_full_model(num_classes: int = 4, d_state: int = D_STATE, d_event: int = D_EVENT, max_events: int = W_SHORT_EVENTS) -> tf.keras.Model:
    inp_state = tf.keras.layers.Input(shape=(d_state,), name="state")
    inp_events = tf.keras.layers.Input(shape=(max_events, d_event), name="events")
    inp_event_mask = tf.keras.layers.Input(shape=(max_events,), name="event_mask")

    state_exp = tf.keras.layers.RepeatVector(max_events)(inp_state)
    combined = tf.keras.layers.Concatenate(axis=-1)([inp_events, state_exp])
    combined = tf.keras.layers.Dense(d_event, activation="relu")(combined)

    def make_mask(x):
        # x: (batch, seq_len). Need mask (batch, seq_len, seq_len) for self-attention.
        # 1.0 = valid, 0.0 = padding. Attend only between valid positions.
        m = tf.cast(x > 0.5, tf.float32)
        m = tf.expand_dims(m, axis=2) * tf.expand_dims(m, axis=1)  # (B, T, T)
        return tf.where(m > 0.5, 0.0, -1e9)

    att = tf.keras.layers.MultiHeadAttention(num_heads=NUM_HEADS, key_dim=D_HEAD)(
        combined, combined,
        attention_mask=tf.keras.layers.Lambda(make_mask)(inp_event_mask),
    )
    att = tf.keras.layers.Add()([combined, att])
    att = tf.keras.layers.LayerNormalization()(att)

    mask_exp = tf.keras.layers.Reshape((max_events, 1))(inp_event_mask)
    att_masked = tf.keras.layers.Multiply()([att, mask_exp])
    pooled = tf.keras.layers.GlobalAveragePooling1D()(att_masked)

    fused = tf.keras.layers.Concatenate()([pooled, inp_state])
    fused = tf.keras.layers.Dense(64, activation="relu")(fused)
    fused = tf.keras.layers.Dropout(0.3)(fused)
    out = tf.keras.layers.Dense(num_classes, activation="softmax", name="head")(fused)

    return tf.keras.Model(
        inputs=[inp_state, inp_events, inp_event_mask],
        outputs=out,
        name="state_space_event",
    )


def build_ml_queue_pipeline(num_classes: int = 4) -> tf.keras.Model:
    """
    Full pipeline: frame_sequence + events → prediction.
    """
    cssm = build_cssm()
    glyph_enc = build_glyph_encoder()
    reaction_enc = build_reaction_encoder()
    head_model = build_full_model(num_classes=num_classes)

    feat_dim = CSSM_BAND_DIM + 1 + 1 + AXIS_RATIO_DIM + 2
    inp_frame_seq = tf.keras.layers.Input(shape=(None, feat_dim), name="frame_sequence")
    inp_events = tf.keras.layers.Input(shape=(W_SHORT_EVENTS, D_EVENT), name="events")
    inp_mask = tf.keras.layers.Input(shape=(W_SHORT_EVENTS,), name="event_mask")

    state = cssm(inp_frame_seq)
    pred = head_model([state, inp_events, inp_mask])

    return tf.keras.Model(
        inputs=[inp_frame_seq, inp_events, inp_mask],
        outputs=pred,
        name="ml_queue_pipeline",
    )


def build_trainable_pipeline(
    num_classes: int = 4,
    max_events: int = W_SHORT_EVENTS,
) -> tf.keras.Model:
    """
    Full trainable pipeline: frame_seq + raw events (proto, axis, amp, dt, band_idx, is_new) → prediction.
    All components (CSSM, glyph_encoder, head) are trained end-to-end.
    """
    feat_dim = CSSM_BAND_DIM + 1 + 1 + AXIS_RATIO_DIM + 2
    inp_frame_seq = tf.keras.layers.Input(shape=(None, feat_dim), name="frame_sequence")
    inp_proto = tf.keras.layers.Input(shape=(max_events, PROTO_SIG_DIM), name="proto_sig")
    inp_axis = tf.keras.layers.Input(shape=(max_events, AXIS_SIG_DIM), name="axis_sig")
    inp_amp = tf.keras.layers.Input(shape=(max_events, 1), name="amp")
    inp_dt = tf.keras.layers.Input(shape=(max_events, 1), name="dt")
    inp_band = tf.keras.layers.Input(shape=(max_events, 1), dtype=tf.int32, name="band_idx")
    inp_is_new = tf.keras.layers.Input(shape=(max_events, 1), name="is_new_id")
    inp_mask = tf.keras.layers.Input(shape=(max_events,), name="event_mask")

    state = build_cssm()(inp_frame_seq)
    events_emb = build_batched_glyph_encoder(max_events=max_events)([inp_proto, inp_axis, inp_amp, inp_dt, inp_band, inp_is_new])
    pred = build_full_model(num_classes=num_classes, max_events=max_events)([state, events_emb, inp_mask])

    return tf.keras.Model(
        inputs=[inp_frame_seq, inp_proto, inp_axis, inp_amp, inp_dt, inp_band, inp_is_new, inp_mask],
        outputs=pred,
        name="trainable_pipeline",
    )
