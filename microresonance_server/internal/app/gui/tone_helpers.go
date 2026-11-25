package gui

import "server/internal/ports/httpapi"

func enabledTones(tones []httpapi.Tone) []httpapi.Tone {
	out := make([]httpapi.Tone, 0, len(tones))
	for _, t := range tones {
		if (t.LeftEnabled || t.RightEnabled) && t.Freq > 0 {
			out = append(out, t)
		}
	}
	return out
}

func (g *gpuGame) toneMarkerFreqs() []float64 {
	return toneMarkerFreqsFromProtocol(g.protocol)
}

func toneMarkerFreqsFromProtocol(p httpapi.ProtocolConfig) []float64 {
	active := enabledTones(p.Tones)
	out := make([]float64, 0, len(active))
	for _, t := range active {
		out = append(out, t.Freq)
	}
	return out
}
