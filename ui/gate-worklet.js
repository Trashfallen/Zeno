// Шумовой гейт микрофона (как «чувствительность ввода» в Discord).
//
// Пока уровень ниже порога, звук не передается: нет шороха, клавиатуры и
// гула между фразами. В режиме «авто» порог сам держится на 10 дБ выше
// фонового шума (уровень шума отслеживается: быстро вниз, медленно вверх).
// Открывается за ~1 мс, закрывается плавно после удержания 250 мс, чтобы не
// обрезать концы слов.

class ZenoGate extends AudioWorkletProcessor {
  constructor() {
    super();
    this.enabled = true;
    this.mode = "auto"; // auto | manual | open
    this.threshold = -50;
    this.floor = -60;
    this.gain = 0;
    this.hold = 0;
    this.n = 0;
    this.port.onmessage = (e) => Object.assign(this, e.data);
  }

  process(inputs, outputs) {
    const inp = inputs[0] && inputs[0][0];
    const out = outputs[0] && outputs[0][0];
    if (!out) return true;
    if (!inp) {
      out.fill(0);
      return true;
    }
    let sum = 0;
    for (let i = 0; i < inp.length; i++) sum += inp[i] * inp[i];
    const rms = Math.sqrt(sum / inp.length);
    const db = rms > 1e-6 ? 20 * Math.log10(rms) : -120;

    this.floor += (db - this.floor) * (db < this.floor ? 0.05 : 0.0006);
    const thr = this.mode === "auto" ? Math.min(Math.max(this.floor + 10, -70), -20) : this.threshold;
    const holdBlocks = Math.round((0.25 * sampleRate) / inp.length);

    if (!this.enabled || this.mode === "open" || db > thr) this.hold = holdBlocks;
    else if (this.hold > 0) this.hold--;

    const target = this.hold > 0 ? 1 : 0;
    const coef = target > this.gain ? 0.02 : 0.0015;
    for (let i = 0; i < inp.length; i++) {
      this.gain += (target - this.gain) * coef;
      out[i] = inp[i] * this.gain;
    }
    if (++this.n % 8 === 0) this.port.postMessage({ level: db, open: this.hold > 0, threshold: thr });
    return true;
  }
}

registerProcessor("zeno-gate", ZenoGate);
