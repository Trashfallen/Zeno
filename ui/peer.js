// Соединение с одним другом: WebRTC напрямую между ПК.
//
// Сначала поднимается только канал данных (SDP маленький - код влезает в
// сообщение мессенджера). Как только канал открылся, тот, кто делал
// предложение, добавляет четыре медиа-слота (микрофон, камера, экран, звук
// экрана) и пересогласует соединение уже по этому каналу. Дальше звонок и
// трансляция только подменяют дорожки в слотах - без новых кодов.

import { api } from "./api.js";

export const SLOTS = ["mic", "cam", "screen", "sys"];

export const DEFAULT_STUN = [
  "stun:stun.l.google.com:19302",
  "stun:stun1.l.google.com:19302",
  "stun:stun.cloudflare.com:3478",
  "stun:stun.sipnet.ru:3478",
];

export const SCREEN_PRESETS = {
  "720p30": { w: 1280, h: 720, fps: 30, kbps: 3500, label: "720p, 30 к/с" },
  "1080p30": { w: 1920, h: 1080, fps: 30, kbps: 6000, label: "1080p, 30 к/с" },
  "1080p60": { w: 1920, h: 1080, fps: 60, kbps: 10000, label: "1080p, 60 к/с" },
  "1440p60": { w: 2560, h: 1440, fps: 60, kbps: 16000, label: "1440p, 60 к/с" },
  "source60": { w: 7680, h: 4320, fps: 60, kbps: 20000, label: "Исходное, 60 к/с" },
};

function gathered(pc, timeout = 4000) {
  return new Promise((resolve) => {
    if (pc.iceGatheringState === "complete") return resolve();
    const done = () => {
      if (pc.iceGatheringState === "complete") {
        pc.removeEventListener("icegatheringstatechange", done);
        resolve();
      }
    };
    pc.addEventListener("icegatheringstatechange", done);
    setTimeout(resolve, timeout);
  });
}

// tuneSDP настраивает Opus по m-секциям: первая аудио - голос (моно, FEC,
// битрейт из настроек), вторая - звук трансляции (стерео 128 кбит/с).
export function tuneSDP(sdp, voiceKbps) {
  const parts = sdp.split(/(?=^m=)/m);
  let audioIdx = 0;
  return parts.map((sec) => {
    if (!sec.startsWith("m=audio")) return sec;
    const which = audioIdx++;
    const m = sec.match(/a=rtpmap:(\d+) opus\/48000/i);
    if (!m) return sec;
    const pt = m[1];
    const want = which === 0
      ? { useinbandfec: "1", usedtx: "0", stereo: "0", "sprop-stereo": "0", maxaveragebitrate: String(voiceKbps * 1000) }
      : { useinbandfec: "1", usedtx: "0", stereo: "1", "sprop-stereo": "1", maxaveragebitrate: "128000" };
    const re = new RegExp("a=fmtp:" + pt + " ([^\\r\\n]*)");
    if (!re.test(sec)) return sec;
    return sec.replace(re, (_, params) => {
      const p = {};
      for (const kv of params.split(";")) {
        const [k, v] = kv.trim().split("=");
        if (k) p[k] = v;
      }
      Object.assign(p, want);
      return "a=fmtp:" + pt + " " + Object.entries(p).map(([k, v]) => k + "=" + v).join(";");
    });
  }).join("");
}

export class Peer extends EventTarget {
  // opts: { iceServers(), voiceKbps(), codec() }
  constructor(friend, opts) {
    super();
    this.friend = friend;
    this.opts = opts;
    this.pc = null;
    this.dc = null;
    this.role = null;
    this.nonce = null;
    this.state = "offline"; // offline | offer | answer | connecting | connected | unstable
    this.tx = {};
    this.mediaReady = false;
    this.pending = {};
    this.negotiating = false;
    this.again = false;
  }

  emit(type, detail) { this.dispatchEvent(new CustomEvent(type, { detail })); }

  _setState(s) {
    if (this.state === s) return;
    this.state = s;
    this.emit("state", s);
  }

  get connected() { return this.state === "connected" || this.state === "unstable"; }

  _newPC() {
    const pc = new RTCPeerConnection({ iceServers: this.opts.iceServers(), bundlePolicy: "max-bundle", rtcpMuxPolicy: "require" });
    pc.onconnectionstatechange = () => {
      if (pc !== this.pc) return;
      const s = pc.connectionState;
      if (s === "failed" || s === "closed") this._lost();
      else if (s === "disconnected" && this.connected) this._setState("unstable");
      else if (s === "connected" && this.state === "unstable") this._setState("connected");
    };
    pc.onicecandidate = (e) => {
      if (e.candidate && this.dc && this.dc.readyState === "open") this.send({ k: "ice", c: e.candidate.toJSON() });
    };
    this.pc = pc;
    return pc;
  }

  // ---------- коды ----------

  async makeOffer() {
    this.close(true);
    this.role = "offerer";
    const pc = this._newPC();
    this._bindDC(pc.createDataChannel("zeno", { ordered: true }));
    await pc.setLocalDescription(await pc.createOffer());
    await gathered(pc);
    const r = await api("invite/make", { type: "offer", to: this.friend.id, sdp: pc.localDescription.sdp });
    this.nonce = r.nonce;
    this._setState("offer");
    return r.code;
  }

  async acceptOffer(inv) {
    this.close(true);
    this.role = "answerer";
    const pc = this._newPC();
    pc.ondatachannel = (e) => this._bindDC(e.channel);
    await pc.setRemoteDescription({ type: "offer", sdp: inv.sdp });
    await pc.setLocalDescription(await pc.createAnswer());
    await gathered(pc);
    const r = await api("invite/make", { type: "answer", to: this.friend.id, nonce: inv.nonce, sdp: pc.localDescription.sdp });
    this._setState("answer");
    return r.code;
  }

  async acceptAnswer(inv) {
    if (this.role !== "offerer" || !this.pc || this.pc.signalingState !== "have-local-offer") {
      throw new Error("Сначала создайте свой код для этого друга, ответ приходит на него");
    }
    if (inv.nonce !== this.nonce) throw new Error("Это ответ на старый код. Попросите друга вставить ваш последний код");
    await this.pc.setRemoteDescription({ type: "answer", sdp: inv.sdp });
    this._setState("connecting");
  }

  // ---------- канал данных ----------

  _bindDC(dc) {
    this.dc = dc;
    dc.onopen = () => {
      if (dc !== this.dc) return;
      this._setState("connected");
      if (this.role === "offerer") this._addMedia();
    };
    dc.onclose = () => { if (dc === this.dc) this._lost(); };
    dc.onmessage = (e) => {
      let m;
      try { m = JSON.parse(e.data); } catch (err) { return; }
      if (m.k === "sdp") this._onSDP(m.d).catch((err) => console.warn("sdp", err));
      else if (m.k === "ice") this.pc && this.pc.addIceCandidate(m.c).catch(() => {});
      else this.emit("message", m);
    };
  }

  send(obj) {
    if (!this.dc || this.dc.readyState !== "open") return false;
    this.dc.send(JSON.stringify(obj));
    return true;
  }

  // ---------- медиа ----------

  _addMedia() {
    const pc = this.pc;
    pc.onnegotiationneeded = () => this._renegotiate();
    for (const slot of SLOTS) {
      this.tx[slot] = pc.addTransceiver(slot === "mic" || slot === "sys" ? "audio" : "video", { direction: "sendrecv" });
    }
    this._codecPrefs();
  }

  _codecPrefs() {
    const want = { h264: "video/H264", vp9: "video/VP9", av1: "video/AV1", vp8: "video/VP8" }[this.opts.codec()];
    if (!want || !window.RTCRtpReceiver || !RTCRtpReceiver.getCapabilities) return;
    const caps = (RTCRtpReceiver.getCapabilities("video") || {}).codecs || [];
    const first = caps.filter((c) => c.mimeType === want);
    if (!first.length) return;
    const list = [...first, ...caps.filter((c) => c.mimeType !== want)];
    for (const s of ["cam", "screen"]) {
      try { this.tx[s] && this.tx[s].setCodecPreferences(list); } catch (e) { /* старый движок */ }
    }
  }

  async _renegotiate() {
    if (this.role !== "offerer" || !this.dc || this.dc.readyState !== "open") return;
    if (this.negotiating) { this.again = true; return; }
    this.negotiating = true;
    try {
      const offer = await this.pc.createOffer();
      const sdp = tuneSDP(offer.sdp, this.opts.voiceKbps());
      await this.pc.setLocalDescription({ type: "offer", sdp });
      this.send({ k: "sdp", d: { type: "offer", sdp } });
    } catch (e) {
      this.negotiating = false;
      console.warn("renegotiate", e);
    }
  }

  async _onSDP(d) {
    const pc = this.pc;
    if (!pc) return;
    if (d.type === "offer") {
      await pc.setRemoteDescription(d);
      const trs = pc.getTransceivers().filter((t) => t.mid !== null);
      SLOTS.forEach((slot, i) => {
        if (trs[i]) {
          this.tx[slot] = trs[i];
          trs[i].direction = "sendrecv";
        }
      });
      this._codecPrefs();
      const answer = await pc.createAnswer();
      const sdp = tuneSDP(answer.sdp, this.opts.voiceKbps());
      await pc.setLocalDescription({ type: "answer", sdp });
      this.send({ k: "sdp", d: { type: "answer", sdp } });
      this._mediaUp();
    } else if (d.type === "answer") {
      await pc.setRemoteDescription(d);
      this.negotiating = false;
      this._mediaUp();
      if (this.again) {
        this.again = false;
        this._renegotiate();
      }
    }
  }

  async _mediaUp() {
    if (this.mediaReady || !SLOTS.every((s) => this.tx[s])) return;
    this.mediaReady = true;
    for (const [slot, track] of Object.entries(this.pending)) {
      await this.tx[slot].sender.replaceTrack(track).catch(() => {});
    }
    this.pending = {};
    this.emit("media");
  }

  remoteTrack(slot) {
    const t = this.tx[slot];
    return t && t.receiver ? t.receiver.track : null;
  }

  async setTrack(slot, track) {
    if (!this.mediaReady) {
      this.pending[slot] = track;
      return;
    }
    await this.tx[slot].sender.replaceTrack(track);
  }

  // tuneSender: битрейт, частота кадров, масштаб, приоритет, что жертвовать при нехватке канала
  async tuneSender(slot, enc, degradation) {
    const t = this.tx[slot];
    if (!t) return;
    const p = t.sender.getParameters();
    if (!p.encodings || !p.encodings.length) p.encodings = [{}];
    Object.assign(p.encodings[0], enc);
    if (degradation) p.degradationPreference = degradation;
    try { await t.sender.setParameters(p); } catch (e) { console.warn("setParameters", slot, e); }
  }

  // ---------- статистика ----------

  async stats() {
    if (!this.pc) return null;
    const r = await this.pc.getStats();
    const out = { rtt: null, route: null, out: {}, in: {}, codecs: {} };
    let pair = null;
    r.forEach((s) => {
      if (s.type === "transport" && s.selectedCandidatePairId) pair = r.get(s.selectedCandidatePairId);
    });
    if (!pair) r.forEach((s) => { if (s.type === "candidate-pair" && s.nominated && s.state === "succeeded") pair = s; });
    if (pair) {
      if (pair.currentRoundTripTime != null) out.rtt = Math.round(pair.currentRoundTripTime * 1000);
      const l = r.get(pair.localCandidateId);
      const rm = r.get(pair.remoteCandidateId);
      const types = [l && l.candidateType, rm && rm.candidateType];
      out.route = types.includes("relay") ? "relay" : (types[0] === "host" && types[1] === "host" ? "lan" : "direct");
      out.availOut = pair.availableOutgoingBitrate || null;
    }
    const midSlot = {};
    for (const [k, t] of Object.entries(this.tx)) if (t.mid !== null) midSlot[t.mid] = k;
    r.forEach((s) => {
      if (s.type === "outbound-rtp" && midSlot[s.mid]) out.out[midSlot[s.mid]] = s;
      if (s.type === "inbound-rtp" && midSlot[s.mid]) out.in[midSlot[s.mid]] = s;
      if (s.type === "codec") out.codecs[s.id] = s.mimeType;
    });
    return out;
  }

  // ---------- конец ----------

  _lost() {
    const was = this.connected;
    this.close(true);
    if (was) this.emit("lost");
  }

  close(silent) {
    if (this.dc) { this.dc.onclose = null; try { this.dc.close(); } catch (e) { /* уже закрыт */ } }
    if (this.pc) { this.pc.onconnectionstatechange = null; try { this.pc.close(); } catch (e) { /* уже закрыт */ } }
    this.pc = this.dc = null;
    this.tx = {};
    this.mediaReady = false;
    this.pending = {};
    this.negotiating = this.again = false;
    this.role = null;
    this._setState("offline");
    if (!silent) this.emit("lost");
  }
}
