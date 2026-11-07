package gcc

import (
	"testing"
	"time"
)

// Zeno: static screen - encoder sends 200 kbit/s against an 8 Mbit/s estimate,
// delay jitter gives overuse now and then. The estimate must not sink to the minimum.
func TestZenoAppLimitedKeepsEstimate(t *testing.T) {
	t0 := time.Unix(0, 0)
	clock := func() time.Time { return t0 }
	c := newRateController(clock, 8_000_000, 800_000, 25_000_000, func(DelayStats) {})
	c.updateRTT(20 * time.Millisecond)
	c.onReceivedRate(200_000)
	feed := func(u usage) {
		t0 = t0.Add(100 * time.Millisecond)
		c.onDelayStats(DelayStats{Usage: u})
	}
	feed(usageNormal) // init
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

// Real congestion at full rate still cuts the estimate to 0.85 x received.
func TestZenoBusyOveruseDecreases(t *testing.T) {
	t0 := time.Unix(0, 0)
	clock := func() time.Time { return t0 }
	c := newRateController(clock, 8_000_000, 800_000, 25_000_000, func(DelayStats) {})
	c.onReceivedRate(6_000_000)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	t0 = t0.Add(100 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageOver})
	if c.target != 5_100_000 {
		t.Fatalf("target %d, want 5100000", c.target)
	}
}
