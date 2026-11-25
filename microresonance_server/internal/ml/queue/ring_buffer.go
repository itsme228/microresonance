// ml_ring_buffer.go — кольцевые буферы для ML: W_short (4s), W_mid (30s).
// ML-воркер держит ring-buffer последнего W_mid для FrameSummary и W_short для событий.

package queue

import (
	"sync"
	"time"
)

// WShortSec — локальная последовательность событий (4s).
const WShortSec = 4

// WMidSec — контекст режима (30s).
const WMidSec = 30

// MLRingBuffer — кольцевой буфер для ML-воркера.
type MLRingBuffer struct {
	mu sync.RWMutex

	// FrameSummary: последние W_mid секунд (примерно 30 записей при 1s heartbeat)
	frames   []FrameSummary
	framesTs []int64

	// EventGlyph + EventReaction: последние W_short секунд
	glyphEvents      []EventGlyph
	glyphEventsTs    []int64
	reactionEvents   []EventReaction
	reactionEventsTs []int64
}

// NewMLRingBuffer создаёт буфер с ёмкостью под W_mid и W_short.
func NewMLRingBuffer() *MLRingBuffer {
	// W_mid = 30s, heartbeat 1s → ~35 frames
	// W_short = 4s, события могут быть частыми → ~64
	return &MLRingBuffer{
		frames:           make([]FrameSummary, 0, 40),
		framesTs:         make([]int64, 0, 40),
		glyphEvents:      make([]EventGlyph, 0, 64),
		glyphEventsTs:    make([]int64, 0, 64),
		reactionEvents:   make([]EventReaction, 0, 64),
		reactionEventsTs: make([]int64, 0, 64),
	}
}

// AddFrame добавляет FrameSummary, отсекая старые за W_mid.
func (b *MLRingBuffer) AddFrame(f FrameSummary, nowMs int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := nowMs - int64(WMidSec*1000)
	b.frames = append(b.frames, f)
	b.framesTs = append(b.framesTs, f.T1)
	// Отсечь старые
	for len(b.frames) > 0 && b.framesTs[0] < cutoff {
		b.frames = b.frames[1:]
		b.framesTs = b.framesTs[1:]
	}
}

// AddGlyphEvent добавляет EventGlyph, отсекая за W_short.
func (b *MLRingBuffer) AddGlyphEvent(e EventGlyph, nowMs int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := nowMs - int64(WShortSec*1000)
	b.glyphEvents = append(b.glyphEvents, e)
	b.glyphEventsTs = append(b.glyphEventsTs, e.T)
	for len(b.glyphEvents) > 0 && b.glyphEventsTs[0] < cutoff {
		b.glyphEvents = b.glyphEvents[1:]
		b.glyphEventsTs = b.glyphEventsTs[1:]
	}
}

// AddReactionEvent добавляет EventReaction.
func (b *MLRingBuffer) AddReactionEvent(e EventReaction, nowMs int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := nowMs - int64(WShortSec*1000)
	b.reactionEvents = append(b.reactionEvents, e)
	b.reactionEventsTs = append(b.reactionEventsTs, e.TCycleEnd)
	for len(b.reactionEvents) > 0 && b.reactionEventsTs[0] < cutoff {
		b.reactionEvents = b.reactionEvents[1:]
		b.reactionEventsTs = b.reactionEventsTs[1:]
	}
}

// FramesInWindow возвращает FrameSummary за последние W_mid секунд.
func (b *MLRingBuffer) FramesInWindow(nowMs int64) []FrameSummary {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cutoff := nowMs - int64(WMidSec*1000)
	var out []FrameSummary
	for i, ts := range b.framesTs {
		if ts >= cutoff {
			out = append(out, b.frames[i])
		}
	}
	return out
}

// GlyphEventsInWindow возвращает EventGlyph за W_short.
func (b *MLRingBuffer) GlyphEventsInWindow(nowMs int64) []EventGlyph {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cutoff := nowMs - int64(WShortSec*1000)
	var out []EventGlyph
	for i, ts := range b.glyphEventsTs {
		if ts >= cutoff {
			out = append(out, b.glyphEvents[i])
		}
	}
	return out
}

// ReactionEventsInWindow возвращает EventReaction за W_short.
func (b *MLRingBuffer) ReactionEventsInWindow(nowMs int64) []EventReaction {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cutoff := nowMs - int64(WShortSec*1000)
	var out []EventReaction
	for i, ts := range b.reactionEventsTs {
		if ts >= cutoff {
			out = append(out, b.reactionEvents[i])
		}
	}
	return out
}

// LastState возвращает последнее состояние (для inference: state_t из CSSM).
// Пока заглушка: последний FrameSummary.
func (b *MLRingBuffer) LastState() *FrameSummary {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.frames) == 0 {
		return nil
	}
	f := b.frames[len(b.frames)-1]
	return &f
}

// WShortDuration возвращает длительность W_short.
func WShortDuration() time.Duration {
	return WShortSec * time.Second
}

// WMidDuration возвращает длительность W_mid.
func WMidDuration() time.Duration {
	return WMidSec * time.Second
}
