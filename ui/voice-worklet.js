// Обработка микрофона в аудиопотоке: шумодав RNNoise + отсечка «не голоса».
//
// RNNoise (нейросеть Xiph, сборка Jitsi) кадрами по 10 мс убирает шум и
// заодно оценивает, есть ли в кадре речь (0..1). Звук передается, только
// когда это голос и он громче порога: чавканье, клавиатура, щелчки мыши и
// шорох между фразами не уходят. Открывается сразу, закрывается плавно после
// удержания 300 мс, чтобы не обрезать концы слов. Задержка - один кадр (10 мс).
//
// Без RNNoise (не загрузился) работает как обычный шумовой гейт по уровню.

import createRNNWasmModuleSync from "./rnnoise-sync.js";

const FRAME = 480; // 10 мс при 48 кГц - размер кадра RNNoise
const RING = 4096;

class ZenoVoice extends AudioWorkletProcessor {
  constructor() {
    super();
    this.denoise = true;
    this.vad = true;
    this.vadLevel = 0.5;
    this.enabled = true; // гейт
    this.mode = "auto"; // auto | manual | open (push-to-talk)
    this.threshold = -50;
    this.floor = -60;
    this.gain = 0;
    this.hold = 0;
    this.prob = 0;
    this.n = 0;
    this.port.onmessage = (e) => Object.assign(this, e.data);
    try {
      this.rnn = createRNNWasmModuleSync();
      this.state = this.rnn._rnnoise_create();
      this.ptr = this.rnn._malloc(FRAME * 4);
    } catch (err) {
      this.rnn = null;
      this.port.postMessage({ error: String(err) });
    }
    this.inBuf = new Float32Array(FRAME);
    this.inPos = 0;
    this.ring = new Float32Array(RING);
    this.rPos = 0;
    this.wPos = FRAME; // кадр тишины в запасе: выход не опустеет между кадрами
  }

  frame() {
    const x = this.inBuf;
    let sum = 0;
    for (let i = 0; i < FRAME; i++) sum += x[i] * x[i];
    const rms = Math.sqrt(sum / FRAME);
    const db = rms > 1e-6 ? 20 * Math.log10(rms) : -120;

    let out = x;
    if (this.rnn && (this.denoise || this.vad)) {
      const heap = this.rnn.HEAPF32;
      const base = this.ptr >> 2;
      for (let i = 0; i < FRAME; i++) heap[base + i] = x[i] * 32768;
      const p = this.rnn._rnnoise_process_frame(this.state, this.ptr, this.ptr);
      this.prob = Math.max(p, this.prob * 0.6); // короткие провалы внутри слова не рвут речь
      if (this.denoise) {
        out = new Float32Array(FRAME);
        for (let i = 0; i < FRAME; i++) out[i] = heap[base + i] / 32768;
      }
    }

    this.floor += (db - this.floor) * (db < this.floor ? 0.1 : 0.002);
    const thr = this.mode === "auto" ? Math.min(Math.max(this.floor + 10, -70), -25) : this.threshold;
    const voice = !this.vad || !this.rnn || this.prob >= this.vadLevel;
    const open = this.mode === "open" || (voice && (!this.enabled || db > thr));
    if (open) this.hold = 30; // 300 мс
    else if (this.hold > 0) this.hold--;

    const target = this.hold > 0 ? 1 : 0;
    const coef = target > this.gain ? 0.02 : 0.0015;
    for (let i = 0; i < FRAME; i++) {
      this.gain += (target - this.gain) * coef;
      this.ring[this.wPos] = out[i] * this.gain;
      this.wPos = (this.wPos + 1) % RING;
    }
    if (++this.n % 3 === 0) {
      this.port.postMessage({ level: db, open: this.hold > 0, threshold: thr, prob: this.prob });
    }
  }

  process(inputs, outputs) {
    const inp = inputs[0] && inputs[0][0];
    const out = outputs[0] && outputs[0][0];
    if (!out) return true;
    const len = out.length;
    for (let i = 0; i < len; i++) {
      this.inBuf[this.inPos++] = inp ? inp[i] : 0;
      if (this.inPos === FRAME) {
        this.frame();
        this.inPos = 0;
      }
    }
    for (let i = 0; i < len; i++) {
      out[i] = this.ring[this.rPos];
      this.rPos = (this.rPos + 1) % RING;
    }
    return true;
  }
}

registerProcessor("zeno-voice", ZenoVoice);
