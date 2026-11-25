package alphabet

import (
	"testing"
)

func TestExciterIDFromPresetName(t *testing.T) {
	tests := []struct {
		preset string
		want   int
	}{
		{"", 0},
		{"manual", 0},
		{"am-sine", 1},
		{"chirp", 2},
		{"phase-scan", 3},
		{"dual-asym", 4},
		{"fast-scan", 5},
		{"burst", 6},
		{"mode-a", 7},
		{"mode-b", 8},
		{"mode-c", 9},
		{"mode-d", 10},
	}
	for _, tt := range tests {
		got := ExciterIDFromPresetName(tt.preset)
		if got != tt.want {
			t.Errorf("ExciterIDFromPresetName(%q) = %d, want %d", tt.preset, got, tt.want)
		}
	}
}

func TestPrimaryAlphabetStoreUpdate(t *testing.T) {
	s := NewPrimaryAlphabetStore()
	s.Update(1, 3, 0.5, 1000)
	s.Update(1, 3, 0.6, 2000)
	c := s.Cell(1, 3)
	if c == nil {
		t.Fatal("Cell(1,3) should exist after Update")
	}
	if c.E < 0.1 {
		t.Errorf("Cell.E = %v, want > 0.1", c.E)
	}
	if s.Count() != 1 {
		t.Errorf("Count() = %d, want 1", s.Count())
	}
}

func TestPrimaryAlphabetStoreTop2Bands(t *testing.T) {
	s := NewPrimaryAlphabetStore()
	s.Update(0, 2, 0.3, 1000)
	s.Update(0, 4, 0.5, 1000)
	c2 := s.Cell(0, 2)
	c4 := s.Cell(0, 4)
	if c2 == nil || c4 == nil {
		t.Fatal("both cells should exist")
	}
	if s.Count() != 2 {
		t.Errorf("Count() = %d, want 2", s.Count())
	}
}
