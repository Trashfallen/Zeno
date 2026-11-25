# pion/interceptor v0.1.51 with a Zeno patch

Copy of github.com/pion/interceptor v0.1.51 (MIT, see LICENSE), tests and examples removed.
Changed only `pkg/gcc` (lines marked `Zeno`): `rate_controller.go`,
`loss_based_bwe.go`, `delay_based_bwe.go`, `send_side_bwe.go`, plus
`zeno_tuning.go` and `zeno_applimited_test.go`, and
`internal/cc/feedback_adapter.go`. When updating pion, carry
these changes over and run `go test ./pkg/gcc/` here and the
`TestGCCSim` comparison in Zeno (`internal/nativecap/gccsim_test.go`).

## 1. Static screen (0.7.1)

Problem: with a static screen the encoder sends far less than the estimate.
Any delay overuse then cut the estimate to 0.85 x encoder output, and the
additive increase capped it at 1.5 x encoder output, so the estimate stuck at
the minimum and the stream dropped to 720p for tens of seconds.

Fix: while the received rate is below half of the target
(application-limited), the estimate is held instead of increased, the
encoder's low rate is not stored as the link capacity, and a delay overuse
does not cut the estimate at all unless loss (2 % and more) confirms it.

## 2. Hold quality on jittery paths (0.8.0)

Problem: on Wi-Fi and uneven routes the stream quality dropped hard and came
back slowly. Stock pion:

- counts overuse after 10 ms of delay growth, so every Wi-Fi hiccup is congestion;
- cuts to 0.85 x received on every delay sample while overuse lasts, so one
  hiccup gives a cascade of cuts;
- after a cut climbs back 8 % per second (tens of seconds to full quality);
- loss between 2 and 10 % freezes the estimate, though NACK repairs it;
- the loss controller is pulled down with every delay cut and climbs back
  5 % per 200 ms, which also slows recovery.

Fix:

- overuse time 100 ms;
- a delay overuse that neither loss (2 %+) nor a grown RTT (base + max(25 ms,
  base / 2), base = minimum over 10 s) confirms cuts to 0.95 x received;
  confirmed congestion still cuts to 0.85 x;
- at most one cut per 200 ms + RTT;
- fast recovery: the best rate the path carried while calm in the last 60 s is
  remembered; after 2 s without overuse and loss the estimate jumps to 0.85 x
  that rate. An overuse within 3 s of the jump forgets that rate and doubles
  the wait before the next jump;
- loss thresholds 5 % (grow) and 15 % (cut); below 5 % the loss controller
  follows the delay controller instead of holding it back;
- `GetStats` also returns `rtt`, `lastCut` (why the last cut happened) and
  `recoveries` for the sender's journal.

## 3. GCC blind under congestion (0.8.0)

`internal/cc/feedback_adapter.go`: the TWCC feedback adapter remembered only
the last 250 sent packets (about 150 ms at 16 Mbit/s). Once the path queue
grew past ~100 ms, feedback named packets already forgotten, GCC got no
acknowledgments at all and kept sending at full rate into the queue (found
with the vnet bench: capacity cut from 40 to 5 Mbit/s, the estimate never
moved). History is now 8192 packets. This fix is always on.

`SetZenoTuned(false)` turns off everything from 0.8.0 (part 2 and the loss
check in part 1), leaving stock pion with the 0.7.1 static-screen fix, for
comparison tests.
