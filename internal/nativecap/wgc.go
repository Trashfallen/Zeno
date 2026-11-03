package nativecap

// Windows Graphics Capture (WinRT) без CGO. У WinRT-интерфейсов после трех
// методов IUnknown идут три метода IInspectable, поэтому свои методы - с 6.

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iidIGraphicsCaptureItemInterop = guid("{3628e81b-3cac-4c60-b7f4-23ce0e0c3356}")
	iidIGraphicsCaptureItem        = guid("{79c3f95b-31f7-4ec2-a464-632ef5d30760}")
	iidIFramePoolStatics2          = guid("{589b103f-6bbc-5df5-a991-02e28b3b66d5}")
	iidIGraphicsCaptureSession2    = guid("{2c39ae40-7d2e-5044-804e-8b6799d4cf9e}")
	iidIGraphicsCaptureSession3    = guid("{f2cdd966-22ae-5ea1-9596-3a289344c3be}")
	iidIDirect3DDevice             = guid("{a37624ab-8d5f-4650-9d3e-9eae3d9bc670}")
	iidIDirect3DDxgiInterfaceAcc   = guid("{a9b3d012-3df2-4ee3-b8d1-8695f457d3c1}")
	iidIClosable                   = guid("{30d5a829-7fa4-4026-83bb-d75bae4ea99e}")
)

const pixelBGRA = 87 // DirectXPixelFormat.B8G8R8A8UIntNormalized

type sizeInt32 struct{ W, H int32 }

func (s sizeInt32) packed() uintptr { return uintptr(uint32(s.W)) | uintptr(uint32(s.H))<<32 }

// capture - сессия захвата окна или монитора.
type capture struct {
	d       *device
	rtDev   com // IDirect3DDevice
	item    com // IGraphicsCaptureItem
	pool    com // IDirect3D11CaptureFramePool
	session com // IGraphicsCaptureSession
	size    sizeInt32
}

func activationFactory(class string, iid *windows.GUID) (com, error) {
	s, _ := windows.UTF16FromString(class)
	var hs uintptr
	if err := hr(func() uintptr {
		r, _, _ := pWindowsCreateString.Call(uintptr(unsafe.Pointer(&s[0])), uintptr(len(s)-1), uintptr(unsafe.Pointer(&hs)))
		return r
	}()); err != nil {
		return com{}, err
	}
	defer pWindowsDeleteString.Call(hs)
	var f unsafe.Pointer
	r, _, _ := pRoGetActivationFact.Call(hs, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&f)))
	if err := hr(r); err != nil {
		return com{}, err
	}
	return com{f}, nil
}

// newCapture: window != 0 - окно, иначе монитор (HMONITOR).
func newCapture(d *device, window, monitor uintptr, cursor bool) (*capture, error) {
	pRoInitialize.Call(1) // RO_INIT_MULTITHREADED; повторный вызов безвреден
	c := &capture{d: d}
	interop, err := activationFactory("Windows.Graphics.Capture.GraphicsCaptureItem", &iidIGraphicsCaptureItemInterop)
	if err != nil {
		return nil, err
	}
	var item unsafe.Pointer
	if window != 0 {
		err = hr(interop.call(3, window, uintptr(unsafe.Pointer(&iidIGraphicsCaptureItem)), uintptr(unsafe.Pointer(&item))))
	} else {
		err = hr(interop.call(4, monitor, uintptr(unsafe.Pointer(&iidIGraphicsCaptureItem)), uintptr(unsafe.Pointer(&item))))
	}
	interop.release()
	if err != nil {
		return nil, err
	}
	c.item = com{item}
	if err := hr(c.item.call(7, uintptr(unsafe.Pointer(&c.size)))); err != nil { // get_Size
		c.close()
		return nil, err
	}
	if c.size.W <= 0 || c.size.H <= 0 {
		c.close()
		return nil, errors.New("у источника нулевой размер (окно свернуто?)")
	}
	if c.rtDev, err = d.winrtDevice(); err != nil {
		c.close()
		return nil, err
	}
	statics, err := activationFactory("Windows.Graphics.Capture.Direct3D11CaptureFramePool", &iidIFramePoolStatics2)
	if err != nil {
		c.close()
		return nil, err
	}
	var pool unsafe.Pointer
	err = hr(statics.call(6, c.rtDev.ptr(), pixelBGRA, 2, c.size.packed(), uintptr(unsafe.Pointer(&pool)))) // CreateFreeThreaded
	statics.release()
	if err != nil {
		c.close()
		return nil, err
	}
	c.pool = com{pool}
	var sess unsafe.Pointer
	if err := hr(c.pool.call(10, c.item.ptr(), uintptr(unsafe.Pointer(&sess)))); err != nil { // CreateCaptureSession
		c.close()
		return nil, err
	}
	c.session = com{sess}
	if s2, err := c.session.qi(&iidIGraphicsCaptureSession2); err == nil {
		s2.call(7, boolArg(cursor)) // put_IsCursorCaptureEnabled
		s2.release()
	}
	if s3, err := c.session.qi(&iidIGraphicsCaptureSession3); err == nil {
		s3.call(7, 0) // put_IsBorderRequired(false): без желтой рамки вокруг источника
		s3.release()
	}
	if err := hr(c.session.call(6)); err != nil { // StartCapture
		c.close()
		return nil, err
	}
	return c, nil
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// grab отдает все накопившиеся кадры (текстура, размер содержимого, размер
// текстуры пула, время кадра). ok=false - новых кадров нет.
func (c *capture) grab(onFrame func(tex com, w, h, texW, texH uint32, t int64)) (ok bool, err error) {
	for {
		var fr unsafe.Pointer
		if err := hr(c.pool.call(7, uintptr(unsafe.Pointer(&fr)))); err != nil { // TryGetNextFrame
			return ok, err
		}
		if fr == nil {
			return ok, nil
		}
		frame := com{fr}
		var cs sizeInt32
		frame.call(8, uintptr(unsafe.Pointer(&cs))) // get_ContentSize
		var t int64
		frame.call(7, uintptr(unsafe.Pointer(&t))) // get_SystemRelativeTime: момент кадра, 100 нс
		var surf unsafe.Pointer
		if hr(frame.call(6, uintptr(unsafe.Pointer(&surf)))) == nil && surf != nil { // get_Surface
			s := com{surf}
			if acc, err := s.qi(&iidIDirect3DDxgiInterfaceAcc); err == nil {
				var tex unsafe.Pointer
				if hr(acc.call(3, uintptr(unsafe.Pointer(&iidID3D11Texture2D)), uintptr(unsafe.Pointer(&tex)))) == nil {
					tx := com{tex}
					w, h := uint32(min(cs.W, c.size.W)), uint32(min(cs.H, c.size.H))
					if w > 0 && h > 0 {
						onFrame(tx, w, h, uint32(c.size.W), uint32(c.size.H), t)
						ok = true
					}
					tx.release()
				}
				acc.release()
			}
			s.release()
		}
		closeObj(frame)
		frame.release()
		// источник поменял размер - пул под новый размер
		if cs.W > 0 && cs.H > 0 && (cs.W != c.size.W || cs.H != c.size.H) {
			c.size = cs
			c.pool.call(6, c.rtDev.ptr(), pixelBGRA, 2, cs.packed()) // Recreate
		}
	}
}

func closeObj(o com) {
	if cl, err := o.qi(&iidIClosable); err == nil {
		cl.call(6) // Close
		cl.release()
	}
}

func (c *capture) close() {
	if c.session.ok() {
		closeObj(c.session)
	}
	c.session.release()
	if c.pool.ok() {
		closeObj(c.pool)
	}
	c.pool.release()
	c.item.release()
	c.rtDev.release()
}

// monitorAt - HMONITOR монитора, на котором точка (x, y).
func monitorAt(x, y int32) uintptr {
	pt := uintptr(uint32(x)) | uintptr(uint32(y))<<32
	m, _, _ := pMonitorFromPoint.Call(pt, 2) // MONITOR_DEFAULTTONEAREST
	return m
}
