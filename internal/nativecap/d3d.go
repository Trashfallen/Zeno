package nativecap

import "unsafe"

var (
	iidID3D10Multithread = guid("{9b7e4e00-342c-4106-a19f-4f2704f689f0}")
	iidID3D11VideoDevice = guid("{10ec4d5b-975a-4689-b9e4-d0aac30fe333}")
	iidID3D11VideoCtx    = guid("{61f21c45-3c0e-4a74-9cea-67100d9ad5e4}")
	iidIDXGIDevice       = guid("{54ec77fa-1377-44e6-8c32-88fd5f44c84c}")
	iidID3D11Texture2D   = guid("{6f15aaf2-d208-4e89-9ab4-489535d34f9c}")
)

const (
	fmtBGRA = 87  // DXGI_FORMAT_B8G8R8A8_UNORM
	fmtNV12 = 103 // DXGI_FORMAT_NV12

	bindShaderResource = 0x8
	bindRenderTarget   = 0x20
)

// device - D3D11 на основной видеокарте с поддержкой видео (нужно кодеру и видеопроцессору).
type device struct {
	dev, ctx   com // ID3D11Device, ID3D11DeviceContext
	vdev, vctx com // ID3D11VideoDevice, ID3D11VideoContext
}

func newDevice() (*device, error) {
	const driverHardware, sdkVersion = 1, 7
	const flags = 0x20 | 0x800 // BGRA_SUPPORT | VIDEO_SUPPORT
	var dev, ctx unsafe.Pointer
	var level uint32
	r, _, _ := pD3D11CreateDevice.Call(0, driverHardware, 0, flags, 0, 0, sdkVersion,
		uintptr(unsafe.Pointer(&dev)), uintptr(unsafe.Pointer(&level)), uintptr(unsafe.Pointer(&ctx)))
	if err := hr(r); err != nil {
		return nil, err
	}
	d := &device{dev: com{dev}, ctx: com{ctx}}
	// кодер Media Foundation зовет устройство из своих потоков
	if mt, err := d.dev.qi(&iidID3D10Multithread); err == nil {
		mt.call(5, 1) // SetMultithreadProtected(TRUE)
		mt.release()
	}
	var err error
	if d.vdev, err = d.dev.qi(&iidID3D11VideoDevice); err != nil {
		d.close()
		return nil, err
	}
	if d.vctx, err = d.ctx.qi(&iidID3D11VideoCtx); err != nil {
		d.close()
		return nil, err
	}
	return d, nil
}

func (d *device) close() {
	d.vctx.release()
	d.vdev.release()
	d.ctx.release()
	d.dev.release()
}

type texDesc struct {
	Width, Height, MipLevels, ArraySize, Format uint32
	SampleCount, SampleQuality                  uint32
	Usage, BindFlags, CPUAccessFlags, MiscFlags uint32
}

func (d *device) texture(w, h, format, bind uint32) (com, error) {
	desc := texDesc{Width: w, Height: h, MipLevels: 1, ArraySize: 1, Format: format, SampleCount: 1, BindFlags: bind}
	var t unsafe.Pointer
	if err := hr(d.dev.call(5, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&t)))); err != nil { // CreateTexture2D
		return com{}, err
	}
	return com{t}, nil
}

type box struct{ Left, Top, Front, Right, Bottom, Back uint32 }

// copyRegion копирует левый верхний угол w x h из src в dst.
func (d *device) copyRegion(dst, src com, w, h uint32) {
	b := box{Right: w, Bottom: h, Back: 1}
	d.ctx.call(46, dst.ptr(), 0, 0, 0, 0, src.ptr(), 0, uintptr(unsafe.Pointer(&b))) // CopySubresourceRegion
}

// winrtDevice - то же устройство в виде IDirect3DDevice для Windows Graphics Capture.
func (d *device) winrtDevice() (com, error) {
	dxgi, err := d.dev.qi(&iidIDXGIDevice)
	if err != nil {
		return com{}, err
	}
	defer dxgi.release()
	var insp unsafe.Pointer
	r, _, _ := pCreateDirect3D11FromD.Call(dxgi.ptr(), uintptr(unsafe.Pointer(&insp)))
	if err := hr(r); err != nil {
		return com{}, err
	}
	i := com{insp}
	defer i.release()
	return i.qi(&iidIDirect3DDevice)
}
