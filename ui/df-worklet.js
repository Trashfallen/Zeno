// Шумодав DeepFilterNet3 в аудиопотоке (режим «DeepFilterNet» в настройках).
//
// Нейросеть DeepFilterNet3 (Hendrik Schröter, github.com/Rikorose/DeepFilterNet)
// чище RNNoise, но тяжелее: wasm-движок ONNX и модель ~8 МБ, процессор
// грузит в несколько раз сильнее, задержка больше (~40 мс против ~10).
// wasm и модель грузит voice.js из ui/df/ и передает сюда готовыми.
//
// Ниже до класса ZenoDFN - обертка wasm-bindgen из пакета
// deepfilternet3-noise-filter 1.3.0 (github.com/mezonai/mezon-noise-suppression,
// Apache-2.0 OR MIT, лицензии в third_party/deepfilternet-LICENSE-*.txt).
// Она должна точно соответствовать ui/df/df_bg.wasm.gz - не править.

// в AudioWorkletGlobalScope может не быть crypto: wasm берет случайные байты только для хешей
if (typeof globalThis.crypto === "undefined") {
  globalThis.crypto = { getRandomValues(a) { for (let i = 0; i < a.length; i++) a[i] = (Math.random() * 256) | 0; return a; } };
}

/* @ts-self-types="./df.d.ts" */

// --- AudioWorklet polyfill ---------------------------------------------------
// AudioWorkletGlobalScope has no TextDecoder/TextEncoder. wasm-bindgen 0.2.126
// constructs `new TextDecoder()` at module top-level (unguarded), which throws
// `ReferenceError: TextDecoder is not defined` inside a worklet and prevents
// registerProcessor() from running. Provide minimal UTF-8 shims when absent.
// (Only used for error/diagnostic strings; the audio path passes bytes/floats.)
if (typeof TextDecoder === 'undefined') {
    globalThis.TextDecoder = class {
        decode(bytes) {
            if (!bytes) return '';
            const u8 = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes.buffer || bytes);
            let out = '';
            for (let i = 0; i < u8.length;) {
                const c = u8[i++];
                if (c < 0x80) out += String.fromCharCode(c);
                else if (c < 0xE0) out += String.fromCharCode(((c & 0x1F) << 6) | (u8[i++] & 0x3F));
                else if (c < 0xF0) out += String.fromCharCode(((c & 0x0F) << 12) | ((u8[i++] & 0x3F) << 6) | (u8[i++] & 0x3F));
                else {
                    const cp = (((c & 0x07) << 18) | ((u8[i++] & 0x3F) << 12) | ((u8[i++] & 0x3F) << 6) | (u8[i++] & 0x3F)) - 0x10000;
                    out += String.fromCharCode(0xD800 + (cp >> 10), 0xDC00 + (cp & 0x3FF));
                }
            }
            return out;
        }
    };
}
if (typeof TextEncoder === 'undefined') {
    globalThis.TextEncoder = class {
        encode(str) {
            const out = [];
            for (let i = 0; i < str.length; i++) {
                let c = str.charCodeAt(i);
                if (c < 0x80) out.push(c);
                else if (c < 0x800) out.push(0xC0 | (c >> 6), 0x80 | (c & 0x3F));
                else if (c >= 0xD800 && c < 0xDC00) {
                    const c2 = str.charCodeAt(++i);
                    c = 0x10000 + ((c & 0x3FF) << 10) + (c2 & 0x3FF);
                    out.push(0xF0 | (c >> 18), 0x80 | ((c >> 12) & 0x3F), 0x80 | ((c >> 6) & 0x3F), 0x80 | (c & 0x3F));
                } else out.push(0xE0 | (c >> 12), 0x80 | ((c >> 6) & 0x3F), 0x80 | (c & 0x3F));
            }
            return new Uint8Array(out);
        }
        encodeInto(str, dst) {
            const enc = this.encode(str);
            dst.set(enc);
            return { read: str.length, written: enc.length };
        }
    };
}

/**
 * Create a DeepFilterNet Model
 *
 * Args:
 *     - path: File path to a DeepFilterNet tar.gz onnx model
 *     - atten_lim: Attenuation limit in dB.
 *
 * Returns:
 *     - DF state doing the full processing: stft, DNN noise reduction, istft.
 * @param {Uint8Array} model_bytes
 * @param {number} atten_lim
 * @returns {number}
 */
function df_create(model_bytes, atten_lim) {
    const ptr0 = passArray8ToWasm0(model_bytes, wasm.__wbindgen_malloc_command_export);
    const len0 = WASM_VECTOR_LEN;
    const ret = wasm.df_create(ptr0, len0, atten_lim);
    return ret >>> 0;
}

/**
 * Get DeepFilterNet frame size in samples.
 * @param {number} st
 * @returns {number}
 */
function df_get_frame_length(st) {
    const ret = wasm.df_get_frame_length(st);
    return ret >>> 0;
}

/**
 * Processes a chunk of samples.
 *
 * Args:
 *     - df_state: Created via df_create()
 *     - input: Input buffer of length df_get_frame_length()
 *     - output: Output buffer of length df_get_frame_length()
 *
 * Returns:
 *     - Local SNR of the current frame.
 * @param {number} st
 * @param {Float32Array} input
 * @returns {Float32Array}
 */
function df_process_frame(st, input) {
    const ptr0 = passArrayF32ToWasm0(input, wasm.__wbindgen_malloc_command_export);
    const len0 = WASM_VECTOR_LEN;
    const ret = wasm.df_process_frame(st, ptr0, len0);
    return ret;
}

/**
 * Set DeepFilterNet attenuation limit.
 *
 * Args:
 *     - lim_db: New attenuation limit in dB.
 * @param {number} st
 * @param {number} lim_db
 */
function df_set_atten_lim(st, lim_db) {
    wasm.df_set_atten_lim(st, lim_db);
}
function __wbg_get_imports() {
    const import0 = {
        __proto__: null,
        __wbg___wbindgen_throw_344f42d3211c4765: function(arg0, arg1) {
            throw new Error(getStringFromWasm0(arg0, arg1));
        },
        __wbg_getRandomValues_cc7f052a444bb2ce: function() { return handleError(function (arg0, arg1) {
            globalThis.crypto.getRandomValues(getArrayU8FromWasm0(arg0, arg1));
        }, arguments); },
        __wbg_new_from_slice_ddf8b82c4d6af38e: function(arg0, arg1) {
            const ret = new Float32Array(getArrayF32FromWasm0(arg0, arg1));
            return ret;
        },
        __wbindgen_init_externref_table: function() {
            const table = wasm.__wbindgen_externrefs;
            const offset = table.grow(4);
            table.set(0, undefined);
            table.set(offset + 0, undefined);
            table.set(offset + 1, null);
            table.set(offset + 2, true);
            table.set(offset + 3, false);
        },
    };
    return {
        __proto__: null,
        "./df_bg.js": import0,
    };
}

(typeof FinalizationRegistry === 'undefined')
    ? { }
    : new FinalizationRegistry(ptr => wasm.__wbg_dfstate_free(ptr, 1));

function addToExternrefTable0(obj) {
    const idx = wasm.__externref_table_alloc_command_export();
    wasm.__wbindgen_externrefs.set(idx, obj);
    return idx;
}

function getArrayF32FromWasm0(ptr, len) {
    ptr = ptr >>> 0;
    return getFloat32ArrayMemory0().subarray(ptr / 4, ptr / 4 + len);
}

function getArrayU8FromWasm0(ptr, len) {
    ptr = ptr >>> 0;
    return getUint8ArrayMemory0().subarray(ptr / 1, ptr / 1 + len);
}

let cachedFloat32ArrayMemory0 = null;
function getFloat32ArrayMemory0() {
    if (cachedFloat32ArrayMemory0 === null || cachedFloat32ArrayMemory0.byteLength === 0) {
        cachedFloat32ArrayMemory0 = new Float32Array(wasm.memory.buffer);
    }
    return cachedFloat32ArrayMemory0;
}

function getStringFromWasm0(ptr, len) {
    return decodeText(ptr >>> 0, len);
}

let cachedUint8ArrayMemory0 = null;
function getUint8ArrayMemory0() {
    if (cachedUint8ArrayMemory0 === null || cachedUint8ArrayMemory0.byteLength === 0) {
        cachedUint8ArrayMemory0 = new Uint8Array(wasm.memory.buffer);
    }
    return cachedUint8ArrayMemory0;
}

function handleError(f, args) {
    try {
        return f.apply(this, args);
    } catch (e) {
        const idx = addToExternrefTable0(e);
        wasm.__wbindgen_exn_store_command_export(idx);
    }
}

function passArray8ToWasm0(arg, malloc) {
    const ptr = malloc(arg.length * 1, 1) >>> 0;
    getUint8ArrayMemory0().set(arg, ptr / 1);
    WASM_VECTOR_LEN = arg.length;
    return ptr;
}

function passArrayF32ToWasm0(arg, malloc) {
    const ptr = malloc(arg.length * 4, 4) >>> 0;
    getFloat32ArrayMemory0().set(arg, ptr / 4);
    WASM_VECTOR_LEN = arg.length;
    return ptr;
}

let cachedTextDecoder = new TextDecoder('utf-8', { ignoreBOM: true, fatal: true });
cachedTextDecoder.decode();
const MAX_SAFARI_DECODE_BYTES = 2146435072;
let numBytesDecoded = 0;
function decodeText(ptr, len) {
    numBytesDecoded += len;
    if (numBytesDecoded >= MAX_SAFARI_DECODE_BYTES) {
        cachedTextDecoder = new TextDecoder('utf-8', { ignoreBOM: true, fatal: true });
        cachedTextDecoder.decode();
        numBytesDecoded = len;
    }
    return cachedTextDecoder.decode(getUint8ArrayMemory0().subarray(ptr, ptr + len));
}

let WASM_VECTOR_LEN = 0;

let wasm;
function __wbg_finalize_init(instance, module) {
    wasm = instance.exports;
    cachedFloat32ArrayMemory0 = null;
    cachedUint8ArrayMemory0 = null;
    wasm.__wbindgen_start();
    return wasm;
}

function initSync(module) {
    if (wasm !== undefined) return wasm;


    if (module !== undefined) {
        if (Object.getPrototypeOf(module) === Object.prototype) {
            ({module} = module);
        } else {
            console.warn('using deprecated parameters for `initSync()`; pass a single object instead');
        }
    }

    const imports = __wbg_get_imports();
    if (!(module instanceof WebAssembly.Module)) {
        module = new WebAssembly.Module(module);
    }
    const instance = new WebAssembly.Instance(module, imports);
    return __wbg_finalize_init(instance);
}


// ZenoDFN пропускает звук через DeepFilterNet кадрами (480 отсчетов = 10 мс
// при 48 кГц). Выход стартует с кадра тишины в запасе - так он не опустеет,
// пока копится следующий кадр. Не загрузился - звук идет как есть.
class ZenoDFN extends AudioWorkletProcessor {
  constructor(options) {
    super();
    this.st = 0;
    this.bypass = false;
    try {
      const o = options.processorOptions;
      initSync({ module: o.wasmModule });
      this.st = df_create(new Uint8Array(o.modelBytes), o.level ?? 100);
      this.len = df_get_frame_length(this.st);
    } catch (err) {
      this.st = 0;
      this.port.postMessage({ error: String(err) });
    }
    this.size = 8192;
    this.inBuf = new Float32Array(this.size);
    this.outBuf = new Float32Array(this.size);
    this.frame = new Float32Array(this.len || 480);
    this.inW = this.inR = this.outR = 0;
    this.outW = this.len || 0;
    this.port.onmessage = (e) => {
      if (typeof e.data.bypass === "boolean") this.bypass = e.data.bypass;
      if (this.st && typeof e.data.level === "number") df_set_atten_lim(this.st, Math.max(0, Math.min(100, e.data.level)));
    };
    if (this.st) this.port.postMessage({ ready: true });
  }

  process(inputs, outputs) {
    const inp = inputs[0] && inputs[0][0];
    const out = outputs[0] && outputs[0][0];
    if (!out) return true;
    if (!this.st || this.bypass) {
      if (inp) out.set(inp);
      return true;
    }
    const n = out.length, size = this.size, len = this.len;
    for (let i = 0; i < n; i++) {
      this.inBuf[this.inW] = inp ? inp[i] : 0;
      this.inW = (this.inW + 1) % size;
    }
    while ((this.inW - this.inR + size) % size >= len) {
      for (let i = 0; i < len; i++) {
        this.frame[i] = this.inBuf[this.inR];
        this.inR = (this.inR + 1) % size;
      }
      const res = df_process_frame(this.st, this.frame);
      for (let i = 0; i < res.length; i++) {
        this.outBuf[this.outW] = res[i];
        this.outW = (this.outW + 1) % size;
      }
    }
    if ((this.outW - this.outR + size) % size < n) return true; // не успели - тишина
    for (let i = 0; i < n; i++) {
      out[i] = this.outBuf[this.outR];
      this.outR = (this.outR + 1) % size;
    }
    return true;
  }
}

registerProcessor("zeno-dfn", ZenoDFN);
