// Шумодав DPDFNet (режим «DPDFNet» в настройках) - отдельный поток.
//
// DPDFNet2 48 кГц (Ceva, модель из github.com/k2-fsa/sherpa-onnx) - потомок
// DeepFilterNet с двухпутевыми RNN-блоками. Работает в onnxruntime-web
// (wasm, один поток), а не прямо в аудиопотоке: вызов модели асинхронный и
// занимает несколько миллисекунд, аудиопоток на это время вставать не должен.
//
// Кадры по 10 мс (480 отсчетов) приходят из dpdf-worklet.js через MessagePort.
// Здесь окно Vorbis 960, спектр 481 полоса, модель, обратный спектр,
// сложение с перекрытием - так же, как в sherpa-onnx (online-speech-denoiser).
// Сила: lim = 10^(-дБ/20), спектр = чистый * (1 - lim) + исходный * lim.
// Исходный спектр берется на 4 кадра раньше: выход модели отстает от входа на
// 40 мс, без сдвига смесь звучит гребенкой.
//
// Концы фраз: модель давит сильно и съедает тихие последние согласные.
// 150 мс после конца речи (речь - по громкости очищенного звука) подмешивается
// половина исходного. Начало так не защитить: опережения тут нет.

import * as ort from "./dpdf/ort.wasm.bundle.min.mjs";

const N = 960, H = 480, BINS = N / 2 + 1;

const gunzip = async (url) => {
  const r = await fetch(url);
  if (!r.ok) throw new Error(url + ": " + r.status);
  return new Uint8Array(await new Response(r.body.pipeThrough(new DecompressionStream("gzip"))).arrayBuffer());
};

// таблицы DFT на 960 точек (не степень двойки) - прямой и обратный проход матрицей
const cosT = new Float32Array(BINS * N), sinT = new Float32Array(BINS * N);
for (let k = 0; k < BINS; k++) {
  for (let n = 0; n < N; n++) {
    const a = (2 * Math.PI * ((k * n) % N)) / N;
    cosT[k * N + n] = Math.cos(a);
    sinT[k * N + n] = Math.sin(a);
  }
}
const win = new Float32Array(N);
for (let i = 0; i < N; i++) {
  const s = Math.sin((0.5 * Math.PI * (i + 0.5)) / (N / 2));
  win[i] = Math.sin(0.5 * Math.PI * s * s);
}

let sess = null, state = null, port = null, lim = 0;
const ana = new Float32Array(N), ola = new Float32Array(N), xw = new Float32Array(N), y = new Float32Array(N);
const spec = new Float32Array(BINS * 2);
let busy = Promise.resolve();
const DELAY = 4; // кадров: на столько выход модели отстает от входа
const hist = []; // исходные спектры за DELAY кадров
const EDGE = 15, EDGE_MIX = 0.5; // защита конца фразы: 150 мс, половина исходного
let voiceLvl = -60, run = 0, since = 99, edge = 0;

async function init(level) {
  ort.env.wasm.numThreads = 1;
  ort.env.wasm.proxy = false;
  const [wasm, model, meta] = await Promise.all([
    gunzip("dpdf/ort-wasm-simd-threaded.wasm.gz"),
    gunzip("dpdf/dpdfnet2_48khz_hr.onnx.gz"),
    fetch("dpdf/dpdf-init.json").then((r) => r.json()),
  ]);
  ort.env.wasm.wasmBinary = wasm;
  sess = await ort.InferenceSession.create(model, { executionProviders: ["wasm"], graphOptimizationLevel: "all" });
  const st = new Float32Array(meta.stateSize);
  st.set(meta.erb, 0);
  st.set(meta.spec, meta.erb.length);
  state = new ort.Tensor("float32", st, [meta.stateSize]);
  setLevel(level);
}

function setLevel(db) {
  db = Math.max(0, Math.min(100, db ?? 100));
  lim = db >= 100 ? 0 : Math.pow(10, -db / 20);
}

async function hop(x) {
  ana.copyWithin(0, H);
  ana.set(x, N - H);
  for (let i = 0; i < N; i++) xw[i] = ana[i] * win[i];
  for (let k = 0; k < BINS; k++) {
    let re = 0, im = 0;
    const o = k * N;
    for (let n = 0; n < N; n++) { const v = xw[n]; re += v * cosT[o + n]; im -= v * sinT[o + n]; }
    spec[2 * k] = re;
    spec[2 * k + 1] = im;
  }
  const out = await sess.run({ spec: new ort.Tensor("float32", spec.slice(), [1, 1, BINS, 2]), state_in: state });
  state = out.state_out;
  const e = out.spec_e.data;
  hist.push(spec.slice());
  if (hist.length > DELAY + 1) hist.shift();
  const xs = hist[0];
  // громкость очищенного кадра (по Парсевалю, как средний квадрат отсчетов)
  let p = e[0] * e[0] + e[2 * (BINS - 1)] ** 2;
  for (let k = 1; k < BINS - 1; k++) p += 2 * (e[2 * k] ** 2 + e[2 * k + 1] ** 2);
  const den = p > 0 ? 10 * Math.log10(p / (N * N)) : -120;
  voiceLvl = Math.max(den, voiceLvl - 0.02);
  run = den > -70 && den > voiceLvl - 25 ? run + 1 : 0;
  since = run >= 2 ? 0 : since + 1;
  edge += ((since > 0 && since <= EDGE ? EDGE_MIX : 0) - edge) * 0.5;
  const m = Math.max(lim, edge);
  if (m > 0.001) for (let i = 0; i < BINS * 2; i++) e[i] = e[i] * (1 - m) + xs[i] * m;
  // обратный DFT вещественного сигнала
  for (let n = 0; n < N; n++) {
    let s = e[0] + e[2 * (BINS - 1)] * (n & 1 ? -1 : 1);
    for (let k = 1; k < BINS - 1; k++) {
      const t = k * N + n; // cos/sin симметричны по k*n
      s += 2 * (e[2 * k] * cosT[t] - e[2 * k + 1] * sinT[t]);
    }
    y[n] = s / N;
  }
  ola.copyWithin(0, H);
  ola.fill(0, N - H);
  for (let i = 0; i < N; i++) ola[i] += y[i] * win[i];
  return ola.slice(0, H);
}

self.onmessage = (e) => {
  const d = e.data;
  if (d.port) {
    port = d.port;
    port.onmessage = (m) => {
      const x = m.data;
      if (!(x instanceof Float32Array) || !sess) return;
      // кадры строго по очереди: состояние модели переходит от кадра к кадру
      busy = busy.then(() => hop(x)).then((r) => port.postMessage(r, [r.buffer]))
        .catch((err) => self.postMessage({ error: String(err) }));
    };
    init(d.level).then(() => self.postMessage({ ready: true }), (err) => self.postMessage({ error: String(err) }));
  }
  if (typeof d.level === "number" && !d.port) setLevel(d.level);
};
