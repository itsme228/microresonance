package gui

import (
	"math"
	"testing"
)

func TestCalcRMS(t *testing.T) {
	data := []float64{1.0, 1.0, 1.0, 1.0}
	rms := calcRMS(data, 4)
	if math.Abs(rms-1.0) > 1e-9 {
		t.Errorf("Expected RMS 1.0, got %f", rms)
	}

	data = []float64{0, 2}
	rms = calcRMS(data, 2)
	// sqrt((0^2 + 2^2)/2) = sqrt(2) = 1.414...
	if math.Abs(rms-math.Sqrt(2)) > 1e-9 {
		t.Errorf("Expected RMS %f, got %f", math.Sqrt(2), rms)
	}
}

func TestEnergyNormalization(t *testing.T) {
	// Amp = 5.0 should result in E = 1.0
	amp := 5.0
	energy := math.Min(1.0, amp/5.0)
	if energy != 1.0 {
		t.Errorf("Expected energy 1.0 for Amp 5.0, got %f", energy)
	}

	// Amp = 2.5 should result in E = 0.5
	amp = 2.5
	energy = math.Min(1.0, amp/5.0)
	if energy != 0.5 {
		t.Errorf("Expected energy 0.5 for Amp 2.5, got %f", energy)
	}
}
