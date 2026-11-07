// Обработка микрофона в аудиопотоке: шумодав RNNoise + отсечка «не голоса».
//
// RNNoise 0.2 (нейросеть Xiph, сборка Shiguredo) кадрами по 10 мс убирает шум и
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
// Края фраз режет и сам шумодав: RNNoise 0.2 давит сильно и вместе с шумом
// съедает тихие первые и последние согласные. Поэтому рядом с началом и
// концом речи (до 60 мс раньше - из опережения - и 150 мс после) к очищенному
// звуку подмешивается половина исходного: на краях шум почти не слышен за
// голосом, а в середине фраз и в паузах работает полный шумодав. Речь тут
// определяется по громкости уже очищенного звука (не по оценке RNNoise):
// щелчки и стук клавиш RNNoise из него убрал, и защита на них не включается.
//
// Без RNNoise (не загрузился) работает как обычный шумовой гейт по уровню.

import { Rnnoise } from "./rnnoise-v2.js";

// сборка проверяет, что работает в окне или воркере; в аудиопотоке нет ни того, ни другого
if (typeof globalThis.window === "undefined") globalThis.window = globalThis;

const FRAME = 480; // 10 мс при 48 кГц - размер кадра RNNoise
const LOOKAHEAD = 6; // кадров опережения (60 мс)
const HOLD = 50; // кадров удержания после конца речи (500 мс)
const RING = 8192;
const EDGE = 15; // кадров защиты края после начала и после конца речи (150 мс)
const EDGE_MIX = 0.5; // доля исходного звука на краях
const MINRUN = 2; // речь короче 20 мс краем фразы не считается

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
    // модель грузится асинхронно (~50 мс); пока не готова - звук идет без шумодава
    this.rnn = null;
    Rnnoise.load().then((r) => {
      if (r.frameSize !== FRAME) throw new Error("кадр RNNoise " + r.frameSize);
      this.rnn = r.createDenoiseState();
      this.port.postMessage({ ready: true });
    }).catch((err) => this.port.postMessage({ error: String(err) }));
    this.pcm = new Float32Array(FRAME);
    this.rawHist = [new Float32Array(FRAME), new Float32Array(FRAME)]; // выход RNNoise отстает от входа на 2 кадра (20 мс)
    this.acts = []; // речь по кадрам: опережение + EDGE кадров назад
    this.edge = 0;
    this.voiceLvl = -60;
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
    let raw = null; // исходный звук, совмещенный с очищенным
    if (this.rnn && (this.denoise || this.vad)) {
      const pcm = this.pcm;
      for (let i = 0; i < FRAME; i++) pcm[i] = x[i] * 32768;
      const p = this.rnn.processFrame(pcm);
      this.prob = Math.max(p, this.prob * 0.7); // короткие провалы внутри слова не рвут речь
      if (this.denoise) {
        for (let i = 0; i < FRAME; i++) out[i] = pcm[i] / 32768;
        raw = this.rawHist[0];
      }
      this.rawHist.shift();
      this.rawHist.push(x.slice());
    }
    // речь для защиты краев - по громкости уже очищенного звука: щелчки и стук
    // RNNoise из него убрал, а голос остался. Порог - 25 дБ ниже громкости голоса.
    let es = 0;
    for (let i = 0; i < FRAME; i++) es += out[i] * out[i];
    const den = es > 0 ? 10 * Math.log10(es / FRAME) : -120;
    this.voiceLvl = Math.max(den, this.voiceLvl - 0.02); // громкость голоса: пик, медленно спадает
    this.acts.push(!!raw && den > -70 && den > this.voiceLvl - 25);
    if (this.acts.length > LOOKAHEAD + EDGE + 1) this.acts.shift();

    this.floor += (db - this.floor) * (db < this.floor ? 0.1 : 0.002);
    const thr = this.mode === "auto" ? Math.min(Math.max(this.floor + 10, -70), -25) : this.threshold;
    // гистерезис: и по вероятности речи, и по громкости
    const voiceOn = !this.vad || !this.rnn || this.prob >= (this.active ? this.vadKeep : this.vadLevel);
    const loudOn = !this.enabled || db > (this.active ? thr - 6 : thr);
    this.active = voiceOn && loudOn;
    if (this.mode === "open" || this.active) this.hold = HOLD;
    else if (this.hold > 0) this.hold--;

    // решение по текущему кадру - к кадру из очереди (на LOOKAHEAD раньше)
    this.queue.push([out, raw]);
    if (this.queue.length <= LOOKAHEAD) return;
    const [frame, fraw] = this.queue.shift();
    // край фразы рядом: начало или конец речи в окне от EDGE кадров назад до опережения
    const a = this.acts;
    let near = false;
    for (let i = 0, run = 0; i < a.length; i++) {
      run = a[i] ? run + 1 : 0;
      if (run >= MINRUN) { near = true; break; }
    }
    // в окне идет речь целиком (середина фразы) - это не край
    if (near && a.every((v) => v)) near = false;
    const edgeT = near && fraw ? EDGE_MIX : 0;
    const target = this.hold > 0 ? 1 : 0;
    const coef = target > this.gain ? 0.01 : 0.0004; // открыть ~2 мс, закрыть ~50 мс
    for (let i = 0; i < FRAME; i++) {
      this.gain += (target - this.gain) * coef;
      this.edge += (edgeT - this.edge) * 0.003; // ~7 мс, без щелчков
      const v = fraw ? frame[i] + this.edge * (fraw[i] - frame[i]) : frame[i];
      this.ring[this.wPos] = v * this.gain;
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
