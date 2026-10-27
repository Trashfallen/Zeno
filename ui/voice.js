// Микрофон и звуки.
//
// Цепочка микрофона: захват с обработкой Chromium (эхоподавление,
// автоусиление, стандартный шумодав и изоляция голоса - что включено)
// -> громкость ввода -> [DeepFilterNet3 (df-worklet.js), в режиме dfn]
// -> RNNoise и отсечка «не голоса» (voice-worklet.js)
// -> выключатель (микрофон выкл / push-to-talk) -> дорожка, которая уходит другу.
//
// В режиме dfn шум убирает DeepFilterNet, а RNNoise только оценивает, голос ли это.

export const VOICE_DEFAULTS = {
  input: "default", output: "default", camera: "default",
  inputVolume: 100, outputVolume: 100,
  nsMode: "rnnoise", // rnnoise (нейросеть, как Krisp) | dfn (DeepFilterNet3, чище и тяжелее) | standard (Chromium) | off
  dfnLevel: 100, // DeepFilterNet: на сколько дБ максимум глушить шум
  vad: true, // передавать только голос
  isolation: true, ec: true, agc: true,
  gate: true, gateAuto: true, threshold: -50,
  mode: "vad", pttKey: 0, pttLabel: "",
  voiceKbps: 64,
};

// wasm и модель DeepFilterNet грузятся один раз за запуск программы
let dfnAssets = null;

function loadDFN() {
  if (!dfnAssets) {
    const get = async (url) => {
      const r = await fetch(url);
      if (!r.ok) throw new Error(url + ": " + r.status);
      return r;
    };
    dfnAssets = Promise.all([
      // wasm лежит сжатым (16 МБ -> 4 МБ в exe)
      get("df/df_bg.wasm.gz").then((r) => new Response(r.body.pipeThrough(new DecompressionStream("gzip"))).arrayBuffer())
        .then((b) => WebAssembly.compile(b)),
      get("df/DeepFilterNet3_onnx.tar.gz").then((r) => r.arrayBuffer()),
    ]).then(([wasmModule, modelBytes]) => ({ wasmModule, modelBytes }));
    dfnAssets.catch(() => { dfnAssets = null; });
  }
  return dfnAssets;
}

export function supportsVoiceIsolation() {
  try { return !!navigator.mediaDevices.getSupportedConstraints().voiceIsolation; } catch (e) { return false; }
}

export class Mic extends EventTarget {
  constructor() {
    super();
    this.ctx = null;
    this.raw = null;
    this.track = null;
    this.level = -120;
    this.gateOpen = false;
    this.threshold = -50;
    this.muted = false;
    this.ptt = false;
    this.pttDown = false;
    this.cfgKey = "";
  }

  get live() { return !!this.track && this.track.readyState === "live"; }

  // transmitting - сейчас звук реально уходит (не выключен, PTT нажат)
  get transmitting() { return !this.muted && (!this.ptt || this.pttDown); }
  get speaking() { return this.transmitting && this.gateOpen; }

  static captureKey(cfg) {
    return [cfg.input, cfg.nsMode, cfg.isolation, cfg.ec, cfg.agc].join("|");
  }

  async start(cfg) {
    this.stop();
    // при RNNoise стандартный шумодав выключен: два шумодава подряд портят голос
    const standard = cfg.nsMode === "standard";
    const audio = { echoCancellation: cfg.ec, noiseSuppression: standard, autoGainControl: cfg.agc, channelCount: 1, sampleRate: 48000 };
    if (supportsVoiceIsolation()) audio.voiceIsolation = standard && cfg.isolation;
    if (cfg.input && cfg.input !== "default") audio.deviceId = { exact: cfg.input };
    try {
      this.raw = await navigator.mediaDevices.getUserMedia({ audio });
    } catch (e) {
      if (!audio.deviceId) throw e;
      delete audio.deviceId; // выбранного микрофона нет - берем системный
      this.raw = await navigator.mediaDevices.getUserMedia({ audio });
    }
    this.ctx = new AudioContext({ latencyHint: "interactive", sampleRate: 48000 });
    await this.ctx.audioWorklet.addModule("voice-worklet.js");
    const src = this.ctx.createMediaStreamSource(this.raw);
    this.gain = this.ctx.createGain();
    this.gate = new AudioWorkletNode(this.ctx, "zeno-voice", { numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1] });
    this.out = this.ctx.createGain();
    this.dest = this.ctx.createMediaStreamDestination();
    this.dest.channelCount = 1;
    this.dfn = null;
    this.dfnError = "";
    if (cfg.nsMode === "dfn") await this._startDFN(cfg);
    src.connect(this.gain);
    (this.dfn ? this.gain.connect(this.dfn) : this.gain).connect(this.gate).connect(this.out).connect(this.dest);
    this.gate.port.onmessage = (e) => {
      if (e.data.error) {
        console.warn("RNNoise не загрузился:", e.data.error);
        this.rnnoiseError = e.data.error;
        return;
      }
      this.voiceProb = e.data.prob;
      this.level = e.data.level;
      this.gateOpen = e.data.open;
      this.threshold = e.data.threshold;
      this.dispatchEvent(new CustomEvent("level"));
    };
    this.track = this.dest.stream.getAudioTracks()[0];
    this.cfgKey = Mic.captureKey(cfg);
    this.apply(cfg);
    if (this.ctx.state !== "running") await this.ctx.resume().catch(() => {});
    return this.track;
  }

  // _startDFN ставит DeepFilterNet; не вышло - шум убирает RNNoise (см. apply)
  async _startDFN(cfg) {
    const ctx = this.ctx;
    try {
      const a = await loadDFN();
      await ctx.audioWorklet.addModule("df-worklet.js");
      this.dfn = new AudioWorkletNode(ctx, "zeno-dfn", {
        numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1],
        processorOptions: { wasmModule: a.wasmModule, modelBytes: a.modelBytes, level: cfg.dfnLevel },
      });
      this.dfn.port.onmessage = (e) => {
        if (!e.data.error) return;
        console.warn("DeepFilterNet не запустился:", e.data.error);
        this.dfnError = e.data.error;
        if (this.cfg) this.apply(this.cfg);
      };
    } catch (e) {
      console.warn("DeepFilterNet не загрузился:", e);
      this.dfnError = String(e);
      this.dfn = null;
    }
  }

  // apply - то, что меняется без перезахвата микрофона
  apply(cfg) {
    if (!this.ctx) return;
    this.cfg = cfg;
    this.gain.gain.value = Math.max(0, cfg.inputVolume) / 100;
    this.ptt = cfg.mode === "ptt";
    if (this.dfn) this.dfn.port.postMessage({ level: cfg.dfnLevel });
    const dfnOK = !!this.dfn && !this.dfnError;
    this.gate.port.postMessage({
      enabled: cfg.gate && !this.ptt,
      mode: this.ptt ? "open" : (cfg.gateAuto ? "auto" : "manual"),
      threshold: cfg.threshold,
      denoise: cfg.nsMode === "rnnoise" || (cfg.nsMode === "dfn" && !dfnOK),
      vad: cfg.vad && !this.ptt,
    });
    this._updateOut();
  }

  setMuted(m) { this.muted = m; this._updateOut(); }
  setPTT(down) { this.pttDown = down; this._updateOut(); }

  _updateOut() {
    if (!this.ctx) return;
    this.out.gain.setTargetAtTime(this.transmitting ? 1 : 0, this.ctx.currentTime, 0.008);
  }

  stop() {
    if (this.raw) this.raw.getTracks().forEach((t) => t.stop());
    if (this.track) this.track.stop();
    if (this.ctx) this.ctx.close().catch(() => {});
    this.raw = this.track = this.ctx = this.dfn = null;
    this.gateOpen = false;
    this.level = -120;
  }
}

// Воспроизведение звука друга с громкостью 0-300%, как в Discord.
//
// До 100% играет обычный <audio> (громкость элемента): так работает
// эхоподавление. Выше 100% элемент глушится, а звук идет через усилитель
// Web Audio. Элемент со srcObject при этом нужен все равно: без него Chromium
// не отдает чужую WebRTC-дорожку в Web Audio.
export class Playback {
  constructor(el) {
    this.el = el;
    this.ctx = null;
    this.src = null;
    this.gain = null;
    this.track = null;
  }

  setTrack(track) {
    if (track === this.track) return;
    this.track = track;
    this.el.srcObject = track ? new MediaStream([track]) : null;
    if (this.src) { this.src.disconnect(); this.src = null; }
  }

  // level - 0..3 (1 = 100%)
  apply(level, muted, sink) {
    const sinkId = !sink || sink === "default" ? "" : sink;
    if (this.el.setSinkId && this.el.sinkId !== sinkId) this.el.setSinkId(sinkId).catch(() => {});
    const boost = !muted && level > 1 && this.track;
    if (!boost) {
      this.el.muted = muted;
      this.el.volume = Math.max(0, Math.min(1, level));
      if (this.gain) this.gain.gain.value = 0;
      return;
    }
    this.el.muted = true;
    if (!this.ctx) {
      this.ctx = new AudioContext({ latencyHint: "interactive" });
      this.gain = this.ctx.createGain();
      this.gain.connect(this.ctx.destination);
    }
    if (this.ctx.setSinkId && this.ctx.sinkId !== sinkId) this.ctx.setSinkId(sinkId).catch(() => {});
    if (!this.src && this.el.srcObject) {
      this.src = this.ctx.createMediaStreamSource(this.el.srcObject);
      this.src.connect(this.gain);
    }
    this.gain.gain.value = level;
    if (this.ctx.state !== "running") this.ctx.resume().catch(() => {});
  }

  stop() {
    this.setTrack(null);
    if (this.ctx) this.ctx.close().catch(() => {});
    this.ctx = this.gain = this.src = null;
  }
}

// Уровень громкости чужого звука - для зеленой рамки «говорит».
export class LevelWatcher {
  constructor() {
    this.ctx = null;
    this.an = null;
    this.buf = null;
  }

  attach(track) {
    this.detach();
    if (!track) return;
    this.ctx = new AudioContext();
    const src = this.ctx.createMediaStreamSource(new MediaStream([track]));
    this.an = this.ctx.createAnalyser();
    this.an.fftSize = 512;
    this.buf = new Float32Array(this.an.fftSize);
    src.connect(this.an);
  }

  level() {
    if (!this.an) return -120;
    this.an.getFloatTimeDomainData(this.buf);
    let s = 0;
    for (const v of this.buf) s += v * v;
    const rms = Math.sqrt(s / this.buf.length);
    return rms > 1e-6 ? 20 * Math.log10(rms) : -120;
  }

  detach() {
    if (this.ctx) this.ctx.close().catch(() => {});
    this.ctx = this.an = null;
  }
}

// Звуки интерфейса синтезируются на лету - без файлов.
export class Sfx {
  constructor() {
    this.ctx = null;
    this.enabled = true;
    this.sink = "default";
    this.ringTimer = null;
  }

  _ctx() {
    if (!this.ctx) this.ctx = new AudioContext();
    if (this.ctx.state !== "running") this.ctx.resume().catch(() => {});
    return this.ctx;
  }

  setSink(id) {
    this.sink = id || "default";
    const c = this._ctx();
    if (c.setSinkId) c.setSinkId(this.sink === "default" ? "" : this.sink).catch(() => {});
  }

  tone(freqs, start, dur, vol = 0.12, type = "sine") {
    const c = this._ctx();
    const t0 = c.currentTime + start;
    for (const f of [].concat(freqs)) {
      const o = c.createOscillator();
      const g = c.createGain();
      o.type = type;
      o.frequency.value = f;
      g.gain.setValueAtTime(0, t0);
      g.gain.linearRampToValueAtTime(vol, t0 + 0.015);
      g.gain.setValueAtTime(vol, t0 + dur - 0.05);
      g.gain.linearRampToValueAtTime(0, t0 + dur);
      o.connect(g).connect(c.destination);
      o.start(t0);
      o.stop(t0 + dur + 0.02);
    }
  }

  // playFile играет звук из ui/ не дольше maxSec с коротким затуханием в конце;
  // файл декодируется один раз. Не вышло - fallback (синтезатор).
  playFile(url, maxSec, fallback) {
    const c = this._ctx();
    if (!this.buffers) this.buffers = new Map();
    if (!this.buffers.has(url)) {
      this.buffers.set(url, fetch(url).then((r) => r.arrayBuffer()).then((b) => c.decodeAudioData(b)));
    }
    this.buffers.get(url).then((buf) => {
      const dur = Math.min(buf.duration, maxSec);
      const t = c.currentTime;
      const src = c.createBufferSource();
      const g = c.createGain();
      src.buffer = buf;
      g.gain.setValueAtTime(0.8, t);
      g.gain.setValueAtTime(0.8, t + dur - 0.06);
      g.gain.linearRampToValueAtTime(0, t + dur);
      src.connect(g).connect(c.destination);
      src.start(t, 0, dur);
    }).catch(() => {
      this.buffers.delete(url);
      if (fallback) fallback();
    });
  }

  play(name) {
    if (!this.enabled) return;
    switch (name) {
      case "message": this.playFile("message.mp3", 1.0, () => { this.tone(880, 0, 0.09, 0.07); this.tone(1320, 0.08, 0.12, 0.06); }); break;
      case "join": this.tone(523, 0, 0.12); this.tone(784, 0.1, 0.18); break;
      case "leave": this.tone(784, 0, 0.12); this.tone(523, 0.1, 0.2); break;
      case "connect": this.tone([659, 988], 0, 0.18, 0.06); break;
      case "mute": this.tone(440, 0, 0.08, 0.08); break;
      case "unmute": this.tone(660, 0, 0.08, 0.08); break;
      case "stream": this.tone(587, 0, 0.1); this.tone(880, 0.09, 0.14); break;
    }
  }

  // ring: входящий звонок (мелодия ring.mp3 по кругу) или гудки исходящего; играет до stopRing
  ring(incoming) {
    this.stopRing();
    if (incoming) {
      const a = new Audio("ring.mp3");
      a.loop = true;
      a.volume = 0.8;
      if (a.setSinkId && this.sink !== "default") a.setSinkId(this.sink).catch(() => {});
      this.ringAudio = a;
      // мелодия не запустилась (нет файла, нет устройства) - звоним синтезатором
      a.play().catch(() => {
        if (this.ringAudio !== a) return;
        this.ringAudio = null;
        this._ringTones(true);
      });
      return;
    }
    this._ringTones(false);
  }

  _ringTones(incoming) {
    const once = () => {
      if (incoming) {
        this.tone([784, 988], 0, 0.35, 0.1, "triangle");
        this.tone([784, 988], 0.45, 0.35, 0.1, "triangle");
      } else {
        this.tone(425, 0, 1.0, 0.06);
      }
    };
    once();
    this.ringTimer = setInterval(once, incoming ? 2200 : 4000);
  }

  stopRing() {
    clearInterval(this.ringTimer);
    this.ringTimer = null;
    if (this.ringAudio) {
      this.ringAudio.pause();
      this.ringAudio.src = "";
      this.ringAudio = null;
    }
  }
}
