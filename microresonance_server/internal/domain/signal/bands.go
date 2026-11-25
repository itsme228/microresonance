package signal

// BandDef — одна полоса filterbank (канал наблюдения).
type BandDef struct {
	Label string
	LoHz  float64
	HiHz  float64
	Desc  string
}

// BandsFilterbank — сетка полос по рекомендации (логарифмически, 7–8 полос).
var BandsFilterbank = []BandDef{
	{Label: "B0", LoHz: 0.2, HiHz: 2, Desc: "slow modulation / drift"},
	{Label: "B1", LoHz: 2, HiHz: 10, Desc: "envelope, rhythm"},
	{Label: "B2", LoHz: 10, HiHz: 40, Desc: "mechanical vibrations"},
	{Label: "B3", LoHz: 40, HiHz: 120, Desc: "structural resonances"},
	{Label: "B4", LoHz: 120, HiHz: 400, Desc: "excitation / impacts"},
	{Label: "B5", LoHz: 400, HiHz: 1500, Desc: "combs, contact"},
	{Label: "B6", LoHz: 1500, HiHz: 2500, Desc: "carrier + distortion"},
	{Label: "B7", LoHz: 2500, HiHz: 5000, Desc: "micro-vibrations / noise signature"},
}
