package gui

import (
	"time"

	domainalphabet "server/internal/domain/alphabet"
	"server/internal/domain/pipeline"
	mlqueue "server/internal/ml/queue"
	mltrainer "server/internal/ml/trainer"
	mlworker "server/internal/ml/worker"
)

// --- alphabet ---

type Pattern = domainalphabet.Pattern

var ExciterIDFromPresetName = domainalphabet.ExciterIDFromPresetName
var ExciterIDToName = domainalphabet.ExciterIDToName

// --- pipeline helpers used by screen files without package qualifier ---

var NCC = pipeline.NCC
var Build1DInvariantMag2 = pipeline.Build1DInvariantMag2

// --- trainer ---

type ActionStore = mltrainer.ActionStore
type Action = mltrainer.Action
type GlyphStreamRecorder = mltrainer.GlyphStreamRecorder
type GlyphShapeBuffer = mltrainer.GlyphShapeBuffer
type TrainerEventBufferImpl = mltrainer.TrainerEventBufferImpl
type RunReactionBuffer = mltrainer.RunReactionBuffer
type CalibrateLoop = mltrainer.CalibrateLoop

var NewActionStore = mltrainer.NewActionStore
var NewGlyphShapeBuffer = mltrainer.NewGlyphShapeBuffer
var NewTrainerEventBuffer = mltrainer.NewTrainerEventBuffer
var NewRunReactionBuffer = mltrainer.NewRunReactionBuffer

const PhaseRest = mltrainer.PhaseRest
const PhaseDo = mltrainer.PhaseDo
const PhaseResult = mltrainer.PhaseResult

// --- ml/worker ---

type MLPrediction = mlworker.MLPrediction
type MLWorker = mlworker.MLWorker

var NewMLWorker = mlworker.NewMLWorker
var MLModelExists = mlworker.MLModelExists

// --- ml/queue ---

type MLQueueImpl = mlqueue.MLQueueImpl
type FrameSummary = mlqueue.FrameSummary
type EventReaction = mlqueue.EventReaction

var NewMLQueue = mlqueue.NewMLQueue

// timeWindow is a package-level variable matching the core flag default.
// Set via SetTimeWindow before calling Run to reflect CLI flag value.
var timeWindow = func() *time.Duration { d := 500 * time.Millisecond; return &d }()

// SetTimeWindow propagates the CLI -time flag into the gui package.
func SetTimeWindow(d *time.Duration) { timeWindow = d }

// adxlRate and adsRate match core flag defaults; set via SetSampleRates.
var adxlRate = func() *int { v := 4000; return &v }()
var adsRate = func() *int { v := 40000; return &v }()

// SetSampleRates propagates the CLI -adxlRate/-adsRate flags into the gui package.
func SetSampleRates(adxl, ads *int) { adxlRate = adxl; adsRate = ads }
