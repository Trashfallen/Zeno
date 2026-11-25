// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"math"
	"sync"
	"time"
)

const (
	decreaseEMAAlpha = 0.95
	beta             = 0.85

	// Zeno: the sender is application-limited when the receiver gets less than
	// this share of the target (static screen, the encoder has nothing to send).
	appLimitedRatio = 0.5

	// Zeno: a delay overuse that neither loss nor a grown RTT confirms (Wi-Fi
	// jitter, a short stall on the path) cuts the estimate softly.
	softBeta = 0.95
	// Zeno: loss share that confirms congestion.
	confirmLoss = 0.02
	// Zeno: one decrease per response time; pion cut on every delay sample
	// while the queue drained and the estimate spiralled down.
	decreaseGap = 200 * time.Millisecond
	// Zeno: fast recovery - after a calm spell the estimate jumps back to this
	// share of the best rate the link carried in the last goodRateTTL.
	recoverShare   = 0.85
	recoverCalm    = 2 * time.Second
	goodRateTTL    = 60 * time.Second
	recoverFailWin = 3 * time.Second
	rttWindow      = 10 // seconds of per-second RTT minima for the base RTT
	// Zeno: RTT must stay grown this long to confirm congestion - a Wi-Fi
	// stall raises it for a moment and then the queued packets arrive at once.
	rttConfirmTime = 300 * time.Millisecond
)

// Zeno: soft decreases and fast recovery can be switched off for comparison tests.
var zenoTuned = true

type rateController struct {
	now                  now
	initialTargetBitrate int
	minBitrate           int
	maxBitrate           int

	dsWriter func(DelayStats)

	lock               sync.Mutex
	init               bool
	delayStats         DelayStats
	target             int
	lastUpdate         time.Time
	lastState          state
	latestRTT          time.Duration
	latestReceivedRate int
	latestDecreaseRate *exponentialMovingAverage

	// Zeno
	latestLoss   float64
	rttMins      [rttWindow]time.Duration
	rttSlot      int64
	rttHighSince time.Time
	softCutDone  bool // this overuse episode already got its soft cut
	lastDecrease time.Time
	calmSince    time.Time
	goodRate     int
	goodAt       time.Time
	recoveredAt  time.Time
	recoverFails int
	lastCut      string // why the last decrease happened: "delay", "delay+loss", "delay+rtt"
	recoveries   int
}

type exponentialMovingAverage struct {
	average      float64
	variance     float64
	stdDeviation float64
}

func (a *exponentialMovingAverage) update(value float64) {
	if a.average == 0.0 {
		a.average = value
	} else {
		x := value - a.average
		a.average += decreaseEMAAlpha * x
		a.variance = (1 - decreaseEMAAlpha) * (a.variance + decreaseEMAAlpha*x*x)
		a.stdDeviation = math.Sqrt(a.variance)
	}
}

func newRateController(
	now now, initialTargetBitrate, minBitrate, maxBitrate int, dsw func(DelayStats),
) *rateController {
	return &rateController{
		now:                  now,
		initialTargetBitrate: initialTargetBitrate,
		minBitrate:           minBitrate,
		maxBitrate:           maxBitrate,
		dsWriter:             dsw,
		init:                 false,
		delayStats:           DelayStats{},
		target:               initialTargetBitrate,
		lastUpdate:           time.Time{},
		lastState:            stateIncrease,
		latestRTT:            0,
		latestReceivedRate:   0,
		latestDecreaseRate:   &exponentialMovingAverage{},
	}
}

func (c *rateController) onReceivedRate(rate int) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.latestReceivedRate = rate
}

func (c *rateController) updateRTT(rtt time.Duration) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.latestRTT = rtt
	// Zeno: per-second minima for the base RTT (queue-free path delay)
	slot := c.now().Unix()
	if slot != c.rttSlot {
		for s := c.rttSlot + 1; s <= slot && s-c.rttSlot <= rttWindow; s++ {
			c.rttMins[s%rttWindow] = 0
		}
		c.rttSlot = slot
	}
	i := slot % rttWindow
	if c.rttMins[i] == 0 || rtt < c.rttMins[i] {
		c.rttMins[i] = rtt
	}
	if !c.rttGrown() {
		c.rttHighSince = time.Time{}
	} else if c.rttHighSince.IsZero() {
		c.rttHighSince = c.now()
	}
}

// rttConfirmed (Zeno): the RTT has stayed grown long enough to be a queue.
func (c *rateController) rttConfirmed(now time.Time) bool {
	return c.rttGrown() && !c.rttHighSince.IsZero() && now.Sub(c.rttHighSince) >= rttConfirmTime
}

// updateLoss (Zeno): average loss from the loss controller.
func (c *rateController) updateLoss(loss float64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.latestLoss = loss
}

// rttGrown (Zeno): the path delay is well above its base - a queue is building.
func (c *rateController) rttGrown() bool {
	var base time.Duration
	for _, v := range c.rttMins {
		if v > 0 && (base == 0 || v < base) {
			base = v
		}
	}
	if base == 0 || c.latestRTT == 0 {
		return false
	}

	return c.latestRTT > base+max(25*time.Millisecond, base/2)
}

func (c *rateController) onDelayStats(ds DelayStats) {
	now := c.now()
	c.lock.Lock()
	c.trackHealth(now, ds.Usage)
	c.lock.Unlock()

	if !c.init {
		c.delayStats = ds
		c.delayStats.State = stateIncrease
		c.init = true

		return
	}
	c.delayStats = ds
	c.delayStats.State = c.delayStats.State.transition(ds.Usage)

	if c.delayStats.State == stateHold {
		return
	}

	var next DelayStats

	c.lock.Lock()

	switch c.delayStats.State {
	case stateHold:
		// should never occur due to check above, but makes the linter happy
	case stateIncrease:
		c.target = clampInt(c.increase(now), c.minBitrate, c.maxBitrate)
		next = DelayStats{
			Measurement:      c.delayStats.Measurement,
			Estimate:         c.delayStats.Estimate,
			Threshold:        c.delayStats.Threshold,
			LastReceiveDelta: c.delayStats.LastReceiveDelta,
			Usage:            c.delayStats.Usage,
			State:            c.delayStats.State,
			TargetBitrate:    c.target,
		}

	case stateDecrease:
		c.target = clampInt(c.decrease(now), c.minBitrate, c.maxBitrate)
		next = DelayStats{
			Measurement:      c.delayStats.Measurement,
			Estimate:         c.delayStats.Estimate,
			Threshold:        c.delayStats.Threshold,
			LastReceiveDelta: c.delayStats.LastReceiveDelta,
			Usage:            c.delayStats.Usage,
			State:            c.delayStats.State,
			TargetBitrate:    c.target,
		}
	}

	c.lock.Unlock()

	c.dsWriter(next)
}

// trackHealth (Zeno): remembers when the path was last troubled and the best
// rate it carried while calm - the target of the fast recovery.
func (c *rateController) trackHealth(now time.Time, use usage) {
	if c.calmSince.IsZero() {
		c.calmSince = now
	}
	if use != usageOver {
		c.softCutDone = false // the episode is over
	}
	if use == usageOver || c.latestLoss >= confirmLoss {
		c.calmSince = now

		return
	}
	if c.appLimited() || now.Sub(c.calmSince) < recoverCalm {
		return
	}
	if c.latestReceivedRate > c.goodRate || now.Sub(c.goodAt) > goodRateTTL {
		c.goodRate, c.goodAt = c.latestReceivedRate, now
	} else if c.latestReceivedRate >= int(0.9*float64(c.goodRate)) {
		c.goodAt = now // still carries it - keep the memory fresh
	}
	if c.recoverFails > 0 && now.Sub(c.recoveredAt) > 10*time.Second && now.Sub(c.lastDecrease) > 10*time.Second {
		c.recoverFails = 0
	}
}

// appLimited (Zeno): the received rate describes the encoder, not the link.
// A grown RTT means the link itself got narrow (a queue builds up) - then
// the low received rate is the link, not an idle encoder.
func (c *rateController) appLimited() bool {
	if zenoTuned && c.rttGrown() {
		return false
	}

	return c.latestReceivedRate > 0 && float64(c.latestReceivedRate) < appLimitedRatio*float64(c.target)
}

func (c *rateController) increase(now time.Time) int {
	// Zeno: nothing proves the link can carry more - hold the estimate
	// instead of growing it (or cutting it to 1.5x the encoder's output).
	if c.appLimited() {
		c.lastUpdate = now

		return c.target
	}
	// Zeno: the path has been calm for a while and the estimate is far below
	// what it carried recently - jump back instead of climbing for tens of
	// seconds. A failed jump (overuse right after it) doubles the wait.
	if zenoTuned && c.goodRate > 0 && now.Sub(c.goodAt) < goodRateTTL &&
		now.Sub(c.calmSince) >= recoverCalm && now.Sub(c.lastDecrease) >= recoverCalm &&
		now.Sub(c.recoveredAt) >= recoverCalm<<min(c.recoverFails, 4) {
		// at most double per jump: the link may still be narrow
		if jump := min(int(recoverShare*float64(c.goodRate)), 2*c.target); jump > c.target {
			c.recoveredAt, c.lastUpdate = now, now
			c.recoveries++

			return jump
		}
	}
	if c.latestDecreaseRate.average > 0 &&
		float64(c.latestReceivedRate) > c.latestDecreaseRate.average-3*c.latestDecreaseRate.stdDeviation &&
		float64(c.latestReceivedRate) < c.latestDecreaseRate.average+3*c.latestDecreaseRate.stdDeviation {
		bitsPerFrame := float64(c.target) / 30.0
		packetsPerFrame := math.Ceil(bitsPerFrame / (1200 * 8))
		expectedPacketSizeBits := bitsPerFrame / packetsPerFrame

		responseTime := 100*time.Millisecond + c.latestRTT
		alpha := 0.5 * math.Min(float64(now.Sub(c.lastUpdate).Milliseconds())/float64(responseTime.Milliseconds()), 1.0)
		increase := int(math.Max(1000.0, alpha*expectedPacketSizeBits))
		c.lastUpdate = now

		// Zeno: the 1.5x received cap must never lower the target.
		next := c.target + increase
		if capped := int(1.5 * float64(c.latestReceivedRate)); next > capped && capped > c.target {
			next = capped
		}

		return next
	}
	eta := math.Pow(1.08, math.Min(float64(now.Sub(c.lastUpdate).Milliseconds())/1000, 1.0))
	c.lastUpdate = now

	rate := int(eta * float64(c.target))

	// maximum increase to 1.5 * received rate
	received := int(1.5 * float64(c.latestReceivedRate))
	if rate > received && received > c.target {
		return received
	}

	if rate < c.target {
		return c.target
	}

	return rate
}

func (c *rateController) decrease(now time.Time) int {
	// Zeno: overuse while application-limited is not caused by our rate.
	// Back off from the current estimate at most once per response time and
	// keep the encoder's low output out of the decrease-rate average:
	// otherwise a static screen drives the estimate to the minimum and it
	// climbs back for tens of seconds.
	if c.appLimited() {
		if now.Sub(c.lastUpdate) < 100*time.Millisecond+c.latestRTT {
			return c.target
		}
		// Zeno: sending under half the estimate cannot queue the path - a
		// delay overuse then is jitter. Only loss proves the path got worse.
		if zenoTuned && c.latestLoss < confirmLoss {
			return c.target
		}
		c.lastUpdate = now

		return int(beta * float64(c.target))
	}
	if !zenoTuned {
		target := int(beta * float64(c.latestReceivedRate))
		c.latestDecreaseRate.update(float64(c.latestReceivedRate))
		c.lastUpdate = now

		return target
	}
	// Zeno: one cut per response time - the queue needs that long to react.
	if now.Sub(c.lastDecrease) < decreaseGap+c.latestRTT {
		return c.target
	}
	b, why := softBeta, "delay"
	switch {
	case c.latestLoss >= confirmLoss:
		b, why = beta, "delay+loss"
	case c.rttConfirmed(now):
		b, why = beta, "delay+rtt"
	}
	// an unconfirmed overuse (a Wi-Fi stall) gets one soft cut, not a
	// series of them while the delay estimate settles back
	if why == "delay" && c.softCutDone {
		return c.target
	}
	c.softCutDone = why == "delay"
	// a cut right after a jump: the jump was too much. The remembered rate
	// stays (the link may come back), the next jump waits twice as long.
	if now.Sub(c.recoveredAt) < max(recoverFailWin, 2*c.latestRTT+time.Second) {
		c.recoverFails++
	}
	target := min(c.target, int(b*float64(c.latestReceivedRate)))
	c.latestDecreaseRate.update(float64(c.latestReceivedRate))
	c.lastUpdate, c.lastDecrease, c.lastCut = now, now, why

	return target
}

// Zeno: values for the sender's journal.
func (c *rateController) rtt() time.Duration {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.latestRTT
}

func (c *rateController) cutReason() string {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.lastCut
}

func (c *rateController) recoveryCount() int {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.recoveries
}
