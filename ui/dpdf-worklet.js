// Аудиопоток шумодава DPDFNet: режет звук на кадры по 10 мс и отдает их
// в dpdf-worker.js, а оттуда принимает очищенные. Сама модель тут не
// считается - на нее уходит несколько миллисекунд, аудиопоток бы заикался.
//
// Выход начинает играть, когда в запасе два готовых кадра (~10 мс запаса
// на задержки потока). Не успели - кадр тишины и снова копим запас.

const H = 480;
const RING = 16384;

class ZenoDPDF extends AudioWorkletProcessor {
  constructor() {
    super();
    this.inBuf = new Float32Array(H);
    this.inPos = 0;
    this.ring = new Float32Array(RING);
    this.w = this.r = 0;
    this.playing = false;
    this.bypass = false;
    this.worker = null;
    this.port.onmessage = (e) => {
      if (e.data.port) {
        this.worker = e.data.port;
        this.worker.onmessage = (m) => this.push(m.data);
      }
      if (typeof e.data.bypass === "boolean") this.bypass = e.data.bypass;
    };
  }

  push(x) {
    for (let i = 0; i < x.length; i++) {
      this.ring[this.w] = x[i];
      this.w = (this.w + 1) % RING;
    }
  }

  process(inputs, outputs) {
    const inp = inputs[0] && inputs[0][0];
    const out = outputs[0] && outputs[0][0];
    if (!out) return true;
    if (this.bypass || !this.worker) {
      if (inp) out.set(inp);
      return true;
    }
    const n = out.length;
    for (let i = 0; i < n; i++) {
      this.inBuf[this.inPos++] = inp ? inp[i] : 0;
      if (this.inPos === H) {
        const f = this.inBuf.slice();
        this.worker.postMessage(f, [f.buffer]);
        this.inPos = 0;
      }
    }
    const have = (this.w - this.r + RING) % RING;
    if (!this.playing && have >= 2 * H) this.playing = true;
    if (this.playing && have < n) this.playing = false;
    if (!this.playing) return true; // тишина, пока копится запас
    for (let i = 0; i < n; i++) {
      out[i] = this.ring[this.r];
      this.r = (this.r + 1) % RING;
    }
    return true;
  }
}

registerProcessor("zeno-dpdf", ZenoDPDF);
