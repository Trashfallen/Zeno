// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"math"
	"sync"
	"time"

	"github.com/pion/interceptor/internal/cc"
	"github.com/pion/logging"
)

const (
	// constants from
	// https://datatracker.ietf.org/doc/html/draft-ietf-rmcat-gcc-02#section-6

	// Zeno: 0.02 and 0.1 in the draft. NACK repairs a few percent of loss, and
	// on Wi-Fi such loss is not congestion: hold between 5 and 15 percent
	// (see lossThresholds).
	increaseLossThreshold = 0.02
	increaseTimeThreshold = 200 * time.Millisecond
	increaseFactor        = 1.05

	decreaseLossThreshold = 0.1
	decreaseTimeThreshold = 200 * time.Millisecond
)

// LossStats contains internal statistics of the loss based controller.
type LossStats struct {
	TargetBitrate int
	AverageLoss   float64
}

type lossBasedBandwidthEstimator struct {
	lock           sync.Mutex
	maxBitrate     int
	minBitrate     int
	bitrate        int
	averageLoss    float64
	lastLossUpdate time.Time
	lastIncrease   time.Time
	lastDecrease   time.Time
	log            logging.LeveledLogger
}

func newLossBasedBWE(initialBitrate int, loggerFactory logging.LoggerFactory) *lossBasedBandwidthEstimator {
	return &lossBasedBandwidthEstimator{
		lock:           sync.Mutex{},
		maxBitrate:     100_000_000, // 100 mbit
		minBitrate:     100_000,     // 100 kbit
		bitrate:        initialBitrate,
		averageLoss:    0,
		lastLossUpdate: time.Time{},
		lastIncrease:   time.Time{},
		lastDecrease:   time.Time{},
		log:            loggerFactory.NewLogger("gcc_loss_controller"),
	}
}

func (e *lossBasedBandwidthEstimator) getEstimate(wantedRate int) LossStats {
	e.lock.Lock()
	defer e.lock.Unlock()

	if e.bitrate <= 0 {
		e.bitrate = clampInt(wantedRate, e.minBitrate, e.maxBitrate)
	}
	// Zeno: without loss the loss controller must not hold the delay
	// controller back (it climbed 5% per 200 ms after every delay cut and
	// slowed the fast recovery).
	if inc, _ := lossThresholds(); zenoTuned && e.averageLoss < inc {
		e.bitrate = clampInt(wantedRate, e.minBitrate, e.maxBitrate)
	}
	e.bitrate = min(wantedRate, e.bitrate)

	return LossStats{
		TargetBitrate: e.bitrate,
		AverageLoss:   e.averageLoss,
	}
}

func (e *lossBasedBandwidthEstimator) updateLossEstimate(results []cc.Acknowledgment) {
	if len(results) == 0 {
		return
	}

	packetsLost := 0
	for _, p := range results {
		if p.Arrival.IsZero() {
			packetsLost++
		}
	}

	e.lock.Lock()
	defer e.lock.Unlock()

	lossRatio := float64(packetsLost) / float64(len(results))
	e.averageLoss = e.average(time.Since(e.lastLossUpdate), e.averageLoss, lossRatio)
	e.lastLossUpdate = time.Now()

	increaseLoss := math.Max(e.averageLoss, lossRatio)
	decreaseLoss := math.Min(e.averageLoss, lossRatio)

	incThreshold, decThreshold := lossThresholds()
	if increaseLoss < incThreshold && time.Since(e.lastIncrease) > increaseTimeThreshold {
		e.log.Infof(
			"loss controller increasing; averageLoss: %v, decreaseLoss: %v, increaseLoss: %v",
			e.averageLoss, decreaseLoss, increaseLoss,
		)
		e.lastIncrease = time.Now()
		e.bitrate = clampInt(int(increaseFactor*float64(e.bitrate)), e.minBitrate, e.maxBitrate)
	} else if decreaseLoss > decThreshold && time.Since(e.lastDecrease) > decreaseTimeThreshold {
		e.log.Infof(
			"loss controller decreasing; averageLoss: %v, decreaseLoss: %v, increaseLoss: %v",
			e.averageLoss, decreaseLoss, increaseLoss,
		)
		e.lastDecrease = time.Now()
		e.bitrate = clampInt(int(float64(e.bitrate)*(1-0.5*decreaseLoss)), e.minBitrate, e.maxBitrate)
	}
}

// lossThresholds (Zeno): loss below inc lets the rate grow, above dec cuts it.
func lossThresholds() (inc, dec float64) {
	if zenoTuned {
		return 0.05, 0.15
	}

	return increaseLossThreshold, decreaseLossThreshold
}

// loss (Zeno): current average loss.
func (e *lossBasedBandwidthEstimator) loss() float64 {
	e.lock.Lock()
	defer e.lock.Unlock()

	return e.averageLoss
}

func (e *lossBasedBandwidthEstimator) average(delta time.Duration, prev, sample float64) float64 {
	return sample + math.Exp(-float64(delta.Milliseconds())/200.0)*(prev-sample)
}
