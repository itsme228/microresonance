package signal

import platsignal "server/internal/platform/signal"

type BandpassFilter = platsignal.BandpassFilter
type FFTScratch = platsignal.FFTScratch
type DownSampleScratch = platsignal.DownSampleScratch
type RawDetectionScratch = platsignal.RawDetectionScratch

var ComputeFFTScanReuse = platsignal.ComputeFFTScanReuse
var DownSampleScanReuse = platsignal.DownSampleScanReuse
var NewBandpassFilter = platsignal.NewBandpassFilter
