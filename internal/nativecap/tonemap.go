package nativecap

// Перевод HDR-кадра (scRGB, R16G16B16A16 float) в обычный RGBA 8 бит на
// видеокарте вычислительным шейдером. Шейдер компилируется при запуске
// системным d3dcompiler_47.dll.

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	d3dcompiler = windows.NewLazySystemDLL("d3dcompiler_47.dll")
	pD3DCompile = d3dcompiler.NewProc("D3DCompile")
)

const (
	fmtRGBA16F = 10 // DXGI_FORMAT_R16G16B16A16_FLOAT
	fmtRGBA8   = 28 // DXGI_FORMAT_R8G8B8A8_UNORM

	bindUnorderedAccess = 0x80
	bindConstantBuffer  = 0x4
	pixelRGBA16F        = 10 // DirectXPixelFormat.R16G16B16A16Float
)

// scRGB: 1.0 = 80 нит. Делим на белый SDR - белый интерфейса становится 1.0.
// Что ярче (блики, солнце), сжимается плавно: до knee без изменений, дальше
// кривая подходит к 1.0, но не обрезается. Цвет сохраняем, сжимая по
// максимальному каналу. Потом гамма sRGB.
const toneMapHLSL = `
Texture2D<float4> src : register(t0);
RWTexture2D<unorm float4> dst : register(u0);
cbuffer P : register(b0) { float scale; float knee; uint w; uint h; };

float3 srgb(float3 c) {
	c = saturate(c);
	return c <= 0.0031308 ? c * 12.92 : 1.055 * pow(c, 1.0 / 2.4) - 0.055;
}

[numthreads(16, 16, 1)]
void main(uint3 id : SV_DispatchThreadID) {
	if (id.x >= w || id.y >= h) return;
	float3 c = max(src[id.xy].rgb, 0) * scale;
	float m = max(c.r, max(c.g, c.b));
	if (m > knee) {
		float t = (m - knee) / (1.0 - knee);
		float nm = knee + (1.0 - knee) * (t / (1.0 + t));
		c *= nm / m;
	}
	dst[id.xy] = float4(srgb(c), 1);
}
`

type toneMapper struct {
	d      *device
	cs, cb com
	white  float64
}

func newToneMapper(d *device, whiteNit float64) (*toneMapper, error) {
	src := []byte(toneMapHLSL)
	entry := []byte("main\x00")
	target := []byte("cs_5_0\x00")
	var code, errs unsafe.Pointer
	r, _, _ := pD3DCompile.Call(uintptr(unsafe.Pointer(&src[0])), uintptr(len(src)), 0, 0, 0,
		uintptr(unsafe.Pointer(&entry[0])), uintptr(unsafe.Pointer(&target[0])), 1<<11, 0, // OPTIMIZATION_LEVEL3
		uintptr(unsafe.Pointer(&code)), uintptr(unsafe.Pointer(&errs)))
	if errs != nil {
		e := com{errs}
		e.release()
	}
	if err := hr(r); err != nil {
		return nil, errors.New("шейдер HDR не собрался: " + err.Error())
	}
	blob := com{code}
	defer blob.release()
	ptr := blob.call(3)  // GetBufferPointer
	size := blob.call(4) // GetBufferSize
	t := &toneMapper{d: d, white: max(whiteNit, 80)}
	var cs unsafe.Pointer
	if err := hr(d.dev.call(18, ptr, size, 0, uintptr(unsafe.Pointer(&cs)))); err != nil { // CreateComputeShader
		return nil, err
	}
	t.cs = com{cs}
	bd := struct{ ByteWidth, Usage, Bind, CPU, Misc, Stride uint32 }{ByteWidth: 16, Bind: bindConstantBuffer}
	var cb unsafe.Pointer
	if err := hr(d.dev.call(3, uintptr(unsafe.Pointer(&bd)), 0, uintptr(unsafe.Pointer(&cb)))); err != nil { // CreateBuffer
		t.close()
		return nil, err
	}
	t.cb = com{cb}
	return t, nil
}

// run: src (FP16, есть SRV) -> dst (RGBA8, есть UAV), область w x h.
func (t *toneMapper) run(srv, uav com, w, h uint32) {
	p := struct {
		Scale, Knee float32
		W, H        uint32
	}{float32(80 / t.white), 0.75, w, h}
	ctx := t.d.ctx
	ctx.call(48, t.cb.ptr(), 0, 0, uintptr(unsafe.Pointer(&p)), 0, 0) // UpdateSubresource
	s, u, b := srv.ptr(), uav.ptr(), t.cb.ptr()
	ctx.call(69, t.cs.ptr(), 0, 0)                          // CSSetShader
	ctx.call(67, 0, 1, uintptr(unsafe.Pointer(&s)))         // CSSetShaderResources
	ctx.call(68, 0, 1, uintptr(unsafe.Pointer(&u)), 0)      // CSSetUnorderedAccessViews
	ctx.call(71, 0, 1, uintptr(unsafe.Pointer(&b)))         // CSSetConstantBuffers
	ctx.call(41, uintptr((w+15)/16), uintptr((h+15)/16), 1) // Dispatch
	var zero uintptr
	ctx.call(67, 0, 1, uintptr(unsafe.Pointer(&zero)))    // отвязываем, чтобы текстуры можно было читать дальше
	ctx.call(68, 0, 1, uintptr(unsafe.Pointer(&zero)), 0) //
}

func (t *toneMapper) close() {
	t.cb.release()
	t.cs.release()
}

// views для пары текстур: SRV на FP16 и UAV на RGBA8 (описания по умолчанию).
func (d *device) srv(tex com) (com, error) {
	var v unsafe.Pointer
	if err := hr(d.dev.call(7, tex.ptr(), 0, uintptr(unsafe.Pointer(&v)))); err != nil { // CreateShaderResourceView
		return com{}, err
	}
	return com{v}, nil
}

func (d *device) uav(tex com) (com, error) {
	var v unsafe.Pointer
	if err := hr(d.dev.call(8, tex.ptr(), 0, uintptr(unsafe.Pointer(&v)))); err != nil { // CreateUnorderedAccessView
		return com{}, err
	}
	return com{v}, nil
}
