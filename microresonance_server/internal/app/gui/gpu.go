// GPU mode: ebiten, FFT spectra, tuning controls. 0.2–40000 Hz.
// Run: ./server -gpu -addr=:5555

package gui

import (
	"fmt"
	"image/color"
	"log"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
	"server/internal/domain/alphabet"
	"server/internal/domain/dynamics"
	"server/internal/domain/pipeline"
	"server/internal/domain/signal"
	"server/internal/ports/httpapi"
	"server/internal/ports/vrws"
)

const (
	headerH       = 20
	mainTabRowH   = 22 // ряд вкладок SCAN | PATTERNS (под заголовком, над слайдерами)
	contentTopGap = 4  // отступ между вкладками и строкой сенсора (все экраны)
	sensorRowH    = 20 // высота строки выбора сенсора
	sliderH       = 22
	fftLabelWidth = 56 // label column width (Sensor:, t:, Y:, FFT:)
	plotPad       = 4
	strokeW       = 1.5
	keyRepeat     = 6
	thumbW        = 10
	panelMinW     = 240
	panelMaxW     = 320
	panelPad      = 10
	tabH          = 20
	rowH          = 22
	btnH          = 18
	// Frequencies 0.2–40000 Hz; display 0..Fs/2 depending on channel
	freqMinHz = 0.2
	freqMaxHz = 40000.0
	adxlYMin  = -1.0
	adxlYMax  = 1.0
	adsYMin   = -2.56
	adsYMax   = 2.56
)

var (
	clrX     = color.RGBA{0xf4, 0x4e, 0x4e, 0xff}
	clrY     = color.RGBA{0x66, 0xbb, 0x6a, 0xff}
	clrZ     = color.RGBA{0x42, 0xa5, 0xf5, 0xff}
	clrADS   = color.RGBA{0xff, 0xca, 0x28, 0xff}
	clrBg    = color.RGBA{0x20, 0x20, 0x30, 0xff} // slightly brighter than original 12,12,1e
	clrGrd   = color.RGBA{0x2a, 0x2a, 0x38, 0xff}
	clrGrid  = color.RGBA{0x1e, 0x1e, 0x28, 0xff}
	clrAcc   = color.RGBA{0x5c, 0x6b, 0xce, 0xff}
	clrAuto  = color.RGBA{0x66, 0xbb, 0x6a, 0xff}
	clrPanel = color.RGBA{0x16, 0x18, 0x24, 0xff}
	clrBtn   = color.RGBA{0x24, 0x29, 0x3a, 0xff}
	clrBtnOn = color.RGBA{0x3a, 0x53, 0x9f, 0xff}
	clrTxt   = color.RGBA{0xd8, 0xe2, 0xff, 0xff}
)

type uiRect struct {
	x int
	y int
	w int
	h int
}

func (r uiRect) contains(mx, my int) bool {
	return mx >= r.x && mx <= r.x+r.w && my >= r.y && my <= r.y+r.h
}

// xcorrRectItem — элемент на экране XCORR (чекбокс или график) для клика.
type xcorrRectItem struct {
	key        string
	rect       uiRect
	isCheckbox bool
}

// lockedCardRect — карточка токена на вкладке LOCKED (для клика → показ метаданных).
type lockedCardRect struct {
	patternID int
	rect      uiRect
}

// reactionCardRect — карточка реакции (exciter, band) сверху ALPHABET; клик → попап с цифрами и спектром.
type reactionCardRect struct {
	rect  uiRect
	index int // индекс в ListAll()
}

type alphabetCardAnimState struct {
	FromX, FromY float64
	ToX, ToY     float64
	StartMs      int64
	EndMs        int64
	SeenGen      uint32
}

// rawPopupBandData — данные одной полосы для всплывающего графика RAW (xB, yB, zB, squared).
type rawPopupBandData struct {
	xB, yB, zB, squared []float64
}

type sliderDef struct {
	rect uiRect
	min  float64
	max  float64
	step float64
}

type panelLayout struct {
	show    bool
	x       int
	y       int
	w       int
	h       int
	tabs    map[string]uiRect
	buttons map[string]uiRect
	sliders map[string]sliderDef
	inputs  map[string]uiRect
}

type sensorGUIState struct {
	// SCAN: FFT cache for Draw
	scanFFTCacheAt   time.Time
	scanFFTCachePkts uint64
	scanFFTMagX      []float64
	scanFFTMagY      []float64
	scanFFTMagZ      []float64
	scanFFTFreqAdxl  []float64
	scanFFTMagADS    []float64
	scanFFTFreqADS   []float64

	// SCAN: spectral heatmap (waterfall) - combined FFT X+Y+Z
	scanHeatmapBuf      [][]float64
	scanHeatmapHead     int
	scanEdgeHeatmapBuf  [][32]uint8
	scanEdgeHeatmapHead int
	lastHeatmapPkts     uint64
	waterfallMaxMag     float64 // Smoothed max magnitude for stable waterfall colors

	// Per-axis FFT waterfalls (X, Y, Z) - shared between SCAN and LOCKIN screens
	lockinHeatX    [][]float64
	lockinHeatY    [][]float64
	lockinHeatZ    [][]float64
	lockinHeatHead [3]int
	lockinLastPkts uint64
}

type gpuGame struct {
	buf         *signal.Buffers
	keyCooldown int
	store       *httpapi.ConfigStore
	protocol    httpapi.ProtocolConfig

	vertAuto      bool
	vertMult      float64
	fftSize       int
	freqLog       bool
	dragSlider    string
	dragValue     float64
	mousePrev     bool
	panelTab      string // "EMITTER" | "RECEIVER" | "GLYPHS"
	textFocus     string
	textCooldown  int
	inputCooldown int
	syncTick      int

	// Per-sensor UI state (isolates waterfalls and FFT cache)
	sensorStates map[int]*sensorGUIState

	// Главный экран: "RAW" | "BANDS" | "DISCOVERY" | "ALPHABET" | "TRAINER" | "MEANING" | "XCORR"
	mainScreen   string
	mainTabRects map[string]uiRect

	// Выбранный сенсор для просмотра графиков (-1 = первый доступный)
	selectedSensorID int
	// Rect'ы кнопок выбора сенсора (заполняются при отрисовке)
	sensorDropdownRects []struct {
		id   int
		rect uiRect
	}
	scanDumpBtnRect      uiRect
	scanSessionName      string
	scanSessionInputRect uiRect
	streamCompactRect    uiRect
	streamRawRect        uiRect
	// XCORR: скролл, галочки «использовать для алфавита», rects для кликов
	xcorrScrollY        int
	xcorrUseForAlphabet map[string]bool
	xcorrRects          []xcorrRectItem

	// VR: real-time broadcast glyphs (без тренировки)
	vrHub *vrws.Hub
	// Веб-GUI: последняя отправка full_snapshot (throttle по времени, не по тикам)
	lastWebSnapshotTime      time.Time
	lastWebHeavySnapshotTime time.Time
	lastPruneStaleTs         time.Time // троттл вызова RemoveStale по TTL
	// Кэш "тяжёлой" части full_snapshot (обновляется реже, чем RAW/FFT).
	cachedGlyphCards         []vrws.VRPatternCard
	cachedReactionCards      []vrws.VRReactionCard
	cachedDiscovery          []vrws.VRDiscoveryCell
	cachedTrainer            vrws.VRTrainerSnapshot
	cachedMeaning            vrws.VRMeaningSnapshot
	cachedBandEnergy         [8]float64
	cachedRawX               []float64
	cachedRawY               []float64
	cachedRawZ               []float64
	cachedRawADS             []float64
	cachedFftFreqs           []float64
	cachedFftMagX            []float64
	cachedFftMagY            []float64
	cachedFftMagZ            []float64
	cachedFftMagADS          []float64
	cachedBandsData          []vrws.VRBandWaveform
	cachedXcorrPairs         []vrws.VRXcorrPair
	cachedShapeMetrics       vrws.VRShapeMetrics
	lastWebGraphSnapshotTime time.Time
	lastWebXcorrSnapshotTime time.Time

	// Токенизация по форме: кластеры форм + остаточная энтропия
	shapeClusterStore  *pipeline.ShapeClusterStore
	residualDecomposer *pipeline.ResidualDecomposer
	shapeParams        pipeline.ShapePipelineParams
	// Per-sensor stores: при переключении сенсора показываем данные только выбранного
	patternStoreBySensor         map[int]*alphabet.PatternStore
	reactionStoreBySensor        map[int]*alphabet.ReactionAlphabetStore
	primaryStoreBySensor         map[int]*alphabet.PrimaryAlphabetStore
	sensorStoresMu               sync.Mutex
	runStats                     *pipeline.ShapePipelineRunStats
	sumTotal                     int64
	sumRejected                  int64
	sumClassified                int64
	lastDetectionTs              time.Time
	detectionInterval            time.Duration
	lastProcessedPacketsBySensor map[int]uint64 // не запускать pipeline при отсутствии новых пакетов

	// Legacy: частотные кандидаты (для обратной совместимости, не используется в пайплайне по форме)
	candidateStore *pipeline.CandidateStore

	// Сырой результат детекции (DETECTION DEBUG): что алгоритм считает пиком — всегда показываем во вкладке CANDIDATES
	lastRawDetection struct {
		PeakFreq   float64
		PeakAmp    float64
		NoiseFloor float64
		AmpNorm    float64
		Valid      bool
	}
	lastDebugLogTs time.Time // раз в 5 сек пишем в консоль статистику FFT/детекции
	// Performance debug: куда уходит время кадра Update().
	perfFrames             int64
	perfUpdateTotal        time.Duration
	perfPipeline           time.Duration
	perfSnapshot           time.Duration
	perfSnapshotXcorr      time.Duration
	perfSnapshotGraph      time.Duration
	perfSnapshotBuild      time.Duration
	perfSnapshotWS         time.Duration // full dispatch (marshal+cache+broadcast)
	perfSnapshotMarshal    time.Duration
	perfSnapshotCacheFull  time.Duration
	perfSnapshotCacheAlpha time.Duration
	perfSnapshotBroadcast  time.Duration
	perfSnapshotHv         time.Duration
	perfSnapshotBytes      int64
	perfSnapshotClients    int64
	// Performance debug (Draw): этапы рендера и время по экрану.
	perfDrawFrames  int64
	perfDrawTotal   time.Duration
	perfDrawLayout  time.Duration
	perfDrawHeader  time.Duration
	perfDrawTabs    time.Duration
	perfDrawContent time.Duration
	perfDrawPanel   time.Duration
	perfDrawRAW     time.Duration
	perfDrawBANDS   time.Duration
	perfDrawXCORR   time.Duration
	perfDrawDISC    time.Duration
	perfDrawALPHA   time.Duration
	perfDrawTRAIN   time.Duration
	perfDrawMEAN    time.Duration
	perfDrawSlow33  int64
	// Pipeline flow counters for compact debug logging.
	flowPipeRuns         uint64
	flowPipeSkipNoData   uint64
	flowPipeSkipInterval uint64
	flowLastState        string
	flowLastStateLogTs   time.Time

	// Прокрутка списка токенов на вкладке ALPHABET (пиксели вниз)
	lockedScrollY  int
	scrollCooldown int // throttle клавиш ↑↓ чтобы не тормозило при удержании
	// Клик по карточке ALPHABET: показывать metadata (rects заполняются в DrawAlphabetScreen)
	lockedCardRects         []lockedCardRect
	lockedSelectedPatternID int    // -1 = не выбрано, 0,1,2… = ID паттерна (чтобы глиф с ID 0 был кликабельным)
	lockedCompareIDs        [2]int // -1 = слот пуст, иначе ID паттерна для сравнения
	// Карточки реакций сверху: rects заполняются в DrawAlphabetScreen; клик → попап рядом с курсором
	reactionCardRects              []reactionCardRect
	reactionSelectedIndex          int    // -1 = не выбрано, иначе индекс в reactions ListAll()
	reactionPopupX, reactionPopupY int    // позиция клика (попап рисуется рядом)
	reactionMinOccurrences         int    // фильтр: показывать только x≥N; 0=все; клик по x≥ — цикл 0,3,5,10
	glyphTTLSec                    int    // remove glyphs not seen for N sec; 0 = don't remove
	reactionFilterRect             uiRect // x≥N button, filled in DrawAlphabetScreen
	alphabetOrderByTop             bool   // when true, list glyphs by Occurrences (top first); when false, store order (no re-sort)
	alphabetOrderByTopRect         uiRect // checkbox "Order by top", filled in DrawAlphabetScreen
	// ALPHABET: view toggle — one curve or 3 axes X/Y/Z on card
	alphabetViewAxes         bool
	alphabetViewCombinedRect uiRect // кнопка «Combined» — заполняется в DrawAlphabetScreen
	alphabetViewAxesRect     uiRect // кнопка «3 axes» — заполняется в DrawAlphabetScreen
	// ALPHABET (3 axes): waveform vs phase portrait (X-Y, Y-Z, X-Z). true = phase portrait (default).
	alphabetAxesPhasePortrait bool
	alphabetAxesPortraitRect  uiRect // кнопка «Phase Portrait»
	alphabetAxesWaveformRect  uiRect // кнопка «Waveform»
	// ALPHABET: скроллбар — rect трека для перетаскивания мышью; заполняется в DrawAlphabetScreen
	alphabetScrollbarRect      uiRect
	alphabetScrollbarMaxScroll int  // maxScroll при последней отрисовке (для пересчёта при drag)
	alphabetScrollbarDrag      bool // true = перетаскиваем скроллбар
	alphabetScrollbarWasDrag   bool // true = только что отпустили после перетаскивания (не считать кликом по карточке)
	// Клик по глифу: попап с анимацией последних N кадров
	lockedPopupFrames      int     // сколько кадров показывать в анимации (ползунок)
	lockedPopupFrameSlider float64 // значение ползунка 0..1
	lockedPopupSliderRect  uiRect  // rect ползунка
	lockedPopupCloseRect   uiRect  // rect кнопки закрытия
	lockedPopupStopRect    uiRect  // rect кнопки Play/Pause
	lockedPopupAnimStop    bool    // true = анимация остановлена
	// Кэш «last» на карточках: обновляем строку только при смене секунды
	lockedLastStrCache map[int]string
	lockedLastSecCache map[int]int64
	// ALPHABET: стабильный порядок top-режима (обновляется батчами, не каждый кадр)
	alphabetTopOrderByBand      map[int][]int
	alphabetTopOrderNextRefresh time.Time
	alphabetTopOrderWasEnabled  bool
	// ALPHABET: анимация перемещения карточек между слотами
	alphabetCardAnimStates      map[int]alphabetCardAnimState
	alphabetCardAnimGen         uint32
	alphabetCardAnimLastCleanup time.Time

	// ПОЛОСЫ: IIR bandpass по полосам (каузальный, time-domain), кэш по fs
	bandFilters   []signal.BandpassFilter
	bandFiltersFs float64
	// RAW (экраны BANDS): клик по графику → всплывающее с увеличением; rects заполняются в DrawBandsScreen
	bandsPlotRects    []uiRect
	rawPopupOpen      bool
	rawPopupBandIndex int
	rawPopupHold      bool
	rawPopupSnapshot  *rawPopupBandData // снимок при Hold (заморозка во всплывающем)
	rawPopupHoldRect  uiRect            // кнопка Hold во всплывающем
	rawPopupCloseRect uiRect            // кнопка Close во всплывающем
	rawPopupBandData  *rawPopupBandData // текущие данные полосы для попапа (обновляется в DrawBandsScreen)

	// Последовательности глифов (для пайплайна и TRAINER; экран DYNAMICS удалён)
	dynamicsStore                 *dynamics.DynamicsStore
	dynamicsLastID                int      // legacy: один доминант (−1 = нет)
	dynamicsLastIDPerBand         [8]int   // гистерезис по полосам B0..B7 (−1 = нет)
	dynamicsSegmentStartMsPerBand [8]int64 // T_start сегмента по полосам (событийная модель)

	// Ручки стабилизации (вкладка LOCKED): T_lock, Δf_lock, ΔA_lock, окно W, стационарность A_cv, A_slope
	tLockSec   float64 // 0.2 / 0.5 / 1.0 сек
	deltaFBins int     // ±1 / ±3 / ±5 бинов
	deltaARel  float64 // 10% / 20% / 25%
	windowWSec float64 // окно для глифа и стационарности, сек (0.6 по умолчанию)
	aCv        float64 // std(env)/mean(env) < aCv (0.15)
	aSlope     float64 // |slope(env)|/mean(env) < aSlope (0.25)
	// Параметры упрощения/кластеризации по форме (морфология): порог формы + fallback T_shape, T_glyph
	distThr float64 // порог merge по форме (ShapeDistance < distThr → один токен); ползунок + текстовое поле
	tShape  float64 // fallback: |log(xy/xy_ref)| + |log(xz/xz_ref)| < tShape
	tGlyph  float64 // fallback: mean(|glyph_new - glyph_ref|) < tGlyph
	// Текстовое поле для ввода произвольного значения (distThr)
	distThrInput        string // буфер ввода
	distThrInputFocused bool   // фокус для клавиатуры

	// DISCOVERY: поля ввода произвольных чисел (rects заполняются в DrawDiscoveryScreen)
	discoveryInputRects    map[string]uiRect
	discoveryInputDistThr  string
	discoveryInputThrScore string
	discoveryInputThrH     string
	discoveryInputSegment  string

	// MANUAL: при Linked=false — какие ручки редактируем (L или R)
	manualEditChannel       string // "L" | "R"
	manualFreqExtra         bool   // Enable External Freq: true => 1Hz - 12kHz; false => 66.67Hz - 2kHz
	manualFreqInput         string
	manualFreqInputIdx      int
	manualFreqInputFocused  bool
	manualFreq2Input        string
	manualFreq2InputFocused bool

	// Контекст для glyph_log (f1, f2, phase, env_type, H_resid)
	glyphLogCtx alphabet.GlyphLogContext
	// Отдельный логгер для детального файла глифов (включается флагом -glyph).
	glyphLogger *log.Logger

	// Runner: STOP = graceful (finish step); PANIC = instant mute
	runnerMu            sync.Mutex
	runnerStopRequested bool

	// Кэш глифов осциллограмм: пересчёт не чаще 10 Hz на элемент
	patternOscilloCache   map[int]OscilloCacheEntry
	candidateOscilloCache map[int]OscilloCacheEntry

	// Переиспользуемые буферы (gpu_pool.go) — меньше аллокаций в Update/Draw
	fftScratch          *signal.FFTScratch
	scanFFTScratch      *signal.FFTScratch
	downSampleScratch   *signal.DownSampleScratch
	rawDetectionScratch *signal.RawDetectionScratch
	sensorScratchX      []float64
	sensorScratchY      []float64
	sensorScratchZ      []float64
	sensorScratchADS    []float64
	panelLayoutReuse    panelLayout
	cachedSyncMetrics   signal.SyncMetrics

	// SCAN: Heatmap image state (still shared for drawing, but buffer is per-sensor)
	scanHeatmapImg *ebiten.Image
	scanHeatmapPix []byte

	// LOCKIN: Heatmap image state
	lockinHeatImgs [3]*ebiten.Image // LOCKIN screen images
	scanAxisImgs   [3]*ebiten.Image // SCAN screen images (same data, separate GPU textures)
	lockinHeatPix  []byte

	// LOCKIN: live result history (selected sensor only for now)
	lockinHistDXY  []float64
	lockinHistDYZ  []float64
	lockinHistDXZ  []float64
	lockinHistMagX []float64
	lockinHistMagY []float64
	lockinHistMagZ []float64

	// TRAINER: Actions, калибровка REST/DO/RELAX
	trainerActionStore      *ActionStore
	trainerSelectedActionID int
	trainerPlannedSequence  []int // выбранная последовательность A→B→C для тренинга; клик по действию добавляет в конец
	trainerCalibrateLoop    *CalibrateLoop
	trainerNewActionName    string
	trainerRects            map[string]uiRect
	trainerActionRects      []trainerActionRect
	trainerRenameInput      string
	trainerGlyphRecorder    *GlyphStreamRecorder
	trainerGlyphBuffer      *GlyphShapeBuffer       // буфер глифов (legacy)
	trainerEventBuffer      *TrainerEventBufferImpl // буфер glyph-event'ов для калибровки → event-based датасет
	trainerReactionBuffer   *RunReactionBuffer      // буфер reaction-векторов за сегмент [Start..End] для датасета

	// MEANING (СМЫСЛ): таблица токен + счётчик, распознавание модели TF
	meaningContentTop int               // верх области контента (обновляется в DrawMeaningScreen)
	meaningContentH   int               // высота области контента (обновляется в Draw)
	mlLastPrediction  MLPrediction      // последнее предсказание модели (имя действия, confidence)
	mlWorker          *MLWorker         // инференс через Python run_ml_queue_model.py; nil если модели нет
	meaningCounters   map[string]int    // счётчик появлений по имени действия (обновляется из предиктора)
	meaningCountersMu sync.Mutex        // защита meaningCounters (пишет горутина предиктора)
	meaningRects      map[string]uiRect // кнопки на вкладке MEANING (meaningReset и т.д.)
	// Последний reaction по полосе для MEANING (обновляется в OnReactionGlyph, не очищается при Start/End)
	meaningReactionByBand      [8][alphabet.ReactionPhaseBins]float64
	meaningReactionTsByBand    [8]int64
	meaningReactionValidByBand [8]bool

	// ML-очереди (новая архитектура: FrameSummary, EventGlyph, EventReaction)
	mlQueue            *MLQueueImpl
	lastFrameSummaryMs int64

	// История количества токенов с начала сессии для графика динамики.
	tokenTrendEnabled      bool
	tokenHistory           []int
	tokenHistoryStart      time.Time
	tokenHistoryNextSample time.Time
	tokenHistoryStrideSec  int

	// TOMOGRAPHY (Томография)
	tomoSensorMap  map[string]int
	tomoPartsRects map[string]uiRect
	tomoMode       string // "Absolute" or "Difference"
	tomoViewMode   string // "Quantum" or "Circular"
	tomoMaxAmp     map[string]float64
	tomoMaxRMS     map[string]float64
	tomoFFTScratch *signal.FFTScratch

	// DYNAMICS screen history (sampled at Draw rate, capped at 5000 pts)
	dynHistPhase      []float64 // carrier phase after tare (deg)
	dynHistMag        []float64 // Z lock-in magnitude (g)
	dynHistMagX       []float64 // X lock-in magnitude (g)
	dynHistMagY       []float64 // Y lock-in magnitude (g)
	dynHistDeltaXY    []float64 // ΔΦ_XY (deg)
	dynHistDeltaYZ    []float64 // ΔΦ_YZ (deg)
	dynHistDeltaZX    []float64 // ΔΦ_ZX = Φ_Z − Φ_X (deg)
	dynHistMagTotal   []float64 // √(MagX²+MagY²+MagZ²) (g)
	dynHistPhaseX     []float64 // absolute Φ_X (deg)
	dynHistPhaseY     []float64 // absolute Φ_Y (deg)
	dynHistPhaseZ     []float64 // absolute Φ_Z (deg)
	dynHistVel        []float64 // phase velocity (deg/s)
	dynHistJitter     []float64 // TSF jitter (µs)
	dynHistTsf        []float64
	dynHistSNR        []float64 // carrier SNR (dB)
	dynPhaseZero      float64   // tare offset (deg)
	dynLastDspTsf     uint64    // detect new DSP packet
	dynLockinHardLock bool      // cached from s.LockinHardLock (set while mutex held)

	// Orbit trail for DYN screen: polar Z, and phase-phase Lissajous
	dynOrbitTrail []struct {
		P float64
		M float64
	}
	dynOrbitXY [][2]float64 // (ΦX, ΦY) trail
	dynOrbitYZ [][2]float64 // (ΦY, ΦZ) trail
	dynOrbitXZ [][2]float64 // (ΦX, ΦZ) trail

	// DYN plot checkbox state (key → enabled); persisted to config file
	dynPlotEnabled   map[string]bool
	dynCheckboxRects []struct {
		key  string
		rect uiRect
	}
	dynVoiceBtnRect uiRect

	// Manual Zoom
	zoomX float64 // 1.0 = default
	zoomY float64 // 1.0 = default

	// AVN (Acoustic Vector Navigation)
	avn avnState

	// TOMO (Mission Control) State
	tomoPhantoms     []tomoPhantom
	tomoLiveBalanceX float64 // (ΔΦL - ΔΦR)
	tomoLiveBalanceY float64 // (MagL + MagR)
	tomoLiveTwist    float64 // (PhaseL - PhaseR)
	tomoLiveSmoothX  float64
	tomoLiveSmoothY  float64
	tomoBalanceTrail [][2]float64

	// BCI RPG System
	tomoCastLevel   float64            // 0..1 current fireball charge
	tomoActiveBuffs map[string]float64 // name -> intensity/duration
}

type tomoPhantom struct {
	Name     string
	X, Y     float64    // centroid in balance space
	Triangle [3]float64 // ΔXY, ΔYZ, ΔZX reference
}

type avnState struct {
	Enabled     bool
	Calibration avnCalibration
	Live        avnLiveState
	Recording   string // "REST", "UP", etc.
	RecBuffer   [][3]float64
	RecVel      []float64
	LiveTrail   [][2]float64
	Counts      map[string]int // Number of samples per step
}

type avnCalibration struct {
	Rest        [3]float64
	Up          [3]float64
	Down        [3]float64
	Left        [3]float64
	Right       [3]float64
	MaxDelta    float64
	ClickVel    float64
	UseZClick   bool
	Initialized bool
}

type avnLiveState struct {
	X, Y             float64
	SmoothX, SmoothY float64
	Vel              float64
	IsClick          bool
	RawX, RawY       float64
	LastLog          time.Time
}

func (g *gpuGame) Layout(outsideWidth, outsideHeight int) (int, int) {
	return outsideWidth, outsideHeight
}

// meaningIncrCounter увеличивает счётчик по имени действия (вызывается из предиктора при каждом предсказании).
func (g *gpuGame) meaningIncrCounter(name string) {
	if name == "" {
		return
	}
	g.meaningCountersMu.Lock()
	defer g.meaningCountersMu.Unlock()
	if g.meaningCounters == nil {
		g.meaningCounters = make(map[string]int)
	}
	g.meaningCounters[name]++
}

// meaningResetCounters обнуляет счётчики на вкладке MEANING.
func (g *gpuGame) meaningResetCounters() {
	g.meaningCountersMu.Lock()
	g.meaningCounters = make(map[string]int)
	g.meaningCountersMu.Unlock()
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func panelWidth(w int) int {
	pw := panelMaxW
	if w < 900 {
		pw = panelMinW
	}
	if w < 760 {
		pw = 0
	}
	return pw
}

func cycleString(cur string, list []string, dir int) string {
	if len(list) == 0 {
		return cur
	}
	idx := 0
	for i, v := range list {
		if v == cur {
			idx = i
			break
		}
	}
	idx += dir
	if idx < 0 {
		idx = len(list) - 1
	}
	if idx >= len(list) {
		idx = 0
	}
	return list[idx]
}

func (g *gpuGame) buildPanelLayout(w, h int) panelLayout {
	pw := panelWidth(w)
	if pw == 0 {
		return panelLayout{show: false}
	}
	x0 := w - pw
	y0 := headerH + mainTabRowH + 6
	switch g.mainScreen {
	case "RAW":
		y0 += contentTopGap + sensorRowH + 4 + sliderH
	case "BANDS", "DISCOVERY", "ALPHABET":
		y0 += contentTopGap + sensorRowH + 4
	}
	hh := h - y0 - 6
	lay := &g.panelLayoutReuse
	if lay.tabs == nil {
		lay.tabs = make(map[string]uiRect)
		lay.buttons = make(map[string]uiRect)
		lay.sliders = make(map[string]sliderDef)
		lay.inputs = make(map[string]uiRect)
	}
	for k := range lay.tabs {
		delete(lay.tabs, k)
	}
	for k := range lay.buttons {
		delete(lay.buttons, k)
	}
	for k := range lay.sliders {
		delete(lay.sliders, k)
	}
	for k := range lay.inputs {
		delete(lay.inputs, k)
	}
	lay.show = true
	lay.x = x0
	lay.y = y0
	lay.w = pw
	lay.h = hh

	tabNames := []string{"EMITTER", "RECEIVER", "GLYPHS"}
	tabW := (pw - 2*panelPad) / len(tabNames)
	tx := x0 + panelPad
	ty := y0
	for i, name := range tabNames {
		lay.tabs[name] = uiRect{x: tx + i*tabW, y: ty, w: tabW - 2, h: tabH}
	}
	cursorY := y0 + tabH + 6
	row := func(height int) uiRect {
		r := uiRect{x: x0 + panelPad, y: cursorY, w: pw - 2*panelPad, h: height}
		cursorY += height + 6
		return r
	}
	split := func(r uiRect, cols int) []uiRect {
		out := make([]uiRect, cols)
		gap := 6
		wc := (r.w - gap*(cols-1)) / cols
		for i := 0; i < cols; i++ {
			out[i] = uiRect{x: r.x + i*(wc+gap), y: r.y, w: wc, h: r.h}
		}
		return out
	}

	tab := g.panelTab
	if tab == "" {
		tab = "EMITTER"
	}

	switch tab {
	case "EMITTER":
		// Signal type: SINE / AM-SINE / CHIRP
		r := row(btnH)
		bs := split(r, 3)
		lay.buttons["signal_sine"] = bs[0]
		lay.buttons["signal_am"] = bs[1]
		lay.buttons["signal_chirp"] = bs[2]

		// External Freq + ON toggle on the same row
		r = row(btnH)
		bs2 := split(r, 2)
		lay.buttons["freq_extra"] = bs2[0]
		lay.buttons["open"] = bs2[1]
		freqMin, freqMax := 66.67, 2000.0
		if g.manualFreqExtra {
			freqMin, freqMax = 1.0, 12000.0
		}
		// Dynamic Tones (up to 10)
		if len(g.protocol.Tones) == 0 {
			g.protocol.Tones = append(g.protocol.Tones, httpapi.Tone{
				Freq:         g.protocol.Frequency,
				LeftEnabled:  true,
				RightEnabled: true,
				LeftLevel:    float64(g.protocol.Intensity),
				RightLevel:   float64(g.protocol.Intensity),
			})
			if g.protocol.Tones[0].Freq <= 0 {
				g.protocol.Tones[0].Freq = 311.0
			}
			if g.protocol.Tones[0].LeftLevel <= 0 {
				g.protocol.Tones[0].LeftLevel = 50.0
			}
			if g.protocol.Tones[0].RightLevel <= 0 {
				g.protocol.Tones[0].RightLevel = 50.0
			}
		}
		for i := 0; i < len(g.protocol.Tones); i++ {
			r = row(rowH)
			bs := split(r, 12)
			lay.buttons[fmt.Sprintf("tone_l_en_%d", i)] = bs[0]
			lay.buttons[fmt.Sprintf("tone_r_en_%d", i)] = bs[1]
			sliderR := uiRect{bs[2].x, bs[2].y, bs[2].w + bs[3].w + bs[4].w + bs[5].w + bs[6].w + bs[7].w + bs[8].w + 36, bs[2].h}
			lay.sliders[fmt.Sprintf("freq_%d", i)] = sliderDef{rect: sliderR, min: freqMin, max: freqMax, step: 0.01}
			inputR := uiRect{bs[10].x, bs[10].y, bs[10].w + bs[11].w + 6, bs[10].h}
			lay.inputs[fmt.Sprintf("freq_val_%d", i)] = inputR
			if i > 0 {
				lay.buttons[fmt.Sprintf("del_tone_%d", i)] = bs[9]
			}

			r = row(rowH)
			bs = split(r, 2)
			lay.sliders[fmt.Sprintf("tone_l_level_%d", i)] = sliderDef{rect: bs[0], min: 0, max: 100, step: 1}
			lay.sliders[fmt.Sprintf("tone_r_level_%d", i)] = sliderDef{rect: bs[1], min: 0, max: 100, step: 1}
		}
		r = row(btnH)
		lay.buttons["add_tone"] = r

		r = row(rowH)
		lay.sliders["phase_r"] = sliderDef{rect: r, min: 0, max: 360, step: 1}

	case "RECEIVER":
		r := row(btnH)
		lay.buttons["receiver_linked"] = r
		if !g.protocol.ReceiverLinked {
			r = row(rowH)
			sliderR := uiRect{r.x, r.y, r.w - 50, r.h}
			inputR := uiRect{r.x + r.w - 44, r.y, 44, r.h}
			lay.sliders["receiver_freq"] = sliderDef{rect: sliderR, min: 66.67, max: 2000.0, step: 0.01}
			lay.inputs["receiver_freq_val"] = inputR
		}

		r = row(btnH)
		lay.buttons["run_auto_scan"] = r
		r = row(btnH)
		bs := split(r, 2)
		lay.buttons["run_burst"] = bs[0]
		lay.buttons["run_chirp"] = bs[1]
		r = row(btnH)
		bs = split(r, 2)
		lay.buttons["run_phase_scan"] = bs[0]
		lay.buttons["run_dual_asym"] = bs[1]
		r = row(btnH)
		lay.buttons["run_fast_scan"] = r
		r = row(btnH)
		bs = split(r, 4)
		lay.buttons["mode_a"] = bs[0]
		lay.buttons["mode_b"] = bs[1]
		lay.buttons["mode_c"] = bs[2]
		lay.buttons["mode_d"] = bs[3]
		r = row(btnH)
		bs = split(r, 2)
		lay.buttons["stop"] = bs[0]
		lay.buttons["panic"] = bs[1]

		r = row(btnH)
		lay.buttons["sync_trigger"] = r
		r = row(btnH)
		lay.buttons["sync_reset_calib"] = r
		r = row(52)
		lay.buttons["sync_tsf_status"] = r
		r = row(76)
		lay.buttons["sync_phase_metrics"] = r
		r = row(60)
		lay.buttons["sync_sensors_disp"] = r

	case "GLYPHS":
		r := row(btnH)
		lay.buttons["entropy_sep"] = r
		r = row(rowH)
		lay.sliders["h_thr"] = sliderDef{rect: r, min: 0.05, max: 0.95, step: 0.02}
		r = row(rowH)
		lay.sliders["min_delta_h"] = sliderDef{rect: r, min: 0.0001, max: 0.05, step: 0.0001}
		r = row(rowH)
		lay.sliders["dist_thr"] = sliderDef{rect: r, min: 0.05, max: 0.8, step: 0.02}
		r = row(rowH)
		lay.sliders["min_cos"] = sliderDef{rect: r, min: -1.0, max: 1.0, step: 0.02}
		r = row(rowH)
		lay.sliders["max_freq_ratio"] = sliderDef{rect: r, min: 1.0, max: 3.0, step: 0.05}
		r = row(rowH)
		lay.sliders["zoom_x"] = sliderDef{rect: r, min: 0.1, max: 10.0, step: 0.1}
		r = row(rowH)
		lay.sliders["zoom_y"] = sliderDef{rect: r, min: 0.1, max: 10.0, step: 0.1}
		r = row(rowH)
		lay.sliders["adjacent_dist_thr"] = sliderDef{rect: r, min: 0.2, max: 0.8, step: 0.02}
		r = row(rowH)
		lay.sliders["hysteresis_margin"] = sliderDef{rect: r, min: 0.0, max: 0.2, step: 0.01}
		r = row(rowH)
		lay.sliders["ultra_strong_dist"] = sliderDef{rect: r, min: 0.001, max: 0.05, step: 0.001}
		r = row(btnH)
		lay.buttons["x_ge"] = r
		r = row(rowH)
		lay.sliders["glyph_ttl_sec"] = sliderDef{rect: r, min: 0, max: 600, step: 30}
		r = row(rowH)
		lay.buttons["entropy_metrics"] = r
		r = row(btnH)
		lay.buttons["token_history_toggle"] = r
		r = row(52)
		lay.buttons["token_histogram"] = r
	}
	return *lay
}

const (
	rampMs   = 20
	windowMs = 450
)

// FAST SCAN: 80–1200 Hz log steps, L → R → BOTH, 450ms/step, 300ms pause between channels
var fastScanFreqs = []float64{80, 100, 125, 160, 200, 250, 315, 400, 500, 630, 800, 1000, 1200}

// PHASE SCAN: BOTH, freqs 200/400/800, each: 0° 2s, pause 300ms, 180° 2s, pause 500ms
var phaseScanFreqs = []float64{200, 400, 800}

// DUAL ASYM: pairs (fL, fR); 2s hold + 500ms pause
var dualAsymPairs = [][2]float64{{311, 401}, {250, 315}, {400, 500}, {500, 630}}

// Режимы зондирования A/B/C/D (два эксайдера, envelope обязательна).
var probingPhaseDegs = []int{0, 90, 120, 180}                                        // A: f1=f2, разные фазы
var probingBeatsPairs = [][2]float64{{300, 304}, {400, 406}, {250, 256}, {500, 505}} // B: биения |Δf| 2–8 Гц
var probingDualPairs = [][2]float64{{200, 650}, {250, 700}, {280, 800}}              // C: f1∈180–300, f2∈600–900

func (g *gpuGame) shouldStopRunner() bool {
	g.runnerMu.Lock()
	defer g.runnerMu.Unlock()
	if g.runnerStopRequested {
		return true
	}
	if g.store != nil && g.store.GetProtocol().PanicStop {
		return true
	}
	return false
}

func (g *gpuGame) requestStopRunner() {
	g.runnerMu.Lock()
	g.runnerStopRequested = true
	g.runnerMu.Unlock()
}

// runPresetSteps запускает пресет в горутине: обновляет store по шагам; STOP = graceful, PANIC = уже в протоколе.
func (g *gpuGame) runPresetSteps(presetName string) {
	g.runnerMu.Lock()
	g.runnerStopRequested = false
	g.runnerMu.Unlock()

	p := g.store.GetProtocol()
	p.Preset = "manual"
	p.SessionState = "running"
	p.PanicStop = false
	p.RampMs = rampMs
	p.WindowMs = windowMs
	p.AutoPresetName = presetName
	p.StepIndex = 0
	p.MuteLeft = false
	p.MuteRight = false
	g.store.SetProtocol(p)

	applyStep := func(ch string, freqL, freqR float64, phase int, muteL, muteR bool, stepIdx int) {
		cfg := g.store.GetProtocol()
		cfg.Channel = ch
		cfg.Frequency = freqL
		cfg.FreqStep = freqR - freqL
		cfg.PhaseOffset = phase
		cfg.MuteLeft = muteL
		cfg.MuteRight = muteR
		cfg.StepIndex = stepIdx
		g.store.SetProtocol(cfg)
		ts := time.Now().UnixMilli()
		log.Printf("preset=%s step=%d ts=%d channel=%s freq_L=%.0f freq_R=%.0f phase=%d intensity=%d",
			presetName, stepIdx, ts, ch, freqL, freqR, phase, cfg.Intensity)
	}

	muteBoth := func() {
		cfg := g.store.GetProtocol()
		cfg.MuteLeft = true
		cfg.MuteRight = true
		g.store.SetProtocol(cfg)
	}

	switch presetName {
	case "fast-scan":
		channels := []struct {
			ch    string
			muteL bool
			muteR bool
		}{{"L", false, true}, {"R", true, false}, {"Both", false, false}}
		stepIdx := 0
		for _, c := range channels {
			if g.shouldStopRunner() {
				break
			}
			for _, freq := range fastScanFreqs {
				if g.shouldStopRunner() {
					break
				}
				applyStep(c.ch, freq, freq, 0, c.muteL, c.muteR, stepIdx)
				stepIdx++
				time.Sleep(windowMs * time.Millisecond)
			}
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(300 * time.Millisecond)
		}
	case "phase-scan":
		stepIdx := 0
		for _, freq := range phaseScanFreqs {
			if g.shouldStopRunner() {
				break
			}
			applyStep("Both", freq, freq, 0, false, false, stepIdx)
			stepIdx++
			time.Sleep(2000 * time.Millisecond)
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(300 * time.Millisecond)
			applyStep("Both", freq, freq, 180, false, false, stepIdx)
			stepIdx++
			time.Sleep(2000 * time.Millisecond)
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(500 * time.Millisecond)
		}
	case "dual-asym":
		cfg := g.store.GetProtocol()
		cfg.Mode = "dual-tone"
		g.store.SetProtocol(cfg)
		stepIdx := 0
		for _, pair := range dualAsymPairs {
			if g.shouldStopRunner() {
				break
			}
			applyStep("Both", pair[0], pair[1], 0, false, false, stepIdx)
			stepIdx++
			time.Sleep(2000 * time.Millisecond)
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(500 * time.Millisecond)
		}
	}

	cfg := g.store.GetProtocol()
	cfg.SessionState = "stopped"
	cfg.AutoPresetName = ""
	cfg.MuteLeft = true
	cfg.MuteRight = true
	g.store.SetProtocol(cfg)
	log.Printf("preset=%s finished sessionState=stopped", presetName)
}

// runProbingMode запускает режим зондирования A/B/C/D (два эксайдера, envelope 0.3–6 Гц, глубина 0.3–0.6).
// A: f1=f2, фазы 0°/90°/120°/180°. B: биения |Δf|=2–8 Гц. C: разные carrier, одинаковая envelope. D: противофазная envelope.
func (g *gpuGame) runProbingMode(mode string) {
	g.runnerMu.Lock()
	g.runnerStopRequested = false
	g.runnerMu.Unlock()

	p := g.store.GetProtocol()
	p.Preset = "manual"
	p.SessionState = "running"
	p.PanicStop = false
	p.RampMs = rampMs
	p.WindowMs = windowMs
	p.ProbingMode = mode
	p.EnvType = "sine"
	p.EnvFreqHz = 1.0
	p.EnvDepth = 0.5
	p.MuteLeft = false
	p.MuteRight = false
	p.Channel = "Both"
	switch mode {
	case "A":
		p.EnvCounterPhase = false
		p.Mode = "stereo"
		p.AutoPresetName = "mode-a"
	case "B":
		p.EnvCounterPhase = false
		p.Mode = "dual-tone"
		p.AutoPresetName = "mode-b"
	case "C":
		p.EnvCounterPhase = false
		p.Mode = "dual-tone"
		p.AutoPresetName = "mode-c"
	case "D":
		p.EnvCounterPhase = true
		p.Mode = "stereo"
		p.AutoPresetName = "mode-d"
	default:
		p.AutoPresetName = ""
		g.store.SetProtocol(p)
		return
	}
	g.store.SetProtocol(p)

	apply := func(freqL, freqR float64, phase int) {
		cfg := g.store.GetProtocol()
		cfg.Frequency = freqL
		cfg.FreqStep = freqR - freqL
		cfg.PhaseOffset = phase
		cfg.MuteLeft = false
		cfg.MuteRight = false
		g.store.SetProtocol(cfg)
		log.Printf("probing=%s f1=%.0f f2=%.0f phase=%d", mode, freqL, freqR, phase)
	}
	muteBoth := func() {
		cfg := g.store.GetProtocol()
		cfg.MuteLeft = true
		cfg.MuteRight = true
		g.store.SetProtocol(cfg)
	}

	holdMs := 2000
	pauseMs := 300

	for !g.shouldStopRunner() {
		switch mode {
		case "A":
			for _, ph := range probingPhaseDegs {
				if g.shouldStopRunner() {
					break
				}
				apply(300, 300, ph)
				time.Sleep(time.Duration(holdMs) * time.Millisecond)
				if g.shouldStopRunner() {
					break
				}
				muteBoth()
				time.Sleep(time.Duration(pauseMs) * time.Millisecond)
			}
		case "B":
			for _, pair := range probingBeatsPairs {
				if g.shouldStopRunner() {
					break
				}
				apply(pair[0], pair[1], 0)
				time.Sleep(time.Duration(holdMs) * time.Millisecond)
				if g.shouldStopRunner() {
					break
				}
				muteBoth()
				time.Sleep(time.Duration(pauseMs) * time.Millisecond)
			}
		case "C":
			for _, pair := range probingDualPairs {
				if g.shouldStopRunner() {
					break
				}
				apply(pair[0], pair[1], 0)
				time.Sleep(time.Duration(holdMs) * time.Millisecond)
				if g.shouldStopRunner() {
					break
				}
				muteBoth()
				time.Sleep(time.Duration(pauseMs) * time.Millisecond)
			}
		case "D":
			apply(300, 300, 0)
			if g.shouldStopRunner() {
				break
			}
			time.Sleep(time.Duration(holdMs) * time.Millisecond)
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(time.Duration(pauseMs) * time.Millisecond)
			apply(300, 300, 180)
			if g.shouldStopRunner() {
				break
			}
			time.Sleep(time.Duration(holdMs) * time.Millisecond)
			if g.shouldStopRunner() {
				break
			}
			muteBoth()
			time.Sleep(time.Duration(pauseMs) * time.Millisecond)
		}
	}

	cfg := g.store.GetProtocol()
	cfg.SessionState = "stopped"
	cfg.AutoPresetName = ""
	cfg.ProbingMode = ""
	cfg.MuteLeft = true
	cfg.MuteRight = true
	g.store.SetProtocol(cfg)
	log.Printf("probing=%s finished sessionState=stopped", mode)
}

func dropLastRune(s string) string {
	if s == "" {
		return s
	}
	_, size := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-size]
}

func (g *gpuGame) getSensorState(id int) *sensorGUIState {
	if g.sensorStates == nil {
		g.sensorStates = make(map[int]*sensorGUIState)
	}
	if g.sensorStates[id] == nil {
		g.sensorStates[id] = &sensorGUIState{
			lockinHeatX:        make([][]float64, 0),
			lockinHeatY:        make([][]float64, 0),
			lockinHeatZ:        make([][]float64, 0),
			scanHeatmapBuf:     make([][]float64, 0),
			scanEdgeHeatmapBuf: make([][32]uint8, 0),
		}
	}
	return g.sensorStates[id]
}

// getSelectedSensorID возвращает ID сенсора для отображения.
func (g *gpuGame) getSelectedSensorID() uint8 {
	ids := g.buf.ListSensors()
	if len(ids) == 0 {
		return 0
	}
	if g.selectedSensorID < 0 {
		return ids[0]
	}
	for _, id := range ids {
		if int(id) == g.selectedSensorID {
			return id
		}
	}
	return ids[0]
}

// getSelectedSensorData возвращает X,Y,Z,ADS для выбранного сенсора (буферы переиспользуются, не аллоцировать).
func (g *gpuGame) getSelectedSensorData() (x, y, z, ads []float64) {
	id := g.getSelectedSensorID()
	g.buf.GetSensorDataInto(id, &g.sensorScratchX, &g.sensorScratchY, &g.sensorScratchZ, &g.sensorScratchADS)
	return g.sensorScratchX, g.sensorScratchY, g.sensorScratchZ, g.sensorScratchADS
}

// getPatternStore возвращает PatternStore для сенсора sensorID (создаёт при необходимости).
func (g *gpuGame) getPatternStore(sensorID int) *alphabet.PatternStore {
	g.sensorStoresMu.Lock()
	defer g.sensorStoresMu.Unlock()
	if g.patternStoreBySensor == nil {
		g.patternStoreBySensor = make(map[int]*alphabet.PatternStore)
	}
	if g.patternStoreBySensor[sensorID] == nil {
		ps := &alphabet.PatternStore{}
		if g.glyphLogger != nil {
			ps.SetGlyphLogger(g.glyphLogger)
		}
		g.patternStoreBySensor[sensorID] = ps
	}
	return g.patternStoreBySensor[sensorID]
}

// getReactionAlphabetStore возвращает ReactionAlphabetStore для сенсора sensorID (создаёт при необходимости).
func (g *gpuGame) getReactionAlphabetStore(sensorID int) *alphabet.ReactionAlphabetStore {
	g.sensorStoresMu.Lock()
	defer g.sensorStoresMu.Unlock()
	if g.reactionStoreBySensor == nil {
		g.reactionStoreBySensor = make(map[int]*alphabet.ReactionAlphabetStore)
	}
	if g.reactionStoreBySensor[sensorID] == nil {
		g.reactionStoreBySensor[sensorID] = alphabet.NewReactionAlphabetStore()
	}
	return g.reactionStoreBySensor[sensorID]
}

// getPrimaryAlphabetStore возвращает PrimaryAlphabetStore для сенсора sensorID (создаёт при необходимости).
func (g *gpuGame) getPrimaryAlphabetStore(sensorID int) *alphabet.PrimaryAlphabetStore {
	g.sensorStoresMu.Lock()
	defer g.sensorStoresMu.Unlock()
	if g.primaryStoreBySensor == nil {
		g.primaryStoreBySensor = make(map[int]*alphabet.PrimaryAlphabetStore)
	}
	if g.primaryStoreBySensor[sensorID] == nil {
		g.primaryStoreBySensor[sensorID] = alphabet.NewPrimaryAlphabetStore()
	}
	return g.primaryStoreBySensor[sensorID]
}
