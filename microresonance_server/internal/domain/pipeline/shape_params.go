package pipeline

// ShapeParams are runtime knobs for entropy decomposition and alphabet compaction.
type ShapeParams struct {
	HThr                   float64 `json:"h_thr"`
	MinDeltaH              float64 `json:"min_delta_h"`
	DistThr                float64 `json:"dist_thr"`
	MinCos                 float64 `json:"min_cos"`
	MaxFreqRatio           float64 `json:"max_freq_ratio"`
	AdjacentDistThr        float64 `json:"adjacent_dist_thr"`
	HysteresisMargin       float64 `json:"hysteresis_margin"`
	UltraStrongDist        float64 `json:"ultra_strong_dist"`
	ReactionMinOccurrences int     `json:"reaction_min_occurrences"`
	GlyphTTLSec            int     `json:"glyph_ttl_sec"`
}
