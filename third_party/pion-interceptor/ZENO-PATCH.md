# pion/interceptor v0.1.51 with a Zeno patch

Copy of github.com/pion/interceptor v0.1.51 (MIT, see LICENSE), tests and examples removed.
Changed only `pkg/gcc/rate_controller.go` (lines marked `Zeno:`).

Problem: with a static screen the encoder sends far less than the estimate.
Any delay overuse then cut the estimate to 0.85 x encoder output, and the
additive increase capped it at 1.5 x encoder output, so the estimate stuck at
the minimum and the stream dropped to 720p for tens of seconds.

Fix: while the received rate is below half of the target
(application-limited), the estimate is held instead of increased, a decrease
backs off from the current estimate at most once per RTT + 100 ms, and the
encoder's low rate is not stored as the link capacity.
