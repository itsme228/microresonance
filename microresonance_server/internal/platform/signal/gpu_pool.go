// gpu_pool.go — переиспользуемые буферы для GPU-режима (FFT, downSample, raw detection).
// Уменьшает аллокации и утечки памяти при частых вызовах из Update/Draw.

package signal

import (
	"math"

	"github.com/mjibson/go-dsp/fft"
)

const (
	maxFFTSize     = 4096
	maxDownSampleW = 500
	rawDetectBins  = 4096
)

// FFTScratch — переиспользуемые буферы для FFT (окно, магнитуды, частоты).
type FFTScratch struct {
	s     []float64
	mag   []float64
	freqs []float64
}

func (f *FFTScratch) ensure(n int) {
	half := n/2 + 1
	if cap(f.s) < n {
		f.s = make([]float64, n)
	} else {
		f.s = f.s[:n]
	}
	if cap(f.mag) < half {
		f.mag = make([]float64, half)
	} else {
		f.mag = f.mag[:half]
	}
	if cap(f.freqs) < half {
		f.freqs = make([]float64, half)
	} else {
		f.freqs = f.freqs[:half]
	}
}

// ComputeFFTScanReuse выполняет FFT с переиспользованием буферов scratch.
// Возвращает mag и freqs — слайсы из scratch; действительны до следующего вызова ComputeFFTScanReuse с тем же scratch.
func ComputeFFTScanReuse(scratch *FFTScratch, samples []float64, n int, fs float64) (mag, freqs []float64) {
	if n < 16 || fs <= 0 || scratch == nil {
		return nil, nil
	}
	scratch.ensure(n)
	s := scratch.s
	if len(samples) >= n {
		copy(s, samples[len(samples)-n:])
	} else {
		for i := range s {
			s[i] = 0
		}
		copy(s[n-len(samples):], samples)
	}
	if n > 1 {
		for i := 0; i < n; i++ {
			s[i] *= 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(n-1)))
		}
	}
	c := fft.FFTReal(s)
	half := n/2 + 1
	mag = scratch.mag[:half]
	freqs = scratch.freqs[:half]
	winAmp := 2.0
	for i := 0; i < half; i++ {
		r, im := real(c[i]), imag(c[i])
		mag[i] = math.Sqrt(r*r+im*im) / float64(n) * winAmp
		freqs[i] = float64(i) * fs / float64(n)
	}
	return mag, freqs
}

// DownSampleScratch — буферы для downSample по 4 каналам (X, Y, Z, ADS).
type DownSampleScratch struct {
	out [4][]float64
}

func (d *DownSampleScratch) ensure(channel int, w int) {
	if channel < 0 || channel >= len(d.out) {
		return
	}
	if cap(d.out[channel]) < w {
		d.out[channel] = make([]float64, w)
	} else {
		d.out[channel] = d.out[channel][:w]
	}
}

// DownSampleScanReuse возвращает downSample в переиспользуемый буфер scratch для канала channel (0..3).
// Результат действителен до следующего вызова DownSampleScanReuse с тем же scratch и каналом.
func DownSampleScanReuse(scratch *DownSampleScratch, channel int, d []float64, w int) []float64 {
	if scratch == nil || w <= 1 {
		return d
	}
	if len(d) <= w {
		return d
	}
	if channel < 0 {
		channel = 0
	}
	if channel >= len(scratch.out) {
		channel = len(scratch.out) - 1
	}
	scratch.ensure(channel, w)
	out := scratch.out[channel]
	step := float64(len(d)-1) / float64(w-1)
	for i := 0; i < w; i++ {
		idx := int(step * float64(i))
		if idx >= len(d) {
			idx = len(d) - 1
		}
		out[i] = d[idx]
	}
	return out
}

// RawDetectionScratch — буфер для медианы в updateLastRawDetection (избегаем make каждый тик).
type RawDetectionScratch struct {
	sliceMag []float64
}

func (r *RawDetectionScratch) Ensure(size int) {
	if cap(r.sliceMag) < size {
		r.sliceMag = make([]float64, size)
	}
	r.sliceMag = r.sliceMag[:size]
}

func (r *RawDetectionScratch) SliceMag() []float64 {
	return r.sliceMag
}
