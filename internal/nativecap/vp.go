package nativecap

// Видеопроцессор D3D11: кадр BGRA любого размера -> NV12 нужного размера с
// сохранением пропорций (черные поля), все на видеокарте.

import (
	"unsafe"
)

type rational struct{ Num, Den uint32 }

type vpContentDesc struct {
	InputFrameFormat uint32
	InputFrameRate   rational
	InputW, InputH   uint32
	OutputFrameRate  rational
	OutputW, OutputH uint32
	Usage            uint32
}

type vpStream struct {
	Enable, OutputIndex, InputFrameOrField, PastFrames, FutureFrames uint32
	_                                                                uint32
	PastSurfaces, InputSurface, FutureSurfaces                       uintptr
	PastSurfacesRight, InputSurfaceRight, FutureSurfacesRight        uintptr
}

type rectI struct{ Left, Top, Right, Bottom int32 }

const nv12Ring = 4 // кодер работает асинхронно - пишем по кругу в несколько текстур

type converter struct {
	d          *device
	fps        uint32
	inW, inH   uint32 // размер входной текстуры (под размер источника)
	outW, outH uint32
	enum, vp   com
	inTex      com
	inView     com
	out        [nv12Ring]struct{ tex, view com }
	next       int
	srcW, srcH uint32 // размер содержимого последнего кадра

	// HDR: кадр FP16 копируется в hdrTex, шейдер пишет обычный RGBA8 в inTex
	tm     *toneMapper
	hdrTex com
	hdrSRV com
	inUAV  com
}

func newConverter(d *device, outW, outH, fps uint32, tm *toneMapper) (*converter, error) {
	c := &converter{d: d, outW: outW, outH: outH, fps: fps, tm: tm}
	for i := range c.out {
		t, err := d.texture(outW, outH, fmtNV12, bindRenderTarget)
		if err != nil {
			c.close()
			return nil, err
		}
		c.out[i].tex = t
	}
	return c, nil
}

// setInput готовит входную текстуру и процессор под размер источника.
func (c *converter) setInput(w, h uint32) error {
	if c.inTex.ok() && c.inW == w && c.inH == h {
		return nil
	}
	c.inView.release()
	c.inUAV.release()
	c.inTex.release()
	c.hdrSRV.release()
	c.hdrTex.release()
	c.vp.release()
	c.enum.release()
	for i := range c.out {
		c.out[i].view.release()
	}
	desc := vpContentDesc{InputFrameRate: rational{c.fps, 1}, InputW: w, InputH: h,
		OutputFrameRate: rational{c.fps, 1}, OutputW: c.outW, OutputH: c.outH, Usage: 1} // OPTIMAL_SPEED
	var e unsafe.Pointer
	if err := hr(c.d.vdev.call(10, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&e)))); err != nil { // CreateVideoProcessorEnumerator
		return err
	}
	c.enum = com{e}
	var vp unsafe.Pointer
	if err := hr(c.d.vdev.call(4, c.enum.ptr(), 0, uintptr(unsafe.Pointer(&vp)))); err != nil { // CreateVideoProcessor
		return err
	}
	c.vp = com{vp}
	inFmt, inBind := uint32(fmtBGRA), uint32(bindRenderTarget|bindShaderResource)
	if c.tm != nil {
		inFmt, inBind = fmtRGBA8, inBind|bindUnorderedAccess
	}
	t, err := c.d.texture(w, h, inFmt, inBind)
	if err != nil {
		return err
	}
	c.inTex = t
	if c.tm != nil {
		if c.hdrTex, err = c.d.texture(w, h, fmtRGBA16F, bindShaderResource); err != nil {
			return err
		}
		if c.hdrSRV, err = c.d.srv(c.hdrTex); err != nil {
			return err
		}
		if c.inUAV, err = c.d.uav(c.inTex); err != nil {
			return err
		}
	}
	inDesc := struct{ FourCC, Dim, Mip, Slice uint32 }{Dim: 1} // D3D11_VPIV_DIMENSION_TEXTURE2D
	var iv unsafe.Pointer
	if err := hr(c.d.vdev.call(8, c.inTex.ptr(), c.enum.ptr(), uintptr(unsafe.Pointer(&inDesc)), uintptr(unsafe.Pointer(&iv)))); err != nil { // CreateVideoProcessorInputView
		return err
	}
	c.inView = com{iv}
	outDesc := struct{ Dim, Mip, A, B uint32 }{Dim: 1} // D3D11_VPOV_DIMENSION_TEXTURE2D
	for i := range c.out {
		var ov unsafe.Pointer
		if err := hr(c.d.vdev.call(9, c.out[i].tex.ptr(), c.enum.ptr(), uintptr(unsafe.Pointer(&outDesc)), uintptr(unsafe.Pointer(&ov)))); err != nil { // CreateVideoProcessorOutputView
			return err
		}
		c.out[i].view = com{ov}
	}
	c.inW, c.inH = w, h
	// вход - RGB полный диапазон, выход - BT.709 16-235
	inCS, outCS := uint32(0), uint32(1<<2|1<<4) // YCbCr_Matrix=709, Nominal_Range=16_235
	vc := c.d.vctx
	vc.call(28, c.vp.ptr(), 0, uintptr(unsafe.Pointer(&inCS)))  // SetStreamColorSpace
	vc.call(15, c.vp.ptr(), uintptr(unsafe.Pointer(&outCS)))    // SetOutputColorSpace
	vc.call(27, c.vp.ptr(), 0, 0)                               // SetStreamFrameFormat(PROGRESSIVE)
	vc.call(37, c.vp.ptr(), 0, 0)                               // SetStreamAutoProcessingMode(FALSE)
	black := [4]float32{0, 0.5, 0.5, 1}                         // Y, Cb, Cr, A
	vc.call(14, c.vp.ptr(), 1, uintptr(unsafe.Pointer(&black))) // SetOutputBackgroundColor(YCbCr)
	full := rectI{0, 0, int32(c.outW), int32(c.outH)}
	vc.call(13, c.vp.ptr(), 1, uintptr(unsafe.Pointer(&full))) // SetOutputTargetRect
	return nil
}

// put копирует содержимое w x h кадра источника во входную текстуру размера
// пула захвата (texW x texH): пока окно не меняет размер пула, процессор не пересоздается.
func (c *converter) put(src com, w, h, texW, texH uint32) error {
	if err := c.setInput(texW, texH); err != nil {
		return err
	}
	if c.tm != nil {
		c.d.copyRegion(c.hdrTex, src, w, h)
		c.tm.run(c.hdrSRV, c.inUAV, w, h)
	} else {
		c.d.copyRegion(c.inTex, src, w, h)
	}
	c.srcW, c.srcH = w, h
	return nil
}

// convert переводит последний кадр в следующую NV12-текстуру и возвращает ее.
func (c *converter) convert() (com, error) {
	if !c.inView.ok() || c.srcW == 0 {
		return com{}, errNoFrame
	}
	o := &c.out[c.next]
	c.next = (c.next + 1) % nv12Ring
	// вписываем с сохранением пропорций
	sw, sh := float64(c.srcW), float64(c.srcH)
	scale := min(float64(c.outW)/sw, float64(c.outH)/sh)
	dw, dh := int32(sw*scale+0.5), int32(sh*scale+0.5)
	dx, dy := (int32(c.outW)-dw)/2, (int32(c.outH)-dh)/2
	src := rectI{0, 0, int32(c.srcW), int32(c.srcH)}
	dst := rectI{dx, dy, dx + dw, dy + dh}
	vc := c.d.vctx
	vc.call(30, c.vp.ptr(), 0, 1, uintptr(unsafe.Pointer(&src))) // SetStreamSourceRect
	vc.call(31, c.vp.ptr(), 0, 1, uintptr(unsafe.Pointer(&dst))) // SetStreamDestRect
	st := vpStream{Enable: 1, InputSurface: c.inView.ptr()}
	if err := hr(vc.call(53, c.vp.ptr(), o.view.ptr(), 0, 1, uintptr(unsafe.Pointer(&st)))); err != nil { // VideoProcessorBlt
		return com{}, err
	}
	return o.tex, nil
}

func (c *converter) close() {
	c.inView.release()
	c.inUAV.release()
	c.inTex.release()
	c.hdrSRV.release()
	c.hdrTex.release()
	for i := range c.out {
		c.out[i].view.release()
		c.out[i].tex.release()
	}
	c.vp.release()
	c.enum.release()
}
