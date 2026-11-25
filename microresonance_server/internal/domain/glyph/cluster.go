// ShapeClusterStore: кандидаты по форме, кластеризация по cosine distance.
// Не по частоте.

package glyph

import (
	"sync"
)

// ShapeCandidate — сегмент с представлением формы (кандидат токена).
// Discovery C: Score = max NCC, HResid = residual entropy.
// BandIdx: полоса частот (B0..B7 контент); -1 = legacy/не задано.
type ShapeCandidate struct {
	ID          int
	Shape       ShapeVector
	StartSample int
	EndSample   int
	StartTs     int64
	LastSeenTs  int64
	// Discovery C: score (NCC 0..1), H (residual entropy 0..1)
	Score     float64
	HResid    float64
	GlyphData []float64 // envelope min/max 64 pts для отрисовки
	BandIdx   int       // полоса: 0..5 контент, -1 неизвестно
}

// ShapeClusterStore — хранилище кандидатов-форм и кластеров, потокобезопасное.
type ShapeClusterStore struct {
	mu            sync.Mutex
	candidates    []ShapeCandidate
	clusters      [][]int // индексы в candidates по кластерам
	nextID        int
	distThr       float64
	maxCandidates int
}

// NewShapeClusterStore создаёт хранилище с порогом расстояния для кластеризации.
func NewShapeClusterStore(distThr float64) *ShapeClusterStore {
	if distThr <= 0 {
		distThr = 0.2
	}
	return &ShapeClusterStore{
		candidates:    make([]ShapeCandidate, 0, 16),
		clusters:      nil,
		nextID:        1,
		distThr:       distThr,
		maxCandidates: 12,
	}
}

// AddSegment добавляет сегмент (shape) и обновляет кластеры. BandIdx = -1.
func (s *ShapeClusterStore) AddSegment(shape ShapeVector, startSample, endSample int, ts int64) {
	if len(shape.Points) == 0 {
		return
	}
	s.addCandidate(ShapeCandidate{
		Shape:       shape,
		StartSample: startSample,
		EndSample:   endSample,
		StartTs:     ts,
		LastSeenTs:  ts,
		BandIdx:     -1,
	})
}

// AddDiscoveryCandidate добавляет кандидат из Discovery C (best repeating segment).
// bandIdx — полоса пика (B0..B7); кластеризация выполняется отдельно по каждой полосе.
func (s *ShapeClusterStore) AddDiscoveryCandidate(shape ShapeVector, glyphData []float64, score, hResid float64, ts int64, bandIdx int) {
	if len(shape.Points) == 0 {
		return
	}
	// Копируем GlyphData, чтобы не переиспользовать слайс из result
	gd := make([]float64, len(glyphData))
	copy(gd, glyphData)
	s.addCandidate(ShapeCandidate{
		Shape:      shape,
		StartTs:    ts,
		LastSeenTs: ts,
		Score:      score,
		HResid:     hResid,
		GlyphData:  gd,
		BandIdx:    bandIdx,
	})
}

func (s *ShapeClusterStore) addCandidate(c ShapeCandidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := c.LastSeenTs - 3000
	newCand := make([]ShapeCandidate, 0, len(s.candidates)+1)
	for _, x := range s.candidates {
		if x.LastSeenTs >= cutoff {
			newCand = append(newCand, x)
		}
	}
	if len(newCand) >= s.maxCandidates {
		oldest := 0
		for i := 1; i < len(newCand); i++ {
			if newCand[i].LastSeenTs < newCand[oldest].LastSeenTs {
				oldest = i
			}
		}
		newCand[oldest] = newCand[len(newCand)-1]
		newCand = newCand[:len(newCand)-1]
	}
	c.ID = s.nextID
	s.nextID++
	newCand = append(newCand, c)
	s.candidates = newCand
	s.clusters = ClusterShapesByBand(newCand, s.distThr)
}

// shapeSlice возвращает срез ShapeVector из кандидатов.
func shapeSlice(cands []ShapeCandidate) []ShapeVector {
	out := make([]ShapeVector, len(cands))
	for i := range cands {
		out[i] = cands[i].Shape
	}
	return out
}

// ClusterShapes группирует формы по cosine distance < distThr (все в одном пуле).
func ClusterShapes(shapes []ShapeVector, distThr float64) [][]int {
	if len(shapes) == 0 {
		return nil
	}
	if distThr <= 0 {
		distThr = 0.2
	}
	clusters := [][]int{{0}}
	for i := 1; i < len(shapes); i++ {
		bestIdx := -1
		bestD := distThr + 1
		for ci, c := range clusters {
			for _, j := range c {
				d := ShapeDistance(shapes[i], shapes[j])
				if d < bestD {
					bestD = d
					bestIdx = ci
				}
			}
		}
		if bestIdx >= 0 && bestD < distThr {
			clusters[bestIdx] = append(clusters[bestIdx], i)
		} else {
			clusters = append(clusters, []int{i})
		}
	}
	return clusters
}

// ClusterShapesByBand группирует формы по cosine distance < distThr только внутри одной полосы (BandIdx).
// Кандидаты из разных полос не объединяются в один кластер.
func ClusterShapesByBand(candidates []ShapeCandidate, distThr float64) [][]int {
	if len(candidates) == 0 {
		return nil
	}
	if distThr <= 0 {
		distThr = 0.2
	}
	clusters := [][]int{{0}}
	for i := 1; i < len(candidates); i++ {
		bandI := candidates[i].BandIdx
		bestIdx := -1
		bestD := distThr + 1
		for ci, c := range clusters {
			// Кластер принадлежит полосе первого элемента
			if len(c) == 0 {
				continue
			}
			if candidates[c[0]].BandIdx != bandI {
				continue
			}
			for _, j := range c {
				d := ShapeDistance(candidates[i].Shape, candidates[j].Shape)
				if d < bestD {
					bestD = d
					bestIdx = ci
				}
			}
		}
		if bestIdx >= 0 && bestD < distThr {
			clusters[bestIdx] = append(clusters[bestIdx], i)
		} else {
			clusters = append(clusters, []int{i})
		}
	}
	return clusters
}

// ListClusters возвращает кластеры (индексы кандидатов) и самих кандидатов.
func (s *ShapeClusterStore) ListClusters() (clusters [][]int, candidates []ShapeCandidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clusters == nil {
		return nil, append([]ShapeCandidate(nil), s.candidates...)
	}
	cl := make([][]int, len(s.clusters))
	for i := range s.clusters {
		cl[i] = append([]int(nil), s.clusters[i]...)
	}
	return cl, append([]ShapeCandidate(nil), s.candidates...)
}

// CentroidShape возвращает усреднённую форму по индексам кандидатов (для отображения кластера).
func CentroidShape(candidates []ShapeCandidate, indices []int) ShapeVector {
	if len(indices) == 0 {
		return ShapeVector{}
	}
	n := len(candidates[indices[0]].Shape.Points)
	pts := make([]float64, n)
	for _, idx := range indices {
		if idx >= len(candidates) {
			continue
		}
		p := candidates[idx].Shape.Points
		if len(p) != n {
			continue
		}
		for i := range pts {
			pts[i] += p[i]
		}
	}
	for i := range pts {
		pts[i] /= float64(len(indices))
	}
	return ShapeVector{Points: pts}
}

// SetDistThr обновляет порог расстояния.
func (s *ShapeClusterStore) SetDistThr(thr float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.distThr = thr
}
