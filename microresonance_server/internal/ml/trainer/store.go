// Trainer: Actions — user labels for repeatable physical scenarios.
// Calibration = guided loop [Start]/[End]: in DO window we collect glyphs in the same format as
// the pipeline classifies them (ShapeVector.Points). Data is saved for TensorFlow training.
// Action classification — by TF model on glyphs, not by bands signature.

package trainer

import (
	"math"
	"sync"
	"time"

	domainalphabet "server/internal/domain/alphabet"
	domaindynamics "server/internal/domain/dynamics"
	domainglyph "server/internal/domain/glyph"
)

const (
	CalRestWindowSec  = 1.0 // REST window before Start (sec)
	CalDefaultRepeats = 10
	CalTempoSec       = 3.0 // once per 3 seconds (reference)
	TrainerBandsN     = 8
)

// GlyphSample — one glyph in classification format: cycle shape (Points), band, time.
// Same representation as MergeOrAddByShape: ShapeVector.Points after phase normalization.
type GlyphSample struct {
	Points  []float64 // cycle shape (64/128/256 points), as in pipeline
	BandIdx int       // band B0..B7
	Ts      int64     // unix ms
}

// TrainerGlyphBuffer — buffer of last glyphs from pipeline for collection in [Start..End] window.
// Pipeline calls Add on each classified glyph; calibration reads GetInWindow.
type TrainerGlyphBuffer interface {
	Add(points []float64, bandIdx int, ts int64)
	GetInWindow(startMs, endMs int64) []GlyphSample
	Clear()
}

// TrainerEventBuffer — buffer of glyph-events (one cycle, 3 shape extrema, invariants). Dataset unit = one event.
// glyphID = alphabet token (PatternStore ID); run/age counted by glyph_id.
type TrainerEventBuffer interface {
	AddEvent(ev *TrainerEvent, bandIdx int, ts int64, glyphID int)
	GetInWindow(startMs, endMs int64) []TrainerEvent
	Clear()
}

// GlyphShapeBuffer — ring buffer of glyphs (TrainerGlyphBuffer implementation).
type GlyphShapeBuffer struct {
	mu      sync.Mutex
	samples []GlyphSample
	cap     int
}

// NewGlyphShapeBuffer creates a buffer for cap last glyphs.
func NewGlyphShapeBuffer(cap int) *GlyphShapeBuffer {
	if cap < 100 {
		cap = 100
	}
	return &GlyphShapeBuffer{samples: make([]GlyphSample, 0, cap), cap: cap}
}

func (b *GlyphShapeBuffer) Add(points []float64, bandIdx int, ts int64) {
	if len(points) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	dup := make([]float64, len(points))
	copy(dup, points)
	s := GlyphSample{Points: dup, BandIdx: bandIdx, Ts: ts}
	if len(b.samples) >= b.cap {
		b.samples = append(b.samples[1:], s)
	} else {
		b.samples = append(b.samples, s)
	}
}

func (b *GlyphShapeBuffer) GetInWindow(startMs, endMs int64) []GlyphSample {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]GlyphSample, 0)
	for _, s := range b.samples {
		if s.Ts >= startMs && s.Ts <= endMs {
			out = append(out, s)
		}
	}
	return out
}

func (b *GlyphShapeBuffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.samples = b.samples[:0]
}

// TrainerEventBufferImpl — ring buffer of events with age_since_prev, event_index_in_run, run_length support.
const eventBufferCap = 2048
const eventRunThresholdMs = 500 // gap > 500 ms → new run

type TrainerEventBufferImpl struct {
	mu        sync.Mutex
	events    []TrainerEvent
	cap       int
	lastTs    map[int]int64 // by glyph_id
	runLength map[int]int
}

func NewTrainerEventBuffer() *TrainerEventBufferImpl {
	return &TrainerEventBufferImpl{
		events:    make([]TrainerEvent, 0, eventBufferCap),
		cap:       eventBufferCap,
		lastTs:    make(map[int]int64),
		runLength: make(map[int]int),
	}
}

func (b *TrainerEventBufferImpl) AddEvent(ev *TrainerEvent, bandIdx int, ts int64, glyphID int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bandIdx < 0 || bandIdx >= 8 {
		bandIdx = 0
	}
	ev.GlyphID = glyphID
	ev.BandIdx = bandIdx
	ev.Ts = ts
	ageMs := float64(0)
	if b.lastTs[glyphID] > 0 {
		ageMs = float64(ts - b.lastTs[glyphID])
		if ageMs > eventRunThresholdMs {
			b.runLength[glyphID] = 0
		}
	}
	b.runLength[glyphID]++
	ev.AgeSincePrevEvent = math.Log1p(ageMs)
	ev.EventIndexInRun = b.runLength[glyphID]
	ev.RunLength = b.runLength[glyphID]
	b.lastTs[glyphID] = ts
	e := *ev
	if len(b.events) >= b.cap {
		b.events = append(b.events[1:], e)
	} else {
		b.events = append(b.events, e)
	}
}

func (b *TrainerEventBufferImpl) GetInWindow(startMs, endMs int64) []TrainerEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]TrainerEvent, 0)
	for _, e := range b.events {
		if e.Ts >= startMs && e.Ts <= endMs {
			out = append(out, e)
		}
	}
	return out
}

func (b *TrainerEventBufferImpl) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = b.events[:0]
	b.lastTs = make(map[int]int64)
	b.runLength = make(map[int]int)
}

// Action — container for repeatable physical scenario (jaw_clench, exhale, …).
type Action struct {
	ID           int
	Name         string
	Description  string
	Samples      int     // number of glyphs collected for this action
	Quality      float64 // 0..1 (optional, from model)
	LastTrained  time.Time
	StateContext string    // S0 "calm" etc.; empty = any context
	Attempts     []Attempt // recent attempts (for UI)
	// Band prototypes kept for JSON compatibility; classification — by TF on glyphs.
	Prototype     *EventSignature
	RestPrototype *EventSignature
}

// EventSignature — legacy: band signature (for persist compatibility).
type EventSignature struct {
	Bands [TrainerBandsN]float64
}

// Attempt — one attempt during calibration (for history).
type Attempt struct {
	Phase      string // "DO" | "REST"
	GlyphCount int    // glyphs recorded in this segment
	Ts         time.Time
}

// ActionStore — action storage.
type ActionStore struct {
	mu      sync.Mutex
	actions []*Action
	nextID  int
}

func NewActionStore() *ActionStore {
	return &ActionStore{actions: make([]*Action, 0), nextID: 1}
}

// Add creates a new action.
func (s *ActionStore) Add(name, desc string) *Action {
	s.mu.Lock()
	a := &Action{
		ID:          s.nextID,
		Name:        name,
		Description: desc,
	}
	s.nextID++
	s.actions = append(s.actions, a)
	s.mu.Unlock()
	_ = s.Save()
	return a
}

// List returns a copy of the action list.
func (s *ActionStore) List() []*Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Action, len(s.actions))
	copy(out, s.actions)
	return out
}

// ByID returns action by ID.
func (s *ActionStore) ByID(id int) *Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.actions {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// Delete removes action (use with caution).
func (s *ActionStore) Delete(id int) {
	s.mu.Lock()
	for i, a := range s.actions {
		if a.ID == id {
			s.actions = append(s.actions[:i], s.actions[i+1:]...)
			s.mu.Unlock()
			_ = s.Save()
			return
		}
	}
	s.mu.Unlock()
}

// Rename renames action.
func (s *ActionStore) Rename(id int, name string) {
	s.mu.Lock()
	for _, a := range s.actions {
		if a.ID == id {
			a.Name = name
			s.mu.Unlock()
			_ = s.Save()
			return
		}
	}
	s.mu.Unlock()
}

// CalibratePhase — calibration cycle phase.
type CalibratePhase string

const (
	PhaseRest   CalibratePhase = "REST"   // waiting for [Start]
	PhaseDo     CalibratePhase = "DO"     // recording, waiting for [End]
	PhaseResult CalibratePhase = "RESULT" // result shown, waiting for [Next]
)

// RunReactionCell stores last reaction vector and emission time per cell (exciter, band).
type RunReactionCell struct {
	Vec [domainalphabet.ReactionPhaseBins]float64
	Ts  int64
}

// RunReactionBuffer — buffer of reaction vectors (16 AM phases) for current segment [Start..End].
// Filled from OnReactionGlyph on emission; when saving dataset each event gets reaction by (exciter, band).
// Stores reaction_ts for matching rule (nearest/within window) during training.
type RunReactionBuffer struct {
	mu    sync.Mutex
	cells map[domainalphabet.ReactionCellKey]RunReactionCell
}

func NewRunReactionBuffer() *RunReactionBuffer {
	return &RunReactionBuffer{cells: make(map[domainalphabet.ReactionCellKey]RunReactionCell)}
}

func (b *RunReactionBuffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cells = make(map[domainalphabet.ReactionCellKey]RunReactionCell)
}

func (b *RunReactionBuffer) Add(exciterID, bandID int, vec [domainalphabet.ReactionPhaseBins]float64, nowMs int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cells[domainalphabet.ReactionCellKey{ExciterID: exciterID, BandID: bandID}] = RunReactionCell{Vec: vec, Ts: nowMs}
}

// Get returns copy of vector, emission time and true for (exciter, band), or (nil, 0, false) if no data.
func (b *RunReactionBuffer) Get(exciterID, bandID int) ([]float64, int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := domainalphabet.ReactionCellKey{ExciterID: exciterID, BandID: bandID}
	cell, ok := b.cells[key]
	if !ok {
		return nil, 0, false
	}
	out := make([]float64, domainalphabet.ReactionPhaseBins)
	copy(out, cell.Vec[:])
	return out, cell.Ts, true
}

// CalibrateLoop — training sequence of actions. Buttons [Start]/[End].
// In DO window we collect glyphs from GlyphBuffer (same format as pipeline classification).
type CalibrateLoop struct {
	Phase           CalibratePhase
	PhaseStart      time.Time
	CurrentActionID int
	// PlannedSequence — order of actions for training; after End next is shown automatically without Next click.
	PlannedSequence []int
	// Glyph buffer (legacy) or events. In event paradigm only EventBuffer is used.
	GlyphBuffer TrainerGlyphBuffer
	EventBuffer TrainerEventBuffer
	// Reaction vector buffer for segment [Start..End]; attached to events by band when saving.
	RunReactionBuffer *RunReactionBuffer
	// Accumulated glyphs per action (legacy).
	GlyphSamplesPerAction map[int][]GlyphSample
	// Accumulated events per action: actionID → []TrainerEvent (dataset unit = one event).
	EventsPerAction map[int][]TrainerEvent
	Sequence        []int
	LastResult      string  // "Recorded N events" or TF result
	LastConf        float64 // event count or confidence from model
}

// PhaseProgress — for displaying DO progress.
func (c *CalibrateLoop) PhaseProgress(now time.Time) float64 {
	if c.Phase == PhaseDo {
		elapsed := now.Sub(c.PhaseStart).Seconds()
		if elapsed > 5 {
			return 1
		}
		return elapsed / 5
	}
	return 0
}

// PhaseRemainingSec — not used in button mode.
func (c *CalibrateLoop) PhaseRemainingSec(now time.Time) float64 {
	return 0
}

// OnStart called on [Start] press: CurrentActionID must be selected. Transition to DO.
func (c *CalibrateLoop) OnStart(now time.Time, _ *domaindynamics.DynamicsStore) bool {
	if c.Phase != PhaseRest || c.CurrentActionID == 0 {
		return false
	}
	c.Phase = PhaseDo
	c.PhaseStart = now
	return true
}

// OnEnd called on [End] press: collect events (or glyphs) from buffer in window [PhaseStart..now].
// Each run (Start–End) saved to separate run_XXX folder with transition meta (prev_action_id, sequence_index).
// If PlannedSequence set and next action exists — auto-transition to it (PhaseRest), without Next click.
func (c *CalibrateLoop) OnEnd(now time.Time, _ *domaindynamics.DynamicsStore) {
	if c.Phase != PhaseDo || c.CurrentActionID == 0 {
		return
	}
	startMs := c.PhaseStart.UnixMilli()
	endMs := now.UnixMilli()
	var prevActionID int
	seqIdx := len(c.Sequence)
	if len(c.Sequence) > 0 {
		prevActionID = c.Sequence[len(c.Sequence)-1]
	}
	if c.EventBuffer != nil {
		events := c.EventBuffer.GetInWindow(startMs, endMs)
		if c.EventsPerAction == nil {
			c.EventsPerAction = make(map[int][]TrainerEvent)
		}
		c.EventsPerAction[c.CurrentActionID] = append(c.EventsPerAction[c.CurrentActionID], events...)
		c.Sequence = append(c.Sequence, c.CurrentActionID)
		c.LastResult = "Recorded"
		c.LastConf = float64(len(events))
		seqLen := 0
		if len(c.PlannedSequence) > 0 {
			seqLen = len(c.PlannedSequence)
		}
		if actionStoreGlobal != nil && len(events) > 0 {
			SaveRunToDataset(c.CurrentActionID, events, actionStoreGlobal, c.RunReactionBuffer, prevActionID, seqIdx, seqLen)
		}
	} else if c.GlyphBuffer != nil {
		samples := c.GlyphBuffer.GetInWindow(startMs, endMs)
		if c.GlyphSamplesPerAction == nil {
			c.GlyphSamplesPerAction = make(map[int][]GlyphSample)
		}
		c.GlyphSamplesPerAction[c.CurrentActionID] = append(c.GlyphSamplesPerAction[c.CurrentActionID], samples...)
		c.Sequence = append(c.Sequence, c.CurrentActionID)
		c.LastResult = "Recorded"
		c.LastConf = float64(len(samples))
	}
	c.Phase = PhaseResult
	// Manual mode: always stay in PhaseResult until user presses Next.
}

// OnNext — next segment: advance to next action in sequence, or new round from first.
func (c *CalibrateLoop) OnNext() {
	if c.Phase != PhaseResult {
		return
	}
	c.Phase = PhaseRest
	c.PhaseStart = time.Now()
	if len(c.PlannedSequence) > 0 {
		nextIdx := len(c.Sequence)
		if nextIdx < len(c.PlannedSequence) {
			c.CurrentActionID = c.PlannedSequence[nextIdx]
		} else {
			c.CurrentActionID = c.PlannedSequence[0] // new round
		}
	} else {
		c.CurrentActionID = 0
	}
}

// OnFinish — finish training, build prototypes for all actions.
func (c *CalibrateLoop) OnFinish() {
	finishSequenceTraining(c)
}

// For ActionStore access from CalibrateLoop (circular dependency — use global)
var actionStoreGlobal *ActionStore

func SetActionStoreForTrainer(s *ActionStore) {
	actionStoreGlobal = s
}

// finishSequenceTraining updates Samples per action and saves actions.json.
// Event vectors already saved per run in OnEnd (SaveRunToDataset); legacy glyphs — via SaveGlyphSamplesToDataset.
func finishSequenceTraining(c *CalibrateLoop) {
	if actionStoreGlobal == nil {
		return
	}
	if c.EventsPerAction != nil {
		for actionID, events := range c.EventsPerAction {
			a := actionStoreGlobal.ByID(actionID)
			if a == nil {
				continue
			}
			a.Samples += len(events)
			a.LastTrained = time.Now()
		}
		// Don't call SaveEventsToDataset — each run already saved in run_XXX in OnEnd
	} else if c.GlyphSamplesPerAction != nil {
		for actionID, samples := range c.GlyphSamplesPerAction {
			a := actionStoreGlobal.ByID(actionID)
			if a == nil {
				continue
			}
			valid := 0
			for _, s := range samples {
				if len(s.Points) == domainglyph.TrainerShapePoints {
					valid++
				}
			}
			a.Samples += valid
			a.LastTrained = time.Now()
		}
		SaveGlyphSamplesToDataset(c.GlyphSamplesPerAction, actionStoreGlobal)
	}
	if actionStoreGlobal != nil {
		_ = actionStoreGlobal.Save()
	}
}
