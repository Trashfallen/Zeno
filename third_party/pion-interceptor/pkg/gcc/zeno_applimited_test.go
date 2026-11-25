package gcc

import (
	"testing"
	"time"
)

// zenoClock - a rate controller on a test clock.
func zenoClock(start, minRate, maxRate int) (*rateController, *time.Time) {
	t0 := time.Unix(1000, 0)
	clock := &t0
	c := newRateController(func() time.Time { return *clock }, start, minRate, maxRate, func(DelayStats) {})

	return c, clock
}

// Zeno: static screen - encoder sends 200 kbit/s against an 8 Mbit/s estimate,
// delay jitter gives overuse now and then. The estimate must not sink to the minimum.
func TestZenoAppLimitedKeepsEstimate(t *testing.T) {
	c, t0 := zenoClock(8_000_000, 800_000, 25_000_000)
	c.updateRTT(20 * time.Millisecond)
	c.onReceivedRate(200_000)
	feed := func(u usage) {
		*t0 = t0.Add(100 * time.Millisecond)
		c.onDelayStats(DelayStats{Usage: u})
	}
	feed(usageNormal)          // init
	for i := 0; i < 300; i++ { // 30 s: overuse burst every 2 s
		if i%20 < 3 {
			feed(usageOver)
		} else {
			feed(usageNormal)
		}
	}
	if c.target < 4_000_000 {
		t.Fatalf("estimate sank to %d while app-limited", c.target)
	}
	if c.target > 8_000_000 {
		t.Fatalf("estimate grew to %d without proof", c.target)
	}
}

// Static screen on a lossy path: loss proves the path got worse, the estimate goes down.
func TestZenoAppLimitedLossDecreases(t *testing.T) {
	c, t0 := zenoClock(8_000_000, 800_000, 25_000_000)
	c.onReceivedRate(200_000)
	c.updateLoss(0.05)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	*t0 = t0.Add(time.Second)
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 6_800_000 {
		t.Fatalf("target %d, want 6800000", c.target)
	}
}

// Overuse at full rate that nothing confirms (no loss, RTT at its base) is
// jitter: cut softly to 0.95 x received.
func TestZenoSoftDecrease(t *testing.T) {
	c, t0 := zenoClock(8_000_000, 800_000, 25_000_000)
	c.updateRTT(20 * time.Millisecond)
	c.onReceivedRate(6_000_000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	*t0 = t0.Add(100 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 5_700_000 || c.lastCut != "delay" {
		t.Fatalf("target %d (%s), want 5700000 (delay)", c.target, c.lastCut)
	}
}

// Real congestion - loss or a grown RTT - still cuts to 0.85 x received.
func TestZenoConfirmedDecrease(t *testing.T) {
	var clk *time.Time
	for _, tc := range []struct {
		name string
		prep func(c *rateController)
		why  string
	}{
		{"loss", func(c *rateController) { c.updateLoss(0.03) }, "delay+loss"},
		{"rtt", func(c *rateController) { // RTT grown for 400 ms - a queue, not a stall
			c.updateRTT(80 * time.Millisecond)
			*clk = clk.Add(400 * time.Millisecond)
			c.updateRTT(80 * time.Millisecond)
		}, "delay+rtt"},
	} {
		c, t0 := zenoClock(8_000_000, 800_000, 25_000_000)
		clk = t0
		c.updateRTT(20 * time.Millisecond)
		c.onReceivedRate(6_000_000)
		c.onDelayStats(DelayStats{Usage: usageNormal})
		*t0 = t0.Add(100 * time.Millisecond)
		tc.prep(c)
		c.onDelayStats(DelayStats{Usage: usageOver})
		if c.target != 5_100_000 || c.lastCut != tc.why {
			t.Fatalf("%s: target %d (%s), want 5100000 (%s)", tc.name, c.target, c.lastCut, tc.why)
		}
	}
}

// A run of overuse samples cuts once per response time, not on every sample.
func TestZenoOneCutPerResponseTime(t *testing.T) {
	c, t0 := zenoClock(8_000_000, 800_000, 25_000_000)
	c.updateRTT(20 * time.Millisecond)
	c.onReceivedRate(6_000_000)
	c.updateLoss(0.03)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	for i := 0; i < 20; i++ { // 200 ms of overuse, a sample every 10 ms
		*t0 = t0.Add(10 * time.Millisecond)
		c.onDelayStats(DelayStats{Usage: usageOver})
	}
	if c.target != 5_100_000 {
		t.Fatalf("target %d after 200 ms of overuse, want one cut to 5100000", c.target)
	}
}

// After a cut and a calm spell the estimate jumps back near the rate the
// link carried, a failed jump keeps the rate and doubles the wait.
func TestZenoFastRecovery(t *testing.T) {
	c, t0 := zenoClock(12_000_000, 800_000, 25_000_000)
	c.updateRTT(20 * time.Millisecond)
	step := func(d time.Duration, u usage) {
		*t0 = t0.Add(d)
		c.onDelayStats(DelayStats{Usage: u})
	}
	c.onReceivedRate(12_000_000)
	step(0, usageNormal)
	for i := 0; i < 30; i++ { // 3 s calm at 12 Mbit/s - remembered as good
		step(100*time.Millisecond, usageNormal)
	}
	if c.goodRate != 12_000_000 {
		t.Fatalf("good rate %d, want 12000000", c.goodRate)
	}
	c.updateLoss(0.03)
	step(100*time.Millisecond, usageOver) // congestion: 0.85 x 12 = 10.2
	c.onReceivedRate(6_000_000)
	step(500*time.Millisecond, usageOver) // and again: 0.85 x 6 = 5.1
	if c.target != 5_100_000 {
		t.Fatalf("target %d after cuts, want 5100000", c.target)
	}
	c.updateLoss(0)
	c.onReceivedRate(4_200_000)
	jumped := false
	for i := 0; i < 25 && !jumped; i++ { // calm: jump to 0.85 x 12 = 10.2 within ~2 s
		step(100*time.Millisecond, usageNormal)
		jumped = c.target == 10_200_000
	}
	if !jumped {
		t.Fatalf("no fast recovery, target %d", c.target)
	}
	// the jump was too much: overuse right after it
	c.onReceivedRate(9_000_000)
	c.updateLoss(0.03)
	step(500*time.Millisecond, usageOver)
	if c.recoverFails != 1 || c.goodRate != 12_000_000 {
		t.Fatalf("failed jump: fails %d, good %d", c.recoverFails, c.goodRate)
	}
}

// With the tuning off the controller behaves like pion: a hard cut on every overuse sample.
func TestZenoTuningOff(t *testing.T) {
	zenoTuned = false
	defer func() { zenoTuned = true }()
	c, t0 := zenoClock(8_000_000, 800_000, 25_000_000)
	c.onReceivedRate(6_000_000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	*t0 = t0.Add(100 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 5_100_000 {
		t.Fatalf("target %d, want 5100000", c.target)
	}
}

// A short stall (RTT up for 150 ms) is not confirmed congestion: soft cut.
func TestZenoShortRTTStallIsSoft(t *testing.T) {
	c, t0 := zenoClock(8_000_000, 800_000, 25_000_000)
	c.updateRTT(20 * time.Millisecond)
	c.onReceivedRate(6_000_000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	c.updateRTT(80 * time.Millisecond)
	*t0 = t0.Add(150 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 5_700_000 {
		t.Fatalf("target %d, want soft 5700000", c.target)
	}
}

// The link got narrow: little arrives, but the RTT grew - that is the link,
// not an idle encoder, and the estimate must go down at once.
func TestZenoNarrowLinkIsNotAppLimited(t *testing.T) {
	c, t0 := zenoClock(16_000_000, 800_000, 25_000_000)
	c.updateRTT(30 * time.Millisecond)
	c.onReceivedRate(5_000_000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	c.updateRTT(400 * time.Millisecond)
	*t0 = t0.Add(time.Second)
	c.updateRTT(400 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 4_250_000 {
		t.Fatalf("target %d, want 0.85 x 5 Mbit/s", c.target)
	}
}

// Fast recovery at most doubles per jump.
func TestZenoRecoveryAtMostDoubles(t *testing.T) {
	c, t0 := zenoClock(16_000_000, 800_000, 25_000_000)
	c.updateRTT(20 * time.Millisecond)
	c.onReceivedRate(16_000_000)
	step := func(d time.Duration, u usage) {
		*t0 = t0.Add(d)
		c.onDelayStats(DelayStats{Usage: u})
	}
	step(0, usageNormal)
	for i := 0; i < 30; i++ {
		step(100*time.Millisecond, usageNormal)
	}
	c.target = 2_000_000
	c.onReceivedRate(2_000_000)
	c.lastDecrease = *t0
	for i := 0; i < 25; i++ {
		step(100*time.Millisecond, usageNormal)
	}
	if c.target > 5_000_000 || c.target < 4_000_000 { // 2 -> 4 (not 13.6), then 8%/s
		t.Fatalf("target %d after one jump from 2 Mbit/s, want about 4", c.target)
	}
}

// One unconfirmed overuse episode (a stall) gets one soft cut however long the
// detector reports it; the next episode gets its own.
func TestZenoOneSoftCutPerEpisode(t *testing.T) {
	c, t0 := zenoClock(8_000_000, 800_000, 25_000_000)
	c.updateRTT(20 * time.Millisecond)
	c.onReceivedRate(6_000_000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	for i := 0; i < 10; i++ { // 1 s of overuse, RTT at base
		*t0 = t0.Add(100 * time.Millisecond)
		c.onDelayStats(DelayStats{Usage: usageOver})
	}
	if c.target != 5_700_000 {
		t.Fatalf("target %d after one episode, want one soft cut 5700000", c.target)
	}
	c.onReceivedRate(5_700_000) // the encoder followed the cut
	*t0 = t0.Add(100 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	*t0 = t0.Add(100 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target >= 5_700_000 {
		t.Fatalf("second episode did not cut: %d", c.target)
	}
}
