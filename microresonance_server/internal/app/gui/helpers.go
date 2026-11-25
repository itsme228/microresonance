package gui

import (
	"server/internal/domain/alphabet"
	"server/internal/ports/vrws"
)

func buildGlyphCards(list []alphabet.Pattern, sid int) []vrws.VRPatternCard {
	out := make([]vrws.VRPatternCard, len(list))
	for i, p := range list {
		out[i] = vrws.VRPatternCard{
			SensorID:    sid,
			ID:          p.ID,
			BandIdx:     p.BandIdx,
			Occurrences: p.Occurrences,
			Freq:        p.Freq,
			Shape:       p.GlyphShape.Points,
		}
	}
	return out
}

func buildReactionCards(list []alphabet.ReactionPattern, sid int) []vrws.VRReactionCard {
	out := make([]vrws.VRReactionCard, len(list))
	for i, r := range list {
		out[i] = vrws.VRReactionCard{
			SensorID:    sid,
			ID:          r.ID,
			BandIdx:     r.BandID,
			Occurrences: r.Occurrences,
			Energy:      r.TotalEnergy,
			Vector:      r.Vector[:],
		}
	}
	return out
}

func isGUISnapshotDemandActive(d any) bool { return true }
func ingestPerfSnapshotAndReset() any      { return struct{ ProcPackets int }{} }
func ingestLastProcAge() any {
	return struct{ Milliseconds func() int64 }{func() int64 { return 0 }}
}
func applyKey(r rune)             {}
func SaveFullSnapshot(input any) any { return nil }
