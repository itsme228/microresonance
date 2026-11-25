package gui

import "time"

const tokenHistoryMaxPoints = 4096

func (g *gpuGame) totalTokensAcrossSensors() int {
	n := 0
	for _, sid := range g.buf.ListSensors() {
		if ps := g.getPatternStore(int(sid)); ps != nil {
			n += ps.Count()
		}
	}
	return n
}

func (g *gpuGame) compactTokenHistory() {
	if len(g.tokenHistory) <= tokenHistoryMaxPoints {
		return
	}
	compacted := make([]int, 0, (len(g.tokenHistory)+1)/2)
	for i := 0; i < len(g.tokenHistory); i += 2 {
		compacted = append(compacted, g.tokenHistory[i])
	}
	g.tokenHistory = compacted
	if g.tokenHistoryStrideSec <= 0 {
		g.tokenHistoryStrideSec = 1
	} else {
		g.tokenHistoryStrideSec *= 2
	}
	g.tokenHistoryNextSample = g.tokenHistoryStart.Add(time.Duration(len(g.tokenHistory)*g.tokenHistoryStrideSec) * time.Second)
}

func (g *gpuGame) sampleTokenHistory(now time.Time) {
	if g.tokenHistoryStrideSec <= 0 {
		g.tokenHistoryStrideSec = 1
	}
	if g.tokenHistoryStart.IsZero() {
		g.tokenHistoryStart = now
		g.tokenHistoryNextSample = now
	}
	if g.tokenHistoryNextSample.IsZero() {
		g.tokenHistoryNextSample = g.tokenHistoryStart
	}
	if now.Before(g.tokenHistoryNextSample) {
		return
	}
	current := g.totalTokensAcrossSensors()
	step := time.Duration(g.tokenHistoryStrideSec) * time.Second
	for !now.Before(g.tokenHistoryNextSample) {
		g.tokenHistory = append(g.tokenHistory, current)
		g.tokenHistoryNextSample = g.tokenHistoryNextSample.Add(step)
	}
	g.compactTokenHistory()
}
