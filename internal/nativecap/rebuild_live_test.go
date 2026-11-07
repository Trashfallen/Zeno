package nativecap

import (
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type procMem struct {
	cb                       uint32
	pageFaults               uint32
	peakWS, ws               uintptr
	qpPeak, qp, qnpPeak, qnp uintptr
	pagefile, peakPagefile   uintptr
	private                  uintptr
}

var (
	psapi                = windows.NewLazySystemDLL("psapi.dll")
	pGetProcessMemInfo   = psapi.NewProc("GetProcessMemoryInfo")
	pGetProcessHandleCnt = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")
)

func procUsage() (privMB float64, handles uint32) {
	h := windows.CurrentProcess()
	var m procMem
	m.cb = uint32(unsafe.Sizeof(m))
	pGetProcessMemInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&m)), uintptr(m.cb))
	pGetProcessHandleCnt.Call(uintptr(h), uintptr(unsafe.Pointer(&handles)))
	return float64(m.private) / (1 << 20), handles
}

// TestLiveRebuild: ступени разрешения и смена качества пересоздают кодер и
// видеопроцессор. Гоняем пересоздание сотни раз и смотрим, что трансляция не
// падает, а память и дескрипторы не растут.
// ZENO_CAP_TEST=1 [ZENO_CAP_REBUILDS=300] go test -run LiveRebuild -v ./internal/nativecap
func TestLiveRebuild(t *testing.T) {
	if os.Getenv("ZENO_CAP_TEST") == "" {
		t.Skip("ZENO_CAP_TEST не задан")
	}
	n := 300
	if v, err := strconv.Atoi(os.Getenv("ZENO_CAP_REBUILDS")); err == nil {
		n = v
	}
	Logf = t.Logf
	var frames atomic.Int64
	s, err := Start(Options{Monitor: monitorAt(0, 0), W: 2560, H: 1440, FPS: 60, Kbps: 16000}, func(Frame) { frames.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	time.Sleep(time.Second)
	m0, h0 := procUsage()
	st0 := s.Stats()
	t.Logf("старт: %s %dx%d, память %.0f МБ, дескрипторов %d", st0.Encoder, st0.W, st0.H, m0, h0)
	sizes := [][2]int{{2560, 1440}, {2304, 1296}, {2048, 1152}, {1792, 1008}, {1536, 864}, {1280, 720}}
	for i := 0; i < n; i++ {
		sz := sizes[i%len(sizes)]
		before := frames.Load()
		s.Resize(sz[0], sz[1])
		deadline := time.Now().Add(3 * time.Second)
		for frames.Load() < before+3 && time.Now().Before(deadline) {
			select {
			case <-s.Done():
				t.Fatalf("захват закончился сам на пересоздании %d (%dx%d)", i, sz[0], sz[1])
			default:
			}
			time.Sleep(5 * time.Millisecond)
		}
		if frames.Load() < before+3 {
			t.Fatalf("после пересоздания %d (%dx%d) кадров нет 3 с", i, sz[0], sz[1])
		}
		if i%50 == 49 {
			m, h := procUsage()
			t.Logf("пересозданий %d: память %.0f МБ, дескрипторов %d, кадров %d", i+1, m, h, frames.Load())
		}
	}
	m1, h1 := procUsage()
	t.Logf("итог: память %.0f -> %.0f МБ, дескрипторов %d -> %d", m0, m1, h0, h1)
}
