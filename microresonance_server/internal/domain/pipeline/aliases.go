package pipeline

import (
	domainalphabet "server/internal/domain/alphabet"
	domaindynamics "server/internal/domain/dynamics"
	domainglyph "server/internal/domain/glyph"
	domainsignal "server/internal/domain/signal"
	mlqueue "server/internal/ml/queue"
	mltrainer "server/internal/ml/trainer"
	platsignal "server/internal/platform/signal"
)

// --- alphabet aliases ---

const ReactionPhaseBins = domainalphabet.ReactionPhaseBins

type DistID = domainalphabet.DistID
type DistIDWithSig = domainalphabet.DistIDWithSig
type Pattern = domainalphabet.Pattern
type PatternStore = domainalphabet.PatternStore
type ShapeMergeParams = domainalphabet.ShapeMergeParams
type MergeResult = domainalphabet.MergeResult

// --- dynamics aliases ---

type DynamicsStore = domaindynamics.DynamicsStore
type GlyphEvent = domaindynamics.GlyphEvent

// --- glyph aliases ---

const MinSamplesForGlyph = domainglyph.MinSamplesForGlyph
const TrainerShapePoints = domainglyph.TrainerShapePoints
const PeakProfileSize = domainglyph.PeakProfileSize

type ShapeSignature = domainglyph.ShapeSignature
type ShapeVector = domainglyph.ShapeVector
type ShapeClusterStore = domainglyph.ShapeClusterStore

var NewShapeClusterStore = domainglyph.NewShapeClusterStore
var BuildShape = domainglyph.BuildShape
var BuildShapeWithBounds = domainglyph.BuildShapeWithBounds
var ExtractWaveShapeAtFreqWithBounds = domainglyph.ExtractWaveShapeAtFreqWithBounds
var ShapeLooksCleanCycle = domainglyph.ShapeLooksCleanCycle
var ComputeFrozenGlyphFromSegment = domainglyph.ComputeFrozenGlyphFromSegment
var EnvelopeCVAndSlope = domainglyph.EnvelopeCVAndSlope
var ShapeDistance = domainglyph.ShapeDistance

// unexported helpers used inline in shape.go / residual.go
var computeEnvelope = domainglyph.ComputeEnvelope
var estimatePeriodSamples = domainglyph.EstimatePeriodSamples

// --- signal aliases ---

var BandsFilterbank = domainsignal.BandsFilterbank

// --- ml/queue aliases ---

type MLQueueImpl = mlqueue.MLQueueImpl
type EventGlyph = mlqueue.EventGlyph
type FrameSummary = mlqueue.FrameSummary
type ExciterState = mlqueue.ExciterState
type EventReaction = mlqueue.EventReaction

var NewMLQueue = mlqueue.NewMLQueue

// DefaultShapeParams returns default pipeline parameters.
func DefaultShapeParams() ShapePipelineParams { return defaultShapePipelineParams() }

// --- ml/trainer aliases ---

const TrainerEventPoints = mltrainer.TrainerEventPoints

type TrainerGlyphBuffer = mltrainer.TrainerGlyphBuffer
type TrainerEventBuffer = mltrainer.TrainerEventBuffer

var ExtractTrainerEvent = mltrainer.ExtractTrainerEvent
var AxisSigFromGlyphData = mltrainer.AxisSigFromGlyphData

// --- FFT helpers (used by shape.go and spectrum.go) ---

func computeFFTScan(samples []float64, n int, fs float64) (mag []float64, freqs []float64) {
	scratch := &platsignal.FFTScratch{}
	magReuse, freqsReuse := platsignal.ComputeFFTScanReuse(scratch, samples, n, fs)
	return copyFloatSlice(magReuse), copyFloatSlice(freqsReuse)
}

func copyFloatSlice(in []float64) []float64 {
	if len(in) == 0 {
		return nil
	}
	out := make([]float64, len(in))
	copy(out, in)
	return out
}
