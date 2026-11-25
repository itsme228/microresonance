package httpapi

func syncLegacyToneFields(c *ProtocolConfig) {
	if c == nil {
		return
	}
	leftOn, rightOn := false, false
	active := make([]Tone, 0, len(c.Tones))
	for i := range c.Tones {
		t := c.Tones[i]
		if t.LeftEnabled {
			leftOn = true
		}
		if t.RightEnabled {
			rightOn = true
		}
		if !toneEnabledAny(t) || t.Freq <= 0 {
			continue
		}
		active = append(active, t)
	}
	c.MuteLeft = !leftOn
	c.MuteRight = !rightOn
	if leftOn {
		c.Intensity = 100
		c.Level1 = 100
	} else {
		c.Intensity = 0
		c.Level1 = 0
	}
	if rightOn {
		c.IntensityR = 100
		c.Level2 = 100
	} else {
		c.IntensityR = 0
		c.Level2 = 0
	}
	if len(active) == 0 {
		c.Freq2 = 0
		return
	}

	c.Freq1 = active[0].Freq
	c.Frequency = active[0].Freq

	if c.MimoLocked {
		c.Freq2 = active[0].Freq
		c.FrequencyR = active[0].Freq
		return
	}

	if len(active) > 1 {
		c.Freq2 = active[1].Freq
		c.FrequencyR = active[1].Freq
		return
	}

	c.Freq2 = 0
	c.FrequencyR = active[0].Freq
}

func toneEnabledAny(t Tone) bool {
	return t.LeftEnabled || t.RightEnabled
}

func toneEnabledLeft(t Tone) bool {
	return t.LeftEnabled
}

func toneEnabledRight(t Tone) bool {
	return t.RightEnabled
}
