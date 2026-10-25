// Микрофон и звуки.
//
// Цепочка микрофона: захват с обработкой Chromium (шумоподавление,
// эхоподавление, автоусиление, изоляция голоса - что включено в настройках)
// -> громкость ввода -> шумовой гейт (gate-worklet.js) -> выключатель
// (микрофон выкл / push-to-talk) -> дорожка, которая уходит другу.

export const VOICE_DEFAULTS = {
  input: "default", output: "default", camera: "default",
  inputVolume: 100, outputVolume: 100, voiceVolume: 100, streamVolume: 100,
  ns: true, isolation: true, ec: true, agc: true,
  gate: true, gateAuto: true, threshold: -50,
  mode: "vad", pttKey: 0, pttLabel: "",
  voiceKbps: 64,
};

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
    return [cfg.input, cfg.ns, cfg.isolation, cfg.ec, cfg.agc].join("|");
  }

  async start(cfg) {
    this.stop();
    const audio = { echoCancellation: cfg.ec, noiseSuppression: cfg.ns, autoGainControl: cfg.agc, channelCount: 1, sampleRate: 48000 };
    if (supportsVoiceIsolation()) audio.voiceIsolation = cfg.ns && cfg.isolation;
    if (cfg.input && cfg.input !== "default") audio.deviceId = { exact: cfg.input };
    try {
      this.raw = await navigator.mediaDevices.getUserMedia({ audio });
    } catch (e) {
      if (!audio.deviceId) throw e;
      delete audio.deviceId; // выбранного микрофона нет - берем системный
      this.raw = await navigator.mediaDevices.getUserMedia({ audio });
    }
    this.ctx = new AudioContext({ latencyHint: "interactive", sampleRate: 48000 });
    await this.ctx.audioWorklet.addModule("gate-worklet.js");
    const src = this.ctx.createMediaStreamSource(this.raw);
    this.gain = this.ctx.createGain();
    this.gate = new AudioWorkletNode(this.ctx, "zeno-gate", { numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1] });
    this.out = this.ctx.createGain();
    this.dest = this.ctx.createMediaStreamDestination();
    this.dest.channelCount = 1;
    src.connect(this.gain).connect(this.gate).connect(this.out).connect(this.dest);
    this.gate.port.onmessage = (e) => {
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

  // apply - то, что меняется без перезахвата микрофона
  apply(cfg) {
    if (!this.ctx) return;
    this.gain.gain.value = Math.max(0, cfg.inputVolume) / 100;
    this.ptt = cfg.mode === "ptt";
    this.gate.port.postMessage({
      enabled: cfg.gate && !this.ptt,
      mode: this.ptt ? "open" : (cfg.gateAuto ? "auto" : "manual"),
      threshold: cfg.threshold,
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
    this.raw = this.track = this.ctx = null;
    this.gateOpen = false;
    this.level = -120;
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

  play(name) {
    if (!this.enabled) return;
    switch (name) {
      case "message": this.tone(880, 0, 0.09, 0.07); this.tone(1320, 0.08, 0.12, 0.06); break;
      case "join": this.tone(523, 0, 0.12); this.tone(784, 0.1, 0.18); break;
      case "leave": this.tone(784, 0, 0.12); this.tone(523, 0.1, 0.2); break;
      case "connect": this.tone([659, 988], 0, 0.18, 0.06); break;
      case "mute": this.tone(440, 0, 0.08, 0.08); break;
      case "unmute": this.tone(660, 0, 0.08, 0.08); break;
      case "stream": this.tone(587, 0, 0.1); this.tone(880, 0.09, 0.14); break;
    }
  }

  // ring: входящий звонок (incoming) или гудки исходящего; повторяется до stopRing
  ring(incoming) {
    this.stopRing();
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
  }
}
