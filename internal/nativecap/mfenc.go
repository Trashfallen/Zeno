package nativecap

// Аппаратный кодер H.264 через Media Foundation. Аппаратные кодеры
// асинхронные: сами говорят событиями, когда готовы принять кадр
// (METransformNeedInput) и когда есть результат (METransformHaveOutput).

import (
	"errors"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iidIMFTransform         = guid("{bf94c121-5b05-4e6f-8000-ba598961414d}")
	iidIMFMediaEventGen     = guid("{2cd0bd52-bcd5-4b89-b62c-eadc0c031e7d}")
	iidICodecAPI            = guid("{901db4c7-31ce-41a2-85dc-8fa0bf41b8da}")
	mftCategoryVideoEncoder = guid("{f79eac7d-e545-4387-bdee-d647d7bde42a}")
	mfMediaTypeVideo        = guid("{73646976-0000-0010-8000-00aa00389b71}")
	mfVideoFormatNV12       = guid("{3231564e-0000-0010-8000-00aa00389b71}")
	mfVideoFormatH264       = guid("{34363248-0000-0010-8000-00aa00389b71}")
	mfMTMajorType           = guid("{48eba18e-f8c9-4687-bf11-0a74c9f96a8f}")
	mfMTSubtype             = guid("{f7e34c9a-42e8-4714-b74b-cb29d72c35e5}")
	mfMTFrameSize           = guid("{1652c33d-d6b2-4012-b834-72030849a37d}")
	mfMTFrameRate           = guid("{c459a2e8-3d2c-4e44-b132-fee5156c7bb0}")
	mfMTPixelAspect         = guid("{c6376a1e-8d0a-4027-be45-6d9a0ad39bb6}")
	mfMTInterlaceMode       = guid("{e2724bb8-e676-4806-b4b2-a8d6efb44ccd}")
	mfMTAvgBitrate          = guid("{20332624-fb0d-4d9e-bd0d-cbf6786c102e}")
	mfMTMpeg2Profile        = guid("{ad76a80b-2d5c-4e0b-b375-64e520137036}")
	mfMTVideoNominalRange   = guid("{c21b8ee5-b956-4071-8daf-325edf5cab11}")
	mfTransformAsync        = guid("{f81a699a-649a-497d-8c73-29f8fed6ad7a}")
	mfTransformAsyncUnlock  = guid("{e5666d6b-3422-4eb6-a421-da7db1f8e207}")
	mftFriendlyName         = guid("{314ffbae-5b41-4c95-9c19-4e7d586face3}")
	mfSampleCleanPoint      = guid("{9cdf01d8-a0f0-43ba-b077-eaa06cbd728a}")
	codecRateControlMode    = guid("{1c0608e9-370c-4710-8a58-cb6181c42423}")
	codecMeanBitRate        = guid("{f7222374-2144-4815-b550-a37f8e12ee52}")
	codecLowLatency         = guid("{9c27891a-ed7a-40e1-88e8-b22727a024ee}")
	codecGOPSize            = guid("{95f31b26-95a4-41aa-9303-246a7fc6eef1}")
	codecForceKeyFrame      = guid("{398c1b98-8353-475a-9ef2-8f265d260345}")
	codecBPictureCount      = guid("{8d390aac-dc5c-4200-b57f-814d04babab2}")
	codecQualityVsSpeed     = guid("{98332df8-03cd-476b-89fa-3f9e442dec9f}")

	pMFStartup                 = mfplat.NewProc("MFStartup")
	pMFCreateDXGIDeviceManager = mfplat.NewProc("MFCreateDXGIDeviceManager")
	pMFTEnumEx                 = mfplat.NewProc("MFTEnumEx")
	pMFCreateMediaType         = mfplat.NewProc("MFCreateMediaType")
	pMFCreateSample            = mfplat.NewProc("MFCreateSample")
	pMFCreateDXGISurfaceBuf    = mfplat.NewProc("MFCreateDXGISurfaceBuffer")
	pMFCreateMemoryBuffer      = mfplat.NewProc("MFCreateMemoryBuffer")

	mfOnce sync.Once
	mfErr  error

	errNoEncoder = errors.New("аппаратный кодер H.264 не найден")
	errNoFrame   = errors.New("кадра еще нет")
)

const (
	meNeedInput   = 601
	meHaveOutput  = 602
	mfENoEvents   = 0xC00D3E80
	mfENeedMore   = 0xC00D6D72
	mfEStreamChg  = 0xC00D6D61
	msgSetD3D     = 0x2
	msgBeginStrm  = 0x10000000
	msgEndStrm    = 0x10000002
	msgStartStrm  = 0x10000003
	providesSmpls = 0x100 // MFT_OUTPUT_STREAM_PROVIDES_SAMPLES
)

type encoder struct {
	Name      string
	mft       com // IMFTransform
	events    com // IMFMediaEventGenerator (асинхронный кодер)
	codec     com // ICodecAPI
	mgr       com // IMFDXGIDeviceManager
	w, h, fps uint32
	needInput int
	async     bool
	provides  bool
	outSize   uint32
	onOutput  func(data []byte, key bool, ts int64)
}

func mfStartup() error {
	mfOnce.Do(func() {
		r, _, _ := pMFStartup.Call(0x20070, 1) // MF_VERSION, MFSTARTUP_LITE
		mfErr = hr(r)
	})
	return mfErr
}

type typeInfo struct{ Major, Sub windows.GUID }

// variant для ICodecAPI: VT_UI4 или VT_BOOL.
type variant struct {
	VT      uint16
	_, _, _ uint16
	Val     uint64
	_       uint64
}

func newEncoder(d *device, w, h, fps, kbps uint32, onOutput func([]byte, bool, int64)) (*encoder, error) {
	if err := mfStartup(); err != nil {
		return nil, err
	}
	e := &encoder{w: w, h: h, fps: fps, onOutput: onOutput}
	ok := false
	defer func() {
		if !ok {
			e.close()
		}
	}()

	var token uint32
	var mgr unsafe.Pointer
	r, _, _ := pMFCreateDXGIDeviceManager.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&mgr)))
	if err := hr(r); err != nil {
		return nil, err
	}
	e.mgr = com{mgr}
	if err := hr(e.mgr.call(7, d.dev.ptr(), uintptr(token))); err != nil { // ResetDevice
		return nil, err
	}

	in := typeInfo{mfMediaTypeVideo, mfVideoFormatNV12}
	out := typeInfo{mfMediaTypeVideo, mfVideoFormatH264}
	var arr unsafe.Pointer
	var n uint32
	const hardware, sortAndFilter = 0x4, 0x40
	r, _, _ = pMFTEnumEx.Call(uintptr(unsafe.Pointer(&mftCategoryVideoEncoder)), hardware|sortAndFilter,
		uintptr(unsafe.Pointer(&in)), uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&arr)), uintptr(unsafe.Pointer(&n)))
	if err := hr(r); err != nil {
		return nil, err
	}
	if n == 0 || arr == nil {
		return nil, errNoEncoder
	}
	acts := unsafe.Slice((*unsafe.Pointer)(arr), n)
	for _, a := range acts {
		act := com{a}
		if !e.mft.ok() {
			var t unsafe.Pointer
			if hr(act.call(33, uintptr(unsafe.Pointer(&iidIMFTransform)), uintptr(unsafe.Pointer(&t)))) == nil { // ActivateObject
				e.mft = com{t}
				e.Name = attrString(act, &mftFriendlyName)
			}
		}
		act.release()
	}
	pCoTaskMemFree.Call(uintptr(arr))
	if !e.mft.ok() {
		return nil, errNoEncoder
	}

	var attrs unsafe.Pointer
	if hr(e.mft.call(8, uintptr(unsafe.Pointer(&attrs)))) == nil && attrs != nil { // GetAttributes
		a := com{attrs}
		var isAsync uint32
		if hr(a.call(7, uintptr(unsafe.Pointer(&mfTransformAsync)), uintptr(unsafe.Pointer(&isAsync)))) == nil && isAsync != 0 {
			e.async = true
			a.call(21, uintptr(unsafe.Pointer(&mfTransformAsyncUnlock)), 1) // SetUINT32
		}
		a.release()
	}
	if e.async {
		ev, err := e.mft.qi(&iidIMFMediaEventGen)
		if err != nil {
			return nil, err
		}
		e.events = ev
	}
	if err := hr(e.mft.call(23, msgSetD3D, e.mgr.ptr())); err != nil { // ProcessMessage
		return nil, err
	}

	if c, err := e.mft.qi(&iidICodecAPI); err == nil {
		e.codec = c
		e.setCodec(&codecRateControlMode, 0) // CBR: ровный поток, без всплесков
		e.setCodec(&codecLowLatency, 1)
		e.setCodec(&codecBPictureCount, 0)
		e.setCodec(&codecGOPSize, fps*10) // ключевой кадр раз в 10 с и по запросу
		e.setCodec(&codecQualityVsSpeed, 33)
		e.setCodec(&codecMeanBitRate, kbps*1000)
	}

	// сначала выходной тип, потом входной - так требуют кодеры
	var setOut error
	for _, profile := range []uint32{100, 77, 66} { // High, Main, Baseline
		if setOut = e.setOutputType(kbps, profile); setOut == nil {
			break
		}
	}
	if setOut != nil {
		return nil, setOut
	}
	if err := e.setInputType(); err != nil {
		return nil, err
	}
	var info struct{ Flags, Size, Align uint32 }
	if hr(e.mft.call(7, 0, uintptr(unsafe.Pointer(&info)))) == nil { // GetOutputStreamInfo
		e.provides = info.Flags&providesSmpls != 0
		e.outSize = max(info.Size, w*h)
	}
	e.mft.call(23, msgBeginStrm, 0)
	e.mft.call(23, msgStartStrm, 0)
	if !e.async {
		e.needInput = 1
	}
	ok = true
	return e, nil
}

func attrString(a com, key *windows.GUID) string {
	var s *uint16
	var n uint32
	if hr(a.call(13, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&s)), uintptr(unsafe.Pointer(&n)))) != nil || s == nil { // GetAllocatedString
		return ""
	}
	defer pCoTaskMemFree.Call(uintptr(unsafe.Pointer(s)))
	return windows.UTF16ToString(unsafe.Slice(s, n))
}

func (e *encoder) setCodec(api *windows.GUID, v uint32) error {
	if !e.codec.ok() {
		return errNoEncoder
	}
	val := variant{VT: 19, Val: uint64(v)}                                                  // VT_UI4
	return hr(e.codec.call(9, uintptr(unsafe.Pointer(api)), uintptr(unsafe.Pointer(&val)))) // SetValue
}

func newMediaType() (com, error) {
	var t unsafe.Pointer
	r, _, _ := pMFCreateMediaType.Call(uintptr(unsafe.Pointer(&t)))
	if err := hr(r); err != nil {
		return com{}, err
	}
	return com{t}, nil
}

func (e *encoder) videoAttrs(t com) {
	t.call(22, uintptr(unsafe.Pointer(&mfMTFrameSize)), uintptr(e.w)<<32|uintptr(e.h)) // SetUINT64
	t.call(22, uintptr(unsafe.Pointer(&mfMTFrameRate)), uintptr(e.fps)<<32|1)          // SetUINT64
	t.call(22, uintptr(unsafe.Pointer(&mfMTPixelAspect)), 1<<32|1)                     // SetUINT64
	t.call(21, uintptr(unsafe.Pointer(&mfMTInterlaceMode)), 2)                         // MFVideoInterlace_Progressive
	t.call(21, uintptr(unsafe.Pointer(&mfMTVideoNominalRange)), 2)                     // MFNominalRange_16_235
}

func (e *encoder) setOutputType(kbps, profile uint32) error {
	t, err := newMediaType()
	if err != nil {
		return err
	}
	defer t.release()
	t.call(24, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&mfMediaTypeVideo))) // SetGUID
	t.call(24, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&mfVideoFormatH264)))
	t.call(21, uintptr(unsafe.Pointer(&mfMTAvgBitrate)), uintptr(kbps*1000))
	t.call(21, uintptr(unsafe.Pointer(&mfMTMpeg2Profile)), uintptr(profile))
	e.videoAttrs(t)
	return hr(e.mft.call(16, 0, t.ptr(), 0)) // SetOutputType
}

func (e *encoder) setInputType() error {
	// берем тип NV12 из предложенных кодером, иначе собираем сами
	for i := uintptr(0); i < 32; i++ {
		var tp unsafe.Pointer
		if hr(e.mft.call(13, 0, i, uintptr(unsafe.Pointer(&tp)))) != nil { // GetInputAvailableType
			break
		}
		t := com{tp}
		var sub windows.GUID
		t.call(10, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&sub))) // GetGUID
		if sub == mfVideoFormatNV12 {
			e.videoAttrs(t)
			err := hr(e.mft.call(15, 0, t.ptr(), 0)) // SetInputType
			t.release()
			return err
		}
		t.release()
	}
	t, err := newMediaType()
	if err != nil {
		return err
	}
	defer t.release()
	t.call(24, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&mfMediaTypeVideo)))
	t.call(24, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&mfVideoFormatNV12)))
	e.videoAttrs(t)
	return hr(e.mft.call(15, 0, t.ptr(), 0))
}

// pump разбирает события асинхронного кодера и забирает готовые кадры.
func (e *encoder) pump() error {
	if !e.async {
		return e.drain()
	}
	for {
		var ev unsafe.Pointer
		r := e.events.call(3, 1, uintptr(unsafe.Pointer(&ev))) // GetEvent(MF_EVENT_FLAG_NO_WAIT)
		if uint32(r) == mfENoEvents {
			return nil
		}
		if err := hr(r); err != nil {
			return err
		}
		evt := com{ev}
		var typ uint32
		evt.call(33, uintptr(unsafe.Pointer(&typ))) // GetType
		evt.release()
		switch typ {
		case meNeedInput:
			e.needInput++
		case meHaveOutput:
			if err := e.drain(); err != nil {
				return err
			}
		}
	}
}

// ready - кодер готов принять кадр.
func (e *encoder) ready() bool { return e.needInput > 0 }

// input отдает кодеру NV12-текстуру. ts - время в единицах 100 нс.
func (e *encoder) input(tex com, ts, dur int64) error {
	var buf unsafe.Pointer
	r, _, _ := pMFCreateDXGISurfaceBuf.Call(uintptr(unsafe.Pointer(&iidID3D11Texture2D)), tex.ptr(), 0, 0, uintptr(unsafe.Pointer(&buf)))
	if err := hr(r); err != nil {
		return err
	}
	b := com{buf}
	defer b.release()
	var maxLen uint32
	b.call(7, uintptr(unsafe.Pointer(&maxLen))) // GetMaxLength
	b.call(6, uintptr(maxLen))                  // SetCurrentLength
	var sp unsafe.Pointer
	r, _, _ = pMFCreateSample.Call(uintptr(unsafe.Pointer(&sp)))
	if err := hr(r); err != nil {
		return err
	}
	s := com{sp}
	defer s.release()
	s.call(42, b.ptr())               // AddBuffer
	s.call(36, uintptr(ts))           // SetSampleTime
	s.call(38, uintptr(dur))          // SetSampleDuration
	r = e.mft.call(24, 0, s.ptr(), 0) // ProcessInput
	if uint32(r) == 0xC00D36B5 {      // MF_E_NOTACCEPTING - подождем следующего NeedInput
		return nil
	}
	if err := hr(r); err != nil {
		return err
	}
	if e.async {
		e.needInput--
	}
	return nil
}

type outputDataBuffer struct {
	StreamID uint32
	_        uint32
	Sample   unsafe.Pointer
	Status   uint32
	_        uint32
	Events   unsafe.Pointer
}

// drain забирает из кодера все готовые кадры.
func (e *encoder) drain() error {
	for {
		ob := outputDataBuffer{}
		if !e.provides {
			var sp, bp unsafe.Pointer
			pMFCreateSample.Call(uintptr(unsafe.Pointer(&sp)))
			pMFCreateMemoryBuffer.Call(uintptr(e.outSize), uintptr(unsafe.Pointer(&bp)))
			s, b := com{sp}, com{bp}
			s.call(42, b.ptr())
			b.release()
			ob.Sample = sp
		}
		var status uint32
		r := e.mft.call(25, 0, 1, uintptr(unsafe.Pointer(&ob)), uintptr(unsafe.Pointer(&status))) // ProcessOutput
		if ob.Events != nil {
			ev := com{ob.Events}
			ev.release()
		}
		s := com{ob.Sample}
		switch uint32(r) {
		case mfENeedMore:
			s.release()
			return nil
		case mfEStreamChg:
			s.release()
			var tp unsafe.Pointer
			if hr(e.mft.call(14, 0, 0, uintptr(unsafe.Pointer(&tp)))) == nil { // GetOutputAvailableType
				t := com{tp}
				e.mft.call(16, 0, t.ptr(), 0)
				t.release()
			}
			continue
		}
		if err := hr(r); err != nil {
			s.release()
			return err
		}
		if s.ok() {
			e.emit(s)
			s.release()
		}
		if e.async {
			return nil // асинхронный кодер на каждый готовый кадр шлет свое событие
		}
	}
}

func (e *encoder) emit(s com) {
	var ts int64
	s.call(35, uintptr(unsafe.Pointer(&ts))) // GetSampleTime
	var clean uint32
	s.call(7, uintptr(unsafe.Pointer(&mfSampleCleanPoint)), uintptr(unsafe.Pointer(&clean))) // GetUINT32
	var bp unsafe.Pointer
	if hr(s.call(41, uintptr(unsafe.Pointer(&bp)))) != nil { // ConvertToContiguousBuffer
		return
	}
	b := com{bp}
	defer b.release()
	var data *byte
	var maxLen, cur uint32
	if hr(b.call(3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&maxLen)), uintptr(unsafe.Pointer(&cur)))) != nil { // Lock
		return
	}
	out := append([]byte(nil), unsafe.Slice(data, cur)...)
	b.call(4) // Unlock
	if e.onOutput != nil && len(out) > 0 {
		e.onOutput(out, clean != 0 || hasIDR(out), ts)
	}
}

// hasIDR ищет NAL ключевого кадра (тип 5) в потоке Annex B.
func hasIDR(b []byte) bool {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 && b[i+3]&0x1f == 5 {
			return true
		}
	}
	return false
}

func (e *encoder) forceKeyFrame() { e.setCodec(&codecForceKeyFrame, 1) }

func (e *encoder) setBitrate(kbps uint32) { e.setCodec(&codecMeanBitRate, kbps*1000) }

func (e *encoder) close() {
	if e.mft.ok() {
		e.mft.call(23, msgEndStrm, 0)
	}
	e.codec.release()
	e.events.release()
	if e.mft.ok() {
		// асинхронный кодер держит ссылку на себя, пока не скажешь Shutdown
		if sh, err := e.mft.qi(&iidIMFShutdown); err == nil {
			sh.call(3) // Shutdown
			sh.release()
		}
	}
	e.mft.release()
	e.mgr.release()
}

var iidIMFShutdown = guid("{97ec2ea4-0e42-4937-97ac-9d6d328824e1}")
