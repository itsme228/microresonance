package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/guptarohit/asciigraph"
	"github.com/rivo/tview"
	"server/internal/domain/ingest"
	"server/internal/domain/signal"
)

type App struct {
	tviewApp *tview.Application
	buffers  *signal.Buffers
	cfgFn    func() ingest.Config
	refresh  time.Duration
	plotW    *int
	plotH    *int
	timeWin  *time.Duration
	logLines *int
}

func NewApp(b *signal.Buffers, cfgFn func() ingest.Config, refresh time.Duration, plotW, plotH *int, timeWin *time.Duration, logLines *int) *App {
	return &App{
		tviewApp: tview.NewApplication(),
		buffers:  b,
		cfgFn:    cfgFn,
		refresh:  refresh,
		plotW:    plotW,
		plotH:    plotH,
		timeWin:  timeWin,
		logLines: logLines,
	}
}

func (a *App) Run() error {
	header := tview.NewTextView().SetDynamicColors(false).SetScrollable(false)

	newGraphBox := func(title string) *tview.TextView {
		tv := tview.NewTextView().SetDynamicColors(false)
		tv.SetBorder(true).SetTitle(" " + title + " ").SetTitleAlign(tview.AlignLeft)
		tv.SetScrollable(false)
		return tv
	}
	graphX := newGraphBox("ADXL X (g)")
	graphY := newGraphBox("ADXL Y (g)")
	graphZ := newGraphBox("ADXL Z (g)")
	graphADS := newGraphBox("ADS8681 (V ±2.56)")

	graphsFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(graphX, 0, 1, false).
		AddItem(graphY, 0, 1, false).
		AddItem(graphZ, 0, 1, false).
		AddItem(graphADS, 0, 1, false)

	logsView := tview.NewTextView().SetDynamicColors(false)
	logsView.SetBorder(true).SetTitle(" ESP32 logs ").SetTitleAlign(tview.AlignLeft)
	logsView.SetScrollable(false)

	mainFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(graphsFlex, 0, 1, false).
		AddItem(logsView, *a.logLines+2, 0, false)

	redraw := func() {
		a.buffers.Mu.Lock()
		pkts := a.buffers.Packets
		logCnt := a.buffers.LogCount
		from := a.buffers.LastFrom
		var x, y, z, ad []float64
		if len(a.buffers.Sensors) > 0 {
			var firstID uint8 = 255
			for id := range a.buffers.Sensors {
				if id < firstID {
					firstID = id
				}
			}
			if s := a.buffers.Sensors[firstID]; s != nil {
				x = signal.CopyFloats(s.AdxlX, nil)
				y = signal.CopyFloats(s.AdxlY, nil)
				z = signal.CopyFloats(s.AdxlZ, nil)
				ad = signal.CopyFloats(s.Ads, nil)
			}
		}
		logs := append([]string(nil), a.buffers.Logs...)
		a.buffers.Mu.Unlock()

		header.SetText(fmt.Sprintf(" MR scope | pkts=%d logs=%d from %s | time=%s w=%d h=%d | [ ] time , . w  h y ht | Ctrl+C ", pkts, logCnt, from, *a.timeWin, *a.plotW, *a.plotH))

		plot := func(d []float64) string {
			if len(d) < 2 {
				return ""
			}
			return asciigraph.Plot(downsample(d, *a.plotW), asciigraph.Height(*a.plotH), asciigraph.Width(*a.plotW))
		}
		graphX.SetText(plot(x))
		graphY.SetText(plot(y))
		graphZ.SetText(plot(z))
		graphADS.SetText(plot(ad))

		n := *a.logLines
		if n > len(logs) {
			n = len(logs)
		}
		start := len(logs) - n
		if start < 0 {
			start = 0
		}
		var b strings.Builder
		for i := start; i < len(logs); i++ {
			line := logs[i]
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
		logsView.SetText(strings.TrimSuffix(b.String(), "\n"))
	}

	a.tviewApp.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyCtrlC {
			a.tviewApp.Stop()
			return nil
		}
		return ev
	})

	go func() {
		tick := time.NewTicker(a.refresh)
		for range tick.C {
			a.tviewApp.QueueUpdateDraw(redraw)
		}
	}()

	a.tviewApp.SetRoot(mainFlex, true)
	return a.tviewApp.Run()
}

func downsample(s []float64, w int) []float64 {
	if len(s) <= w {
		return s
	}
	out := make([]float64, w)
	step := float64(len(s)-1) / float64(w-1)
	for i := 0; i < w; i++ {
		idx := int(step * float64(i))
		if idx >= len(s) {
			idx = len(s) - 1
		}
		out[i] = s[idx]
	}
	return out
}
