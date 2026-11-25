// GPU runtime: interactive update/draw loop, sliders, sensor selector, and Ebiten entrypoint.

package gui

import (
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"server/internal/domain/signal"
	"server/internal/ports/httpapi"
)

var timeWindow = func() *time.Duration { d := 500 * time.Millisecond; return &d }()

// SetTimeWindow propagates the CLI -time flag into the gui package.
func SetTimeWindow(d *time.Duration) { timeWindow = d }

var adxlRate = func() *int { v := 4000; return &v }()
var adsRate = func() *int { v := 40000; return &v }()

// SetSampleRates propagates the CLI -adxlRate/-adsRate flags into the gui package.
func SetSampleRates(adxl, ads *int) { adxlRate = adxl; adsRate = ads }

func (g *gpuGame) drawFFTSliders(screen *ebiten.Image, sy0, w int) {
	sy := sy0 + 2
	x0 := plotPad
	gap := 8
	autoW := 36
	autoGap := 4
	trackW := (w - x0 - 3*fftLabelWidth - autoW - autoGap - 2*gap) / 3
	if trackW < 40 {
		trackW = 40
	}
	if trackW > 180 {
		trackW = 180
	}
	drawSl := func(sx int, label string, val float64, manual string) {
		tx := sx + fftLabelWidth
		vector.FillRect(screen, float32(tx), float32(sy), float32(trackW), 14, clrGrd, true)
		thx := tx + int((val)*(float64(trackW)-float64(thumbW)))
		if thx < tx {
			thx = tx
		}
		if thx > tx+trackW-thumbW {
			thx = tx + trackW - thumbW
		}
		vector.FillRect(screen, float32(thx), float32(sy+1), float32(thumbW), 12, clrAcc, true)
		ebitenutil.DebugPrintAt(screen, label+manual, sx, sy-1)
	}

	tv := (float64((*timeWindow).Milliseconds()) - 50) / 4950
	if tv < 0 {
		tv = 0
	}
	if tv > 1 {
		tv = 1
	}
	if g.dragSlider == "1" {
		tv = g.dragValue
	}
	drawSl(x0, "t:", tv, fmt.Sprintf("%dms", (*timeWindow).Milliseconds()))

	ySx := x0 + fftLabelWidth + trackW + gap
	autoX := ySx + fftLabelWidth
	yTrackX := autoX + autoW + autoGap
	slClrBtn := clrGrd
	if g.vertAuto {
		slClrBtn = clrAuto
	}
	vector.StrokeRect(screen, float32(autoX), float32(sy-2), float32(autoW), 14, 1, slClrBtn, true)

	var vv float64
	if g.vertAuto {
		vv = 1
		ebitenutil.DebugPrintAt(screen, "Y:", ySx, sy-1)
		ebitenutil.DebugPrintAt(screen, "Auto", autoX+4, sy-1)
	} else {
		vv = (g.vertMult - 0.25) / 3.75
		if vv < 0 {
			vv = 0
		}
		if vv > 1 {
			vv = 1
		}
		if g.dragSlider == "2" {
			vv = g.dragValue
		}
		ebitenutil.DebugPrintAt(screen, "Y:"+fmt.Sprintf("x%.2g", g.vertMult), ySx, sy-1)
		vector.FillRect(screen, float32(yTrackX), float32(sy), float32(trackW), 14, clrGrd, true)
		thx := yTrackX + int((vv)*(float64(trackW)-float64(thumbW)))
		if thx < yTrackX {
			thx = yTrackX
		}
		if thx > yTrackX+trackW-thumbW {
			thx = yTrackX + trackW - thumbW
		}
		vector.FillRect(screen, float32(thx), float32(sy+1), float32(thumbW), 12, clrAcc, true)
	}

	fftSx := ySx + fftLabelWidth + autoW + autoGap + trackW + gap
	fsv := 0.0
	switch g.fftSize {
	case 512:
		fsv = 0.05
	case 1024:
		fsv = 0.35
	case 2048:
		fsv = 0.65
	case 4096:
		fsv = 0.95
	default:
		g.fftSize = 2048
		fsv = 0.65
	}
	if g.dragSlider == "3" {
		fsv = g.dragValue
	}
	drawSl(fftSx, "FFT:", fsv, fmt.Sprintf("%d", g.fftSize))
}

func (g *gpuGame) drawSensorDropdown(screen *ebiten.Image, x0, y0, w int) int {
	ids := g.buf.ListSensors()
	g.sensorDropdownRects = g.sensorDropdownRects[:0]
	rowH := 20
	ebitenutil.DebugPrintAt(screen, "Sensor:", x0, y0+2)
	btnX := x0 + fftLabelWidth
	btnW := 28
	btnH := 18
	for _, id := range ids {
		sid := int(id)
		on := g.selectedSensorID < 0 && sid == int(g.getSelectedSensorID()) || g.selectedSensorID == sid
		fill := clrBtn
		if on {
			fill = clrBtnOn
		}
		r := uiRect{x: btnX, y: y0, w: btnW, h: btnH}
		vector.FillRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), fill, true)
		vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 1, clrGrid, true)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%d", sid), r.x+6, r.y+2)
		g.sensorDropdownRects = append(g.sensorDropdownRects, struct {
			id   int
			rect uiRect
		}{sid, r})
		btnX += btnW + 4
	}
	return y0 + rowH
}

func (g *gpuGame) Update() error {
	updateStart := time.Now()
	if ebiten.IsKeyPressed(ebiten.KeyQ) {
		return ebiten.Termination
	} else if ebiten.IsKeyPressed(ebiten.KeyZ) {
		sid := g.getSelectedSensorID()
		g.buf.Mu.Lock()
		if s := g.buf.Sensors[sid]; s != nil && len(s.PhaseHistory) > 0 {
			g.dynPhaseZero = float64(s.PhaseHistory[len(s.PhaseHistory)-1])
		}
		g.buf.Mu.Unlock()
	}
	g.handleInput()

	if g.textFocus != "" {
		if g.textCooldown > 0 {
			g.textCooldown--
		}
		var chars []rune
		chars = ebiten.AppendInputChars(chars)
		if len(chars) > 0 {
			for _, r := range chars {
				if r >= 32 && r <= 126 {
					if g.textFocus == "scanSessionName" {
						g.scanSessionName += string(r)
					}
				}
			}
		}
		if ebiten.IsKeyPressed(ebiten.KeyBackspace) && g.textCooldown == 0 {
			if g.textFocus == "scanSessionName" {
				g.scanSessionName = dropLastRune(g.scanSessionName)
			}
			g.textCooldown = 8
		}
		if ebiten.IsKeyPressed(ebiten.KeyEnter) && g.textCooldown == 0 {
			if g.textFocus == "scanSessionName" {
				g.textFocus = ""
			}
			g.textCooldown = 8
		}
	}

	if g.store != nil && g.syncTick%6 == 0 && g.dragSlider == "" && g.textFocus == "" {
		g.protocol = g.store.GetProtocol()

		var enabledF []float64
		for _, t := range g.protocol.Tones {
			if t.LeftEnabled || t.RightEnabled {
				enabledF = append(enabledF, t.Freq)
			}
		}

		if len(enabledF) > 0 {
			g.protocol.Freq1 = enabledF[0]
			g.protocol.Frequency = enabledF[0]
		}

		if g.buf != nil {
			g.buf.Mu.Lock()
			if len(enabledF) >= 2 {
				g.buf.MimoFreq2Hz = enabledF[1]
				g.protocol.Freq2 = enabledF[1]
			} else {
				g.buf.MimoFreq2Hz = 0
				g.protocol.Freq2 = 0
			}
			g.buf.Mu.Unlock()
		}
	}

	now := time.Now()
	if now.Sub(g.lastDebugLogTs) >= 5*time.Second {
		fps := ebiten.ActualFPS()
		tps := ebiten.ActualTPS()
		avgDraw := 0.0
		if g.perfDrawFrames > 0 {
			avgDraw = float64(g.perfDrawTotal.Milliseconds()) / float64(g.perfDrawFrames)
		}
		log.Printf("[UI-PERF] FPS: %.1f, TPS: %.1f, AvgDraw: %.2fms, SlowDraws: %d",
			fps, tps, avgDraw, g.perfDrawSlow33)

		g.perfDrawTotal = 0
		g.perfDrawFrames = 0
		g.perfDrawSlow33 = 0
		g.lastDebugLogTs = now
	}

	g.perfFrames++
	g.perfUpdateTotal += time.Since(updateStart)
	return nil
}

func Run(buf *signal.Buffers, store *httpapi.ConfigStore) {
	ebiten.SetWindowTitle("Microresonance Scope — 500 Hz TSF Lock-In & Telemetry Recorder")
	ebiten.SetWindowSize(1200, 750)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	g := &gpuGame{
		buf:               buf,
		store:             store,
		protocol:          store.GetProtocol(),
		vertAuto:          true,
		vertMult:          1,
		fftSize:           2048,
		freqLog:           true,
		panelTab:          "EMITTER",
		mainScreen:        "LOCKIN",
		selectedSensorID:  -1,
		lastDebugLogTs:    time.Now(),
		zoomX:             1.0,
		zoomY:             1.0,
		manualEditChannel: "L",
	}

	if err := ebiten.RunGame(g); err != nil && !errors.Is(err, ebiten.Termination) {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func (g *gpuGame) applyFreqInput() {
	if g.manualFreqInput == "" {
		return
	}
	var val float64
	_, err := fmt.Sscanf(g.manualFreqInput, "%f", &val)
	if err != nil {
		return
	}
	minF, maxF := 66.67, 2000.0
	if g.manualFreqExtra {
		minF, maxF = 1.0, 12000.0
	}
	if val < minF {
		val = minF
	}
	if val > maxF {
		val = maxF
	}

	if g.textFocus == "receiver_freq_val" {
		p := g.store.GetProtocol()
		p.ReceiverFreq = val
		g.store.SetProtocol(p)
		g.protocol = p
		g.manualFreqInput = ""
		return
	}

	if g.manualFreqInputIdx >= 0 && g.manualFreqInputIdx < len(g.protocol.Tones) {
		p := g.store.GetProtocol()
		if g.manualFreqInputIdx >= 0 && g.manualFreqInputIdx < len(p.Tones) {
			p.Tones[g.manualFreqInputIdx].Freq = val
			g.store.SetProtocol(p)
			g.protocol = p
		}
		g.manualFreqInput = ""
		return
	}

	p := g.store.GetProtocol()
	p.Freq1 = val
	p.Frequency = val
	if p.Linked || p.MimoLocked {
		p.Freq2 = val
		p.FrequencyR = val
	}
	g.store.SetProtocol(p)
	g.protocol = p
	g.manualFreqInput = ""
}

func (g *gpuGame) applyFreq2Input() {
	if g.manualFreq2Input == "" {
		return
	}
	if g.protocol.MimoLocked {
		g.manualFreq2Input = ""
		return
	}
	var val float64
	_, err := fmt.Sscanf(g.manualFreq2Input, "%f", &val)
	if err != nil {
		return
	}
	minF, maxF := 66.67, 2000.0
	if g.manualFreqExtra {
		minF, maxF = 1.0, 12000.0
	}
	if val < minF {
		val = minF
	}
	if val > maxF {
		val = maxF
	}

	p := g.store.GetProtocol()
	p.Freq2 = val
	p.FrequencyR = val
	g.store.SetProtocol(p)
	g.protocol = p
	g.manualFreq2Input = ""
	if g.buf != nil {
		g.buf.Mu.Lock()
		g.buf.MimoFreq2Hz = val
		g.buf.Mu.Unlock()
	}
}
