package gcc

// SetZenoTuned switches Zeno's changes to GCC on or off: soft decreases,
// one decrease per response time, fast recovery, 100 ms overuse time and the
// 5/15 percent loss thresholds. Off means stock pion behaviour. For
// comparison tests only; call it while no estimator is running.
func SetZenoTuned(on bool) { zenoTuned = on }
