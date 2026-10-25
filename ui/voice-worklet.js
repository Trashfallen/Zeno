// Обработка микрофона в аудиопотоке: шумодав RNNoise + отсечка «не голоса».
//
// RNNoise (нейросеть Xiph, сборка Jitsi) кадрами по 10 мс убирает шум и
// заодно оценивает, есть ли в кадре речь (0..1). Звук передается, только
// когда это голос и он громче порога: чавканье, клавиатура, щелчки мыши и
// шорох между фразами не уходят.
//
// Чтобы не съедать края фраз:
//   - опережение 60 мс: звук идет с задержкой, а решение «голос» по текущему
//     кадру применяется к звуку на 60 мс раньше - первый слог успевает целиком;
//   - гистерезис: открыться - уверенная речь, оставаться открытым - хватает слабой;
//   - удержание 500 мс и плавное затухание - тихие концы слов не обрезаются.
// Общая задержка обработки - 70 мс.
//
// Без RNNoise (не загрузился) работает как обычный шумовой гейт по уровню.

import createRNNWasmModuleSync from "./rnnoise-sync.js";

const FRAME = 480; // 10 мс при 48 кГц - размер кадра RNNoise
const LOOKAHEAD = 6; // кадров опережения (60 мс)
const HOLD = 50; // кадров удержания после конца речи (500 мс)
const RING = 8192;

class ZenoVoice extends AudioWorkletProcessor {
  constructor() {
    super();
    this.denoise = true;
    this.vad = true;
    this.vadLevel = 0.5; // открыться
    this.vadKeep = 0.15; // оставаться открытым
    this.active = false;
    this.queue = [];
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
    this.wPos = FRAME * (LOOKAHEAD + 1); // тишина в запасе на время опережения: выход не опустеет
  }

  frame() {
    const x = this.inBuf;
    let sum = 0;
    for (let i = 0; i < FRAME; i++) sum += x[i] * x[i];
    const rms = Math.sqrt(sum / FRAME);
    const db = rms > 1e-6 ? 20 * Math.log10(rms) : -120;

    const out = new Float32Array(FRAME); // копия: кадр полежит в очереди опережения
    out.set(x);
    if (this.rnn && (this.denoise || this.vad)) {
      const heap = this.rnn.HEAPF32;
      const base = this.ptr >> 2;
      for (let i = 0; i < FRAME; i++) heap[base + i] = x[i] * 32768;
      const p = this.rnn._rnnoise_process_frame(this.state, this.ptr, this.ptr);
      this.prob = Math.max(p, this.prob * 0.7); // короткие провалы внутри слова не рвут речь
      if (this.denoise) for (let i = 0; i < FRAME; i++) out[i] = heap[base + i] / 32768;
    }

    this.floor += (db - this.floor) * (db < this.floor ? 0.1 : 0.002);
    const thr = this.mode === "auto" ? Math.min(Math.max(this.floor + 10, -70), -25) : this.threshold;
    // гистерезис: и по вероятности речи, и по громкости
    const voiceOn = !this.vad || !this.rnn || this.prob >= (this.active ? this.vadKeep : this.vadLevel);
    const loudOn = !this.enabled || db > (this.active ? thr - 6 : thr);
    this.active = voiceOn && loudOn;
    if (this.mode === "open" || this.active) this.hold = HOLD;
    else if (this.hold > 0) this.hold--;

    // решение по текущему кадру - к кадру из очереди (на LOOKAHEAD раньше)
    this.queue.push(out);
    if (this.queue.length <= LOOKAHEAD) return;
    const frame = this.queue.shift();
    const target = this.hold > 0 ? 1 : 0;
    const coef = target > this.gain ? 0.01 : 0.0004; // открыть ~2 мс, закрыть ~50 мс
    for (let i = 0; i < FRAME; i++) {
      this.gain += (target - this.gain) * coef;
      this.ring[this.wPos] = frame[i] * this.gain;
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
