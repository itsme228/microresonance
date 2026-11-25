package vrws

import (
	"net/http"
	"sync"
	"server/internal/ports/httpapi"
)

type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}
}

type client struct {
	conn interface{} // stub for websocket.Conn
	send chan []byte
}

func NewHub() *Hub {
	return &Hub{clients: make(map[*client]struct{})}
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {}
func (h *Hub) ClientCount() int { return len(h.clients) }

// --- Web GUI Types ---

type VRPatternCard struct {
	SensorID    int       `json:"sensor_id"`
	ID          int       `json:"id"`
	BandIdx     int       `json:"band_idx"`
	Occurrences int       `json:"occurrences"`
	Freq        float64   `json:"freq"`
	Shape       []float64 `json:"shape"`
	ShapeX      []float64 `json:"shape_x"`
	ShapeY      []float64 `json:"shape_y"`
	ShapeZ      []float64 `json:"shape_z"`
	PeakProfile []float64 `json:"peak_profile,omitempty"`
}

type VRReactionCard struct {
	SensorID    int       `json:"sensor_id"`
	ID          int       `json:"id"`
	BandIdx     int       `json:"band_idx"`
	Occurrences int       `json:"occurrences"`
	Energy      float64   `json:"energy"`
	Vector      []float64 `json:"vector"`
}

type VRDiscoveryCell struct {
	ExciterID   int     `json:"exciter_id"`
	ExciterName string  `json:"exciter_name"`
	BandIdx     int     `json:"band_idx"`
	RawE        float64 `json:"raw_e"`
}

type VRTrainerAction struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	Samples int     `json:"samples"`
	Quality float64 `json:"quality"`
}

type VRTrainerSnapshot struct {
	Calibrating     bool              `json:"calibrating"`
	Phase           string            `json:"phase"`
	CurrentAction   string            `json:"current_action"`
	SegmentsDone    int               `json:"segments_done"`
	SegmentsTotal   int               `json:"segments_total"`
	Actions         []VRTrainerAction `json:"actions"`
	SelectedActionID int              `json:"selected_action_id"`
}

type VRMeaningSnapshot struct {
	ModelLoaded    bool           `json:"model_loaded"`
	PredictionName string         `json:"prediction_name"`
	Confidence     float64        `json:"confidence"`
	Counters       map[string]int `json:"counters"`
	Actions        []string       `json:"actions"`
}

type VRBandWaveform struct {
	Label   string    `json:"label"`
	XB      []float64 `json:"xb"`
	YB      []float64 `json:"yb"`
	ZB      []float64 `json:"zb"`
	Squared []float64 `json:"squared"`
}

type VRXcorrPair struct {
	Key string    `json:"key"`
	CCF []float64 `json:"ccf"`
}

type VRShapeMetrics struct {
	HFinal          float64 `json:"h_final"`
	TokensTotal     int     `json:"tokens_total"`
	PeaksTotal      int     `json:"peaks_total"`
	PeaksClassified int     `json:"peaks_classified"`
	PeaksRejected   int     `json:"peaks_rejected"`
}

type VRFullSnapshotInput struct {
	Patterns      []VRPatternCard    `json:"patterns"`
	Reactions     []VRReactionCard   `json:"reactions"`
	Discovery     []VRDiscoveryCell  `json:"discovery"`
	Trainer       VRTrainerSnapshot  `json:"trainer"`
	Meaning       VRMeaningSnapshot  `json:"meaning"`
	Protocol      httpapi.ProtocolConfig `json:"protocol"`
	Transmitter   httpapi.TransmitterConfig `json:"transmitter"`
	Receiver      httpapi.ReceiverConfig `json:"receiver"`
	BandEnergy    [8]float64         `json:"band_energy"`
	// ... add more if needed
}

func (h *Hub) BroadcastFullSnapshot(input VRFullSnapshotInput) {}
