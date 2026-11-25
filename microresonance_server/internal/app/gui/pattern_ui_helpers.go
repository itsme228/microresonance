package gui

import "fmt"

const maxLastNShapes = 256

func formatFreqForCard(p Pattern) string {
	if p.Freq > 0 {
		if p.Freq >= 1000 {
			return fmt.Sprintf("%.0fk Hz", p.Freq/1000)
		}
		return fmt.Sprintf("%.0f Hz", p.Freq)
	}
	if p.FreqRangeHz[0] > 0 || p.FreqRangeHz[1] > 0 {
		f0, f1 := p.FreqRangeHz[0], p.FreqRangeHz[1]
		if f0 <= 0 {
			f0 = f1
		}
		if f1 <= 0 {
			f1 = f0
		}
		if f0 == f1 {
			if f0 >= 1000 {
				return fmt.Sprintf("%.0fk Hz", f0/1000)
			}
			return fmt.Sprintf("%.0f Hz", f0)
		}
		return fmt.Sprintf("%.0f–%.0f Hz", f0, f1)
	}
	return ""
}

func formatBandForCard(p Pattern) string {
	if p.BandIdx < 0 || p.BandIdx >= len(BandsFilterbank) {
		return ""
	}
	return BandsFilterbank[p.BandIdx].Label
}
