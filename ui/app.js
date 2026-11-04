// Zeno: интерфейс. Вход, друзья, чат, звонок, трансляция экрана, настройки.

import { api, $, $$, h, esc, copyText, readClipboard, icon, avatar, hhmm, dayLabel, duration } from "./api.js";
import { Mic, LevelWatcher, Playback, Sfx, VOICE_DEFAULTS, supportsVoiceIsolation } from "./voice.js";
import { Peer, SLOTS, DEFAULT_STUN, SCREEN_PRESETS, isOldDefaultStun, gathered } from "./peer.js";

const MEDIA_DEFAULTS = {
  ...VOICE_DEFAULTS,
  screenPreset: "1080p60", screenMode: "motion", screenAudio: true, captureMode: "native", codec: "h264", camRes: "720p",
  stun: "", turnUrl: "", turnUser: "", turnPass: "", // stun пусто - выбор программы (stun.go)
  sounds: true, notifyMessages: true,
  stageHeight: 0, // высота окна звонка, 0 - по умолчанию
  vol: {}, // громкость друзей: { id: { voice, stream, voiceMuted, streamMuted } }, 0-300%
  lanOn: [], // с кем запущен virtualZeno (локальная сеть)
};
// шумодавы на выбор: значение, название, пояснение
const NS_MODES = [
  ["rnnoise", "RNNoise (рекомендуется)", "Нейросеть RNNoise убирает вентиляторы, улицу, клавиатуру и чавканье - как Krisp в Discord. Почти не грузит процессор."],
  ["dfn", "DeepFilterNet3 - чище, тяжелее", "Нейросеть DeepFilterNet3: чище RNNoise, меньше «подводного» звука в голосе. Сильнее грузит процессор, задержка голоса больше на ~40 мс."],
  ["standard", "Стандартное (Chromium)", "Встроенный шумодав браузера: слабее нейросетей, зато работает с изоляцией голоса."],
  ["off", "Выключено", "Без шумодава. Так же выбирают внешний: NVIDIA Broadcast, Krisp и т.п. - выберите их микрофон выше."],
];
const COLORS = ["#5865f2", "#3ba55c", "#faa61a", "#ed4245", "#eb459e", "#9b59b6", "#1abc9c", "#e67e22"];
const SPEAK_DB = -50;

const S = {
  version: "", me: null, friends: [], settings: {}, media: { ...MEDIA_DEFAULTS }, autostart: false, update: null,
  view: "home", homeTab: "all", sel: null,
  peers: new Map(), msgs: new Map(), loaded: new Set(), unread: new Map(), typing: new Map(), remote: new Map(),
  call: null, cam: null, screen: null, focus: null, showStats: false, muted: false, deaf: false,
  rtt: null, route: null, connectFor: null, full: false, showStrip: false,
};

const mic = new Mic();
const sfx = new Sfx();
const remoteLevel = new LevelWatcher();
const pbVoice = new Playback($("#aVoice"));
const pbSys = new Playback($("#aSys"));
const videos = new Map(); // ключ плитки -> <video>, чтобы перерисовка не сбрасывала картинку

const fr = (id) => S.friends.find((f) => f.id === id);
const peerOf = (id) => S.peers.get(id);
const peerState = (id) => (peerOf(id) ? peerOf(id).state : "offline");
const isConnected = (id) => !!peerOf(id) && peerOf(id).connected;

// ---------- уведомления на экране ----------

function toast(text, kind = "") {
  const el = h("div", { class: "toast " + kind }, text);
  $("#toasts").append(el);
  setTimeout(() => el.remove(), kind === "err" ? 6000 : 3500);
}

function fail(e) {
  console.error(e);
  toast(e && e.message ? e.message : String(e), "err");
}

// ---------- запуск ----------

function applyState(st) {
  S.version = st.version;
  S.settings = st.settings || {};
  S.media = { ...MEDIA_DEFAULTS, ...(S.settings.media || {}) };
  if (S.settings.media && S.settings.media.ns === false && !S.settings.media.nsMode) S.media.nsMode = "off"; // старая настройка
  delete S.media.ns;
  if (isOldDefaultStun(S.media.stun)) S.media.stun = ""; // старый список по умолчанию - теперь автовыбор
  S.me = st.account;
  S.friends = st.friends || [];
  S.autostart = st.autostart;
  S.update = st.update;
  sfx.enabled = S.media.sounds;
}

async function boot() {
  const st = await api("state");
  applyState(st);
  if (!S.me) showLogin(S.settings.last_login || "");
  else if (!S.me.nick) showNick();
  else showApp();
}

// ---------- вход и ник ----------

function showLogin(last) {
  $("#appScreen").hidden = true;
  $("#authScreen").hidden = false;
  $("#loginForm").hidden = false;
  $("#nickForm").hidden = true;
  $("#loginName").value = last;
  (last ? $("#loginPass") : $("#loginName")).focus();
}

$("#loginForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const btn = $("#loginBtn");
  btn.disabled = true;
  btn.textContent = "Проверяю…";
  $("#loginError").textContent = "";
  try {
    applyState(await api("login", { login: $("#loginName").value, password: $("#loginPass").value }));
    $("#loginPass").value = "";
    if (!S.me.nick) showNick();
    else showApp();
  } catch (err) {
    $("#loginError").textContent = err.message;
  } finally {
    btn.disabled = false;
    btn.textContent = "Войти";
  }
});

let nickColor = null;

function showNick() {
  $("#appScreen").hidden = true;
  $("#authScreen").hidden = false;
  $("#loginForm").hidden = true;
  $("#nickForm").hidden = false;
  nickColor = S.me.color;
  const input = $("#nickInput");
  input.value = "";
  const draw = () => {
    $("#nickPreview").replaceChildren(avatar({ nick: input.value || S.me.login, color: nickColor }, 84));
    $("#nickColors").replaceChildren(...COLORS.map((c) => h("button", {
      type: "button", class: "swatch" + (c === nickColor ? " on" : ""), style: { background: c },
      onclick: () => { nickColor = c; draw(); },
    })));
  };
  input.oninput = draw;
  draw();
  input.focus();
}

$("#nickForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  $("#nickError").textContent = "";
  try {
    applyState(await api("profile", { nick: $("#nickInput").value, color: nickColor }));
    showApp();
  } catch (err) {
    $("#nickError").textContent = err.message;
  }
});

// ---------- основное окно ----------

let appStarted = false;

function showApp() {
  $("#authScreen").hidden = true;
  $("#appScreen").hidden = false;
  if (!appStarted) {
    appStarted = true;
    staticIcons();
    sfx.setSink(S.media.output);
    syncPTT();
    setInterval(fastTick, 100);
    setInterval(statsTick, 1000);
    setInterval(refreshUpdate, 10 * 60 * 1000);
    refreshStun();
    setInterval(refreshStun, 2 * 60 * 1000);
    autoLoop();
    if (S.media.lanOn.length) lanWant(); // virtualZeno был запущен до выхода - поднимаем сеть снова
    if (!S.friends.length) S.homeTab = "add";
  }
  renderAll();
}

function staticIcons() {
  $("#sideAdd").innerHTML = icon("plus", 16);
  $("#navFriendsIco").innerHTML = icon("users", 22);
  $("#homeIco").innerHTML = icon("users", 22);
  $("#homePaste").innerHTML = icon("paste", 18) + "Ввести код";
  $("#ubSettings").innerHTML = icon("gear", 20);
  $("#dmCall").innerHTML = icon("phone", 20);
  $("#dmVideo").innerHTML = icon("video", 22);
  $("#dmLink").innerHTML = icon("link", 20);
  $("#dmApps").innerHTML = icon("apps", 20);
  $("#attachBtn").innerHTML = icon("clip", 20);
  $("#sbApps").innerHTML = icon("apps", 20);
  $("#sendBtn").innerHTML = icon("send", 20);
  $("#vpHang").innerHTML = icon("hang", 22);
  $("#sbHang").innerHTML = icon("hang", 24);
  $("#sbMore").innerHTML = icon("dots", 20);
  $("#sbMicOpts").innerHTML = icon("down", 14);
  $("#sbCamOpts").innerHTML = icon("down", 14);
  $("#sbStats").innerHTML = icon("info", 20);
  $("#sbVolume").innerHTML = icon("volume", 20);
}

function renderAll() {
  if (!S.me) return;
  renderSide();
  renderUserbar();
  renderVoicePanel();
  renderUpdate();
  $("#homeView").hidden = S.view !== "home";
  $("#dmView").hidden = S.view !== "dm";
  if (S.view === "home") renderHome();
  else renderDM();
}

function statusText(st) {
  return {
    connected: "в сети", unstable: "связь нестабильна", offer: "соединение…",
    answer: "соединение…", connecting: "соединение…", offline: S.netOnline === 0 ? "нет связи с серверами" : "не в сети",
  }[st] || "не в сети";
}

function dotClass(st) {
  if (st === "connected" || st === "unstable") return "online";
  if (st === "offer" || st === "answer" || st === "connecting") return "pending";
  return "";
}

function avatarWithDot(person, size, st) {
  return h("div", { class: "av-wrap" }, avatar(person, size), h("span", { class: "dot " + dotClass(st) }));
}

function renderSide() {
  $("#navFriends").classList.toggle("active", S.view === "home");
  $("#dmList").replaceChildren(...S.friends.map((f) => {
    const st = peerState(f.id);
    const n = S.unread.get(f.id) || 0;
    const inCall = S.call && S.call.id === f.id;
    const sub = inCall
      ? h("div", { class: "dm-sub in-call" }, S.call.state === "active" ? "в звонке" : S.call.state === "reconnecting" ? "переподключение…" : "вызов…")
      : h("div", { class: "dm-sub" }, statusText(st));
    return h("button", {
      class: "dm" + (S.view === "dm" && S.sel === f.id ? " active" : "") + (n ? " unread" : ""),
      onclick: () => openDM(f.id),
      oncontextmenu: (e) => userMenu(e, f.id),
    }, avatarWithDot(f, 32, st), h("div", { class: "dm-text" }, h("div", { class: "dm-name" }, f.nick), sub), n ? h("span", { class: "badge" }, n) : null);
  }));
}

function renderUserbar() {
  const sub = S.deaf ? "звук выключен" : S.muted ? "микрофон выключен" : "@" + S.me.login;
  $("#ubMe").replaceChildren(
    h("div", { class: "av-wrap" }, avatar(S.me, 32, "me-av"), h("span", { class: "dot online" })),
    h("div", { class: "ub-names" }, h("div", { class: "ub-nick" }, S.me.nick), h("div", { class: "ub-login" }, sub)),
  );
  const micBtn = $("#ubMic");
  micBtn.innerHTML = icon(S.muted || S.deaf ? "micOff" : "mic", 20);
  micBtn.classList.toggle("off", S.muted || S.deaf);
  micBtn.title = S.muted ? "Включить микрофон" : "Выключить микрофон";
  const deafBtn = $("#ubDeaf");
  deafBtn.innerHTML = icon(S.deaf ? "headOff" : "head", 20);
  deafBtn.classList.toggle("off", S.deaf);
  deafBtn.title = S.deaf ? "Включить звук" : "Выключить звук";
}

function renderVoicePanel() {
  const c = S.call;
  $("#voicePanel").hidden = !c;
  if (!c) return;
  const f = fr(c.id);
  const unstable = peerState(c.id) === "unstable";
  const title = $("#vpTitle");
  title.textContent = c.state === "active" ? (unstable ? "Связь нестабильна" : "Голосовая связь подключена")
    : c.state === "incoming" ? "Входящий звонок" : c.state === "reconnecting" ? "Переподключение…" : "Вызов…";
  title.classList.toggle("warn", unstable || c.state !== "active");
  const bits = [f ? f.nick : ""];
  if (c.state === "active") {
    if (S.rtt !== null) bits.push(S.rtt + " мс");
    if (S.route) bits.push({ lan: "локальная сеть", direct: "напрямую", relay: "через TURN" }[S.route]);
    bits.push(duration(Date.now() - c.started));
  }
  $("#vpSub").textContent = bits.join(" · ");
  const camBtn = $("#vpCam");
  camBtn.innerHTML = icon(S.cam ? "video" : "videoOff", 20);
  camBtn.classList.toggle("on", !!S.cam);
  const scrBtn = $("#vpScreen");
  scrBtn.innerHTML = icon(S.screen ? "screen" : "screen", 20);
  scrBtn.classList.toggle("on", !!S.screen);
  camBtn.disabled = scrBtn.disabled = c.state !== "active";
}

function renderUpdate() {
  const u = S.update;
  $("#updateBanner").hidden = !(u && u.available);
  if (u && u.available) $("#updateText").textContent = `Доступна новая версия Zeno ${u.version}`;
}

async function refreshUpdate() {
  try {
    const st = await api("state");
    S.update = st.update;
    renderUpdate();
  } catch (e) { /* окно пережило перезапуск программы */ }
}

$("#updateBtn").onclick = async () => {
  if (S.call && !confirm("Обновление перезапустит Zeno. Звонок прервется на несколько секунд и восстановится сам, если у друга Zeno 0.5.4 или новее. Продолжить?")) return;
  try {
    await api("update/install");
    toast("Скачиваю обновление, Zeno перезапустится сам");
  } catch (e) { fail(e); }
};

// ---------- друзья ----------

function openHome(tab) {
  S.view = "home";
  if (tab) S.homeTab = tab;
  renderAll();
}

$("#navFriends").onclick = () => openHome();
$("#sideAdd").onclick = () => openHome("add");
$$(".tab").forEach((t) => (t.onclick = () => openHome(t.dataset.tab)));
$("#homePaste").onclick = () => pasteFromClipboard();

function renderHome() {
  $$(".tab").forEach((t) => t.classList.toggle("active", t.dataset.tab === S.homeTab));
  const body = $("#homeBody");
  if (S.homeTab === "add") {
    const input = h("input", { placeholder: "Вставьте код дружбы (начинается с ZENO-FRIEND:)", spellcheck: "false" });
    const add = async () => {
      try {
        if (!(await handleCode(input.value))) toast("Это не код дружбы Zeno", "err");
      } catch (e) { fail(e); }
    };
    input.onkeydown = (e) => { if (e.key === "Enter") add(); };
    body.replaceChildren(
      h("h2", {}, "Добавить в друзья"),
      h("p", {}, "Обменяйтесь с другом кодами дружбы через любой мессенджер. Код можно вставить сюда или просто нажать Ctrl+V в окне Zeno."),
      h("div", { class: "list-title", style: { borderBottom: "0", paddingTop: "4px" } }, "Ваш код дружбы"),
      myCodeBox(),
      h("div", { class: "list-title", style: { borderBottom: "0", paddingTop: "4px" } }, "Код друга"),
      h("div", { class: "add-row" }, input, h("button", { class: "btn primary small", onclick: add }, "Добавить")),
    );
    return;
  }
  if (!S.friends.length) {
    body.replaceChildren(h("div", { class: "empty" },
      h("img", { src: "icon-256.png", alt: "" }),
      h("div", {}, "Пока никого нет. Добавьте друга по коду дружбы."),
      h("p", {}),
      h("button", { class: "btn primary", onclick: () => openHome("add") }, "Добавить друга")));
    return;
  }
  body.replaceChildren(
    h("div", { class: "list-title" }, `Все друзья — ${S.friends.length}`),
    ...S.friends.map((f) => {
      const st = peerState(f.id);
      const row = h("div", { class: "friend-row", onclick: () => openDM(f.id) },
        avatarWithDot(f, 32, st),
        h("div", { class: "fr-text" },
          h("div", {}, h("span", { class: "fr-name" }, f.nick), h("span", { class: "fr-login" }, "@" + f.login)),
          h("div", { class: "fr-status" }, statusText(st))),
        h("div", { class: "fr-actions" },
          h("button", { class: "circle-btn", title: "Написать", html: icon("chat", 18), onclick: (e) => { e.stopPropagation(); openDM(f.id); } }),
          h("button", { class: "circle-btn", title: isConnected(f.id) ? "Позвонить" : "Соединиться", html: icon(isConnected(f.id) ? "phone" : "link", 18),
            onclick: (e) => { e.stopPropagation(); isConnected(f.id) ? startCall(f.id, false) : openConnect(f.id); } }),
          h("button", { class: "circle-btn danger", title: "Удалить из друзей", html: icon("x", 18), onclick: (e) => { e.stopPropagation(); removeFriend(f.id); } })));
      return row;
    }),
  );
}

function myCodeBox() {
  const box = h("div", { class: "code-box" }, h("div", { class: "code" }, "…"),
    h("button", { class: "btn primary", html: icon("copy", 18) + "Скопировать" }));
  api("friendcode").then((r) => {
    box.firstChild.textContent = r.code;
    box.lastChild.onclick = async () => {
      if (await copyText(r.code)) toast("Код дружбы скопирован - отправьте его другу", "ok");
    };
  }).catch(fail);
  return box;
}

async function removeFriend(id) {
  const f = fr(id);
  if (!confirm(`Удалить ${f.nick} из друзей? Переписка останется на диске, но соединиться можно будет только после нового обмена кодами дружбы.`)) return;
  try {
    if (S.call && S.call.id === id) hangup();
    const p = peerOf(id);
    if (p) p.close(true);
    S.peers.delete(id);
    const r = await api("friends/remove", { id });
    S.friends = r.friends;
    if (S.sel === id) S.view = "home";
    renderAll();
  } catch (e) { fail(e); }
}

// ---------- разговор ----------

async function openDM(id) {
  S.view = "dm";
  S.sel = id;
  S.unread.delete(id);
  renderAll();
  await ensureHistory(id);
  $("#input").focus();
}

async function ensureHistory(id) {
  if (S.loaded.has(id)) return;
  S.loaded.add(id);
  try {
    const r = await api("history", { friend: id });
    const have = S.msgs.get(id) || [];
    const ids = new Set(r.messages.map((m) => m.id));
    S.msgs.set(id, [...r.messages, ...have.filter((m) => !ids.has(m.id))]);
    if (S.view === "dm" && S.sel === id) renderMessages(id, true);
  } catch (e) {
    S.loaded.delete(id);
    fail(e);
  }
}

function renderDM() {
  const id = S.sel;
  const f = fr(id);
  if (!f) { S.view = "home"; renderAll(); return; }
  const st = peerState(id);
  const on = isConnected(id);
  $("#dmHeadAvatar").replaceChildren(avatarWithDot(f, 24, st));
  $("#dmName").textContent = f.nick;
  const stEl = $("#dmStatus");
  stEl.textContent = statusText(st);
  stEl.classList.toggle("online", on);
  const busy = S.call && S.call.id !== id;
  $("#dmCall").disabled = !on || !!S.call;
  $("#dmVideo").disabled = !on || !!S.call;
  $("#dmCall").title = !on ? "Сначала соединитесь" : busy ? "Вы уже в другом звонке" : "Начать голосовой звонок";

  const lb = $("#linkbar");
  lb.hidden = on;
  if (!on) {
    const txt = ["offer", "answer", "connecting"].includes(st)
      ? `Соединяюсь с <b>${esc(f.nick)}</b>…`
      : S.netOnline !== 0
        ? `<b>${esc(f.nick)}</b> не в сети. Как только он запустит Zeno, вы соединитесь сами.`
        : `Нет связи с публичными серверами - автосоединение не работает. Можно соединиться кодом.`;
    $("#lbText").innerHTML = txt;
  }

  const input = $("#input");
  input.disabled = !on;
  input.placeholder = on ? `Написать @${f.nick}` : "Сообщения отправляются, когда вы соединены";
  $("#sendBtn").disabled = !on;
  renderTyping();
  renderMessages(id);
  renderStage();
  renderLanbar();
  renderYgbar();
  renderBtbar();
}

function linkify(text) {
  return esc(text).replace(/https?:\/\/[^\s<]+/g, (u) => `<a data-url="${u}">${u}</a>`);
}

$("#messages").addEventListener("click", (e) => {
  const a = e.target.closest("a[data-url]");
  if (a) api("open-url", { url: a.dataset.url.replace(/&amp;/g, "&") }).catch(fail);
});

function renderMessages(id, force) {
  if (S.view !== "dm" || S.sel !== id) return;
  const box = $("#messages");
  const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 80;
  const f = fr(id);
  const out = [h("div", { class: "chat-start" }, avatar(f, 80), h("h3", {}, f.nick),
    h("p", {}, `Это начало вашей переписки с ${f.nick}. Сообщения идут напрямую между вашими ПК и хранятся только у вас двоих.`))];
  let prev = null;
  let prevDay = "";
  for (const m of S.msgs.get(id) || []) {
    const day = dayLabel(m.ts);
    if (day !== prevDay) {
      out.push(h("div", { class: "day" }, day));
      prevDay = day;
      prev = null;
    }
    if (m.from === "system") {
      out.push(h("div", { class: "msg system" }, h("span", { class: "sys-ico", html: icon("phone", 16) }),
        h("div", { class: "text" }, m.text + " · " + hhmm(m.ts))));
      prev = null;
      continue;
    }
    const first = !prev || prev.from !== m.from || m.ts - prev.ts > 7 * 60 * 1000;
    const author = m.from === S.me.id ? S.me : f;
    const el = h("div", { class: "msg" + (first ? " first" : "") });
    if (first) {
      el.append(avatar(author, 40), h("div", { class: "meta" }, h("span", { class: "who" }, author.nick),
        h("span", { class: "when" }, (day === "Сегодня" || day === "Вчера" ? day + " в " : day + " ") + hhmm(m.ts))));
    } else {
      el.append(h("span", { class: "side-time" }, hhmm(m.ts)));
    }
    if (m.text) el.append(h("div", { class: "text", html: linkify(m.text) }));
    if (m.file) el.append(fileView(m));
    if (m.from === S.me.id && m.status) {
      el.append(h("span", { class: "state " + m.status, title: m.status === "delivered" ? "Доставлено" : "Отправляется",
        html: icon(m.status === "delivered" ? "checks" : "check", 16) }));
    }
    out.push(el);
    prev = m;
  }
  box.replaceChildren(...out);
  if (atBottom || force) box.scrollTop = box.scrollHeight;
}

function renderTyping() {
  const until = S.typing.get(S.sel) || 0;
  const f = fr(S.sel);
  $("#typing").innerHTML = until > Date.now() && f ? `<b>${esc(f.nick)}</b>&nbsp;печатает…` : "";
}

function pushMsg(id, m) {
  if (!S.msgs.has(id)) S.msgs.set(id, []);
  S.msgs.get(id).push(m);
  renderMessages(id, m.from === S.me.id);
}

function addSys(id, text) {
  const m = { id: crypto.randomUUID(), from: "system", ts: Date.now(), text };
  pushMsg(id, m);
  api("history/add", { friend: id, msg: m }).catch(() => {});
}

const input = $("#input");
let lastTypingSent = 0;

function autosize() {
  input.style.height = "auto";
  input.style.height = Math.min(input.scrollHeight, window.innerHeight * 0.4) + "px";
}

input.addEventListener("input", () => {
  autosize();
  const p = peerOf(S.sel);
  if (p && p.connected && input.value && Date.now() - lastTypingSent > 3000) {
    lastTypingSent = Date.now();
    p.send({ k: "typing" });
  }
});

input.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey) {
    e.preventDefault();
    sendMessage();
  }
});
$("#sendBtn").onclick = () => sendMessage();

function sendMessage() {
  const id = S.sel;
  const p = peerOf(id);
  const text = input.value.trim();
  if (!text) return;
  if (!p || !p.connected) return toast("Нет соединения с другом", "err");
  const m = { id: crypto.randomUUID(), from: S.me.id, ts: Date.now(), text: text.slice(0, 4000) };
  if (!p.send({ k: "msg", id: m.id, ts: m.ts, text: m.text })) return toast("Сообщение не ушло - соединение оборвалось", "err");
  lastTypingSent = 0;
  pushMsg(id, { ...m, status: "sending" });
  input.value = "";
  autosize();
  api("history/add", { friend: id, msg: m }).catch(fail);
}

// ---------- файлы и картинки ----------
// Байты идут напрямую другу по каналу данных кусками по 64 КБ (каждый кусок -
// id файла 36 байт + данные), с ожиданием, если канал занят. Договор:
// file (что за файл) -> file-ok (готов принять) -> куски -> file-end -> file-done.
// Принятое программа пишет в «Загрузки\Zeno»; картинки до 25 МБ показываются
// в чате, а свою отправленную картинку тоже кладем туда, чтобы она была в
// истории после перезапуска. Друг на старой версии не ответит file-ok.

const CHUNK = 64 * 1024;
const MAX_FILE = 4 * 1024 ** 3;
const SHOW_IMG = 25 * 1024 * 1024;
const fileTx = new Map(); // id -> { res, rej, friend } - ждем file-ok
const fileRx = new Map(); // id -> прием: { friend, msg, got, pend, pendLen, img, chain }
const blobs = new Map(); // id сообщения -> blob: URL картинки
const sendChain = new Map(); // друг -> очередь отправки (по одному файлу)

const isImage = (mime) => /^image\/(png|jpe?g|gif|webp|bmp)$/i.test(mime || "");
const showable = (f) => isImage(f.mime) && f.size <= SHOW_IMG;

function fmtSize(n) {
  if (n < 1024) return n + " Б";
  if (n < 1024 ** 2) return (n / 1024).toFixed(n < 10240 ? 1 : 0) + " КБ";
  if (n < 1024 ** 3) return (n / 1024 ** 2).toFixed(1) + " МБ";
  return (n / 1024 ** 3).toFixed(2) + " ГБ";
}

function b64(u8) {
  let s = "";
  for (let i = 0; i < u8.length; i += 0x8000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x8000));
  return btoa(s);
}

function joinParts(parts, len) {
  const out = new Uint8Array(len);
  let o = 0;
  for (const p of parts) { out.set(p, o); o += p.length; }
  return out;
}

const fileMsg = (m) => ({ id: m.id, from: m.from, ts: m.ts, text: m.text || "", file: { name: m.file.name, size: m.file.size, mime: m.file.mime, path: m.file.path, fail: m.file.fail } });

function attachFiles(list) {
  const id = S.sel;
  const p = peerOf(id);
  if (!p || !p.connected) return toast("Нет соединения с другом: файлы идут напрямую", "err");
  for (const file of list) {
    if (!file.size) continue;
    if (file.size > MAX_FILE) { toast(`${file.name}: больше 4 ГБ, не отправить`, "err"); continue; }
    const m = { id: crypto.randomUUID(), from: S.me.id, ts: Date.now(), text: "", status: "sending", progress: 0,
      file: { name: file.name || "картинка.png", size: file.size, mime: file.type || "" } };
    if (showable(m.file)) blobs.set(m.id, URL.createObjectURL(file));
    pushMsg(id, m);
    const chain = (sendChain.get(id) || Promise.resolve()).then(() => sendFile(id, file, m)).catch((e) => {
      api("file/cancel", { id: "own-" + m.id }).catch(() => {}); // недописанная своя копия картинки
      m.file.fail = true;
      m.progress = null;
      toast(`${m.file.name} не отправлен: ${e.message}`, "err");
      api("history/add", { friend: id, msg: fileMsg(m) }).catch(() => {});
      renderMessages(id);
    });
    sendChain.set(id, chain);
  }
}

async function sendFile(id, file, m) {
  const p = peerOf(id);
  if (!p || !p.connected) throw new Error("соединение оборвалось");
  const ready = new Promise((res, rej) => {
    fileTx.set(m.id, { res, rej, friend: id });
    setTimeout(() => rej(new Error("друг не ответил - у него старая версия Zeno?")), 15000);
  });
  p.send({ k: "file", id: m.id, ts: m.ts, name: m.file.name, size: m.file.size, mime: m.file.mime });
  try { await ready; } finally { fileTx.delete(m.id); }
  const keep = showable(m.file); // своя копия картинки - для истории
  const own = "own-" + m.id;
  if (keep) await api("file/begin", { id: own, name: m.file.name });
  const head = new TextEncoder().encode(m.id);
  let ownParts = [], ownLen = 0;
  for (let off = 0; off < file.size; off += CHUNK) {
    const data = new Uint8Array(await file.slice(off, off + CHUNK).arrayBuffer());
    const buf = new Uint8Array(head.length + data.length);
    buf.set(head);
    buf.set(data, head.length);
    await p.sendBinary(buf.buffer);
    if (keep) {
      ownParts.push(data);
      ownLen += data.length;
      if (ownLen >= 1 << 20) {
        await api("file/chunk", { id: own, data: b64(joinParts(ownParts, ownLen)) });
        ownParts = [];
        ownLen = 0;
      }
    }
    setProgress(id, m, (off + data.length) / file.size);
  }
  p.send({ k: "file-end", id: m.id });
  if (keep) {
    if (ownLen) await api("file/chunk", { id: own, data: b64(joinParts(ownParts, ownLen)) });
    m.file.path = (await api("file/end", { id: own })).path;
  }
  m.progress = null;
  api("history/add", { friend: id, msg: fileMsg(m) }).catch(() => {});
  renderMessages(id);
}

// setProgress обновляет полоску прямо в разметке, без перерисовки всего чата
let progressAt = 0;
function setProgress(id, m, frac) {
  m.progress = frac;
  const now = performance.now();
  if (frac < 1 && now - progressAt < 100) return;
  progressAt = now;
  const box = document.querySelector(`[data-mid="${m.id}"]`);
  if (!box) return;
  const bar = box.querySelector(".att-bar i");
  if (bar) bar.style.width = Math.round(frac * 100) + "%";
  const t = box.querySelector(".att-sub");
  if (t) t.textContent = `${fmtSize(m.file.size)} · ${Math.round(frac * 100)}%`;
}

async function onFileOffer(id, m) {
  const p = peerOf(id);
  const size = Number(m.size) || 0;
  if (typeof m.id !== "string" || m.id.length !== 36 || fileRx.has(m.id) || size <= 0 || size > MAX_FILE) return;
  const list = S.msgs.get(id) || [];
  if (list.some((x) => x.id === m.id)) return;
  const msg = { id: m.id, from: id, ts: Math.min(Number(m.ts) || Date.now(), Date.now()), text: "", progress: 0,
    file: { name: String(m.name || "файл").slice(0, 200), size, mime: String(m.mime || "").slice(0, 100) } };
  try {
    await api("file/begin", { id: msg.id, name: msg.file.name });
  } catch (e) {
    return toast("Файл не принят: " + e.message, "err");
  }
  fileRx.set(msg.id, { friend: id, msg, got: 0, pend: [], pendLen: 0, img: showable(msg.file) ? [] : null, chain: Promise.resolve() });
  pushMsg(id, msg);
  p.send({ k: "file-ok", id: msg.id });
  const f = fr(id);
  if (!(document.hasFocus() && S.view === "dm" && S.sel === id)) {
    S.unread.set(id, (S.unread.get(id) || 0) + 1);
    if (S.media.notifyMessages) api("notify", { title: f.nick, text: "Файл: " + msg.file.name }).catch(() => {});
    renderSide();
  }
}

function flushRx(rx) {
  if (!rx.pendLen) return rx.chain;
  const data = b64(joinParts(rx.pend, rx.pendLen));
  rx.pend = [];
  rx.pendLen = 0;
  rx.chain = rx.chain.then(() => api("file/chunk", { id: rx.msg.id, data }));
  return rx.chain;
}

function onPeerBinary(id, buf) {
  if (buf.byteLength <= 36) return;
  const fid = new TextDecoder().decode(new Uint8Array(buf, 0, 36));
  const rx = fileRx.get(fid);
  if (!rx || rx.friend !== id) return;
  const data = new Uint8Array(buf, 36);
  rx.pend.push(data);
  rx.pendLen += data.length;
  rx.got += data.length;
  if (rx.img) rx.img.push(data);
  if (rx.pendLen >= 1 << 20) flushRx(rx).catch(() => {});
  setProgress(id, rx.msg, Math.min(1, rx.got / rx.msg.file.size));
}

async function onFileEnd(id, fid) {
  const rx = fileRx.get(fid);
  if (!rx || rx.friend !== id) return;
  fileRx.delete(fid);
  const msg = rx.msg;
  try {
    await flushRx(rx);
    msg.file.path = (await api("file/end", { id: fid })).path;
    if (rx.img) blobs.set(fid, URL.createObjectURL(new Blob([joinParts(rx.img, rx.got)], { type: msg.file.mime })));
  } catch (e) {
    msg.file.fail = true;
    api("file/cancel", { id: fid }).catch(() => {});
  }
  msg.progress = null;
  api("history/add", { friend: id, msg: fileMsg(msg) }).catch(() => {});
  peerOf(id).send({ k: "file-done", id: fid });
  renderMessages(id);
}

// filesLost: связь оборвалась - недопринятое удаляем, неотправленное помечаем
function filesLost(id) {
  for (const [fid, rx] of fileRx) {
    if (rx.friend !== id) continue;
    fileRx.delete(fid);
    rx.msg.file.fail = true;
    rx.msg.progress = null;
    api("file/cancel", { id: fid }).catch(() => {});
    api("history/add", { friend: id, msg: fileMsg(rx.msg) }).catch(() => {});
  }
  for (const [, tx] of fileTx) if (tx.friend === id) tx.rej(new Error("соединение оборвалось"));
  renderMessages(id);
}

async function loadImage(m, img) {
  try {
    const r = await api("file/read", { path: m.file.path });
    const bin = atob(r.data);
    const u8 = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i);
    const url = URL.createObjectURL(new Blob([u8], { type: m.file.mime }));
    blobs.set(m.id, url);
    img.src = url;
  } catch (e) {
    img.replaceWith(h("div", { class: "att-gone" }, "Картинка удалена из папки Zeno"));
  }
}

function fileView(m) {
  const f = m.file;
  const open = (reveal) => (e) => { e.stopPropagation(); api("file/open", { path: f.path, reveal }).catch(fail); };
  const busy = m.progress != null && !f.fail;
  const bar = busy ? h("div", { class: "att-bar" }, h("i", { style: { width: Math.round(m.progress * 100) + "%" } })) : null;
  if (showable(f) && !f.fail && (blobs.has(m.id) || f.path)) {
    const img = h("img", { class: "att-img", alt: f.name, title: f.path ? "Открыть" : f.name, draggable: "false" });
    if (blobs.has(m.id)) img.src = blobs.get(m.id);
    else loadImage(m, img);
    if (f.path) img.onclick = open(false);
    return h("div", { class: "att-imgbox", "data-mid": m.id }, img, bar);
  }
  const sub = f.fail ? "не передан" : busy ? `${fmtSize(f.size)} · ${Math.round(m.progress * 100)}%` : fmtSize(f.size);
  return h("div", { class: "att-file" + (f.fail ? " fail" : ""), "data-mid": m.id },
    h("span", { class: "att-ico", html: icon("file", 26) }),
    h("div", { class: "att-info" }, h("div", { class: "att-name", title: f.name }, f.name), h("div", { class: "att-sub" }, sub), bar),
    f.path && !busy ? h("button", { class: "icon-btn", title: "Открыть", html: icon("file", 18), onclick: open(false) }) : null,
    f.path && !busy ? h("button", { class: "icon-btn", title: "Показать в папке", html: icon("folder", 18), onclick: open(true) }) : null);
}

$("#attachBtn").onclick = () => $("#fileInput").click();
$("#fileInput").onchange = (e) => {
  attachFiles([...e.target.files]);
  e.target.value = "";
};
input.addEventListener("paste", (e) => {
  const files = [...(e.clipboardData ? e.clipboardData.files : [])];
  if (!files.length) return;
  e.preventDefault();
  attachFiles(files);
});
{
  const chat = $("#messages").parentElement;
  chat.addEventListener("dragover", (e) => {
    if (S.view !== "dm" || ![...e.dataTransfer.types].includes("Files")) return;
    e.preventDefault();
    chat.classList.add("drop");
  });
  chat.addEventListener("dragleave", (e) => { if (!chat.contains(e.relatedTarget)) chat.classList.remove("drop"); });
  chat.addEventListener("drop", (e) => {
    chat.classList.remove("drop");
    if (S.view !== "dm" || !e.dataTransfer.files.length) return;
    e.preventDefault();
    attachFiles([...e.dataTransfer.files]);
  });
}

// ---------- соединение ----------

// S.stunAuto - выбор программы: только серверы, видящие настоящий адрес (мимо VPN)
S.stunAuto = null;

async function refreshStun() {
  try { S.stunAuto = await api("net/stun"); } catch (e) { /* остается прежний */ }
}

function iceServers() {
  const own = S.media.stun.split(/\s+/).map((s) => s.trim()).filter((s) => /^stuns?:/.test(s));
  const auto = S.stunAuto && S.stunAuto.servers && S.stunAuto.servers.length ? S.stunAuto.servers : DEFAULT_STUN;
  const list = [{ urls: own.length ? own : auto }];
  if (S.media.turnUrl.trim()) {
    list.push({ urls: S.media.turnUrl.trim().split(/\s+/), username: S.media.turnUser, credential: S.media.turnPass });
  }
  return list;
}

function getPeer(id) {
  let p = peerOf(id);
  if (p) return p;
  p = new Peer(fr(id), { iceServers, voiceKbps: () => S.media.voiceKbps, codec: () => S.media.codec });
  p.addEventListener("state", (e) => onPeerState(id, e.detail));
  p.addEventListener("message", (e) => onPeerMessage(id, e.detail).catch(fail));
  p.addEventListener("binary", (e) => onPeerBinary(id, e.detail));
  p.addEventListener("media", () => onPeerMedia(id));
  p.addEventListener("lost", (e) => onPeerLost(id, e.detail));
  S.peers.set(id, p);
  return p;
}

function onPeerState(id, st) {
  const f = fr(id);
  const p = peerOf(id);
  api("peer/state", { friend: id, state: st }).catch(() => {});
  if (p && st !== "connected" && st !== "unstable") p.helloSent = false;
  if (st === "connected" && p && !p.connectedAt) {
    p.connectedAt = Date.now();
    p.autoAt = 0; // соединились - следующая попытка (после обрыва) может идти сразу
  }
  if (p && st === "offline") p.connectedAt = 0;
  if (st === "connected") {
    if (!p.helloSent) {
      p.helloSent = true;
      p.send({ k: "hello", nick: S.me.nick, color: S.me.color, v: S.version });
      if (S.call && S.call.id === id && S.call.state === "reconnecting") p.send({ k: "resume" });
      lanAnnounce(id);
      ygAnnounce(id);
      btAnnounce(id);
      sfx.play("connect");
      toast(`Соединено с ${f.nick}`, "ok");
      closeConnectModal(id);
      ensureHistory(id);
    }
  }
  renderAll();
}

function onPeerLost(id, why) {
  const f = fr(id);
  const p = peerOf(id);
  api("peer/state", { friend: id, state: "lost: " + (why || "?") }).catch(() => {});
  if (p) {
    p.helloSent = false;
    p.autoAt = 0;
  }
  S.remote.delete(id);
  filesLost(id);
  const inCall = S.call && S.call.id === id && (S.call.state === "active" || S.call.state === "reconnecting");
  if (inCall) callLost();
  else if (S.call && S.call.id === id) endCall("Соединение потеряно");
  if (f && !inCall) toast(`Соединение с ${f.nick} потеряно, переподключаюсь…`, "err");
  setTimeout(() => autoReconnect(id), 1500);
  renderAll();
}

function onPeerMedia(id) {
  if (S.call && S.call.id === id && S.call.state === "active") {
    attachRemote(id);
    tuneSenders().catch(fail);
  }
}

async function onPeerMessage(id, m) {
  const f = fr(id);
  const p = peerOf(id);
  if (!f || !p) return;
  switch (m.k) {
    case "hello": {
      if (typeof m.nick === "string" && (m.nick !== f.nick || m.color !== f.color)) {
        try {
          const r = await api("friends/update", { id, nick: m.nick, color: m.color || "" });
          S.friends = r.friends;
          renderAll();
        } catch (e) { /* ник не прошел проверку - оставляем старый */ }
      }
      break;
    }
    case "file":
      await onFileOffer(id, m);
      break;
    case "file-ok": {
      const tx = fileTx.get(m.id);
      if (tx) tx.res();
      break;
    }
    case "file-end":
      await onFileEnd(id, m.id);
      break;
    case "file-done": {
      const x = (S.msgs.get(id) || []).find((y) => y.id === m.id);
      if (x) {
        x.status = "delivered";
        renderMessages(id);
      }
      break;
    }
    case "msg": {
      const list = S.msgs.get(id) || [];
      p.send({ k: "ack", id: m.id });
      if (typeof m.id !== "string" || list.some((x) => x.id === m.id)) break;
      const msg = { id: m.id, from: id, ts: Math.min(Number(m.ts) || Date.now(), Date.now()), text: String(m.text || "").slice(0, 4000) };
      S.typing.delete(id);
      pushMsg(id, msg);
      api("history/add", { friend: id, msg }).catch(fail);
      const looking = document.hasFocus() && S.view === "dm" && S.sel === id;
      if (!looking) {
        S.unread.set(id, (S.unread.get(id) || 0) + 1);
        if (S.media.sounds) api("sound", { name: "message" }).catch(() => sfx.play("message"));
        if (S.media.notifyMessages) {
          api("notify", { title: f.nick, text: msg.text }).catch(() => {});
          api("attention", { show: false }).catch(() => {});
        }
        renderSide();
      }
      renderTyping();
      break;
    }
    case "ack": {
      const x = (S.msgs.get(id) || []).find((y) => y.id === m.id);
      if (x) {
        x.status = "delivered";
        renderMessages(id);
      }
      break;
    }
    case "typing":
      S.typing.set(id, Date.now() + 4500);
      if (S.sel === id) renderTyping();
      break;
    case "call": onCall(id, m); break;
    case "accept":
      if (S.call && S.call.id === id && S.call.state === "outgoing") activateCall();
      break;
    case "decline":
      if (S.call && S.call.id === id) endCall(`${f.nick} отклонил звонок`);
      break;
    case "busy":
      if (S.call && S.call.id === id) endCall(`${f.nick} уже в другом звонке`);
      break;
    case "cancel":
      if (S.call && S.call.id === id && S.call.state === "incoming") endCall("Пропущенный звонок");
      break;
    case "hangup":
      if (S.call && S.call.id === id) endCall(S.call.state === "reconnecting" ? "Звонок завершен: у друга он уже закончился" : undefined);
      break;
    case "resume": // друг восстановил связь и хочет продолжить звонок
      if (S.call && S.call.id === id && (S.call.state === "reconnecting" || S.call.state === "active")) {
        p.send({ k: "resume-ok" });
        await resumeCall();
      } else if (!S.call && recentCall(id)) { // это мы перезапускались (обновление) посреди звонка
        p.send({ k: "resume-ok" });
        await rejoinCall(id);
      } else {
        p.send({ k: "hangup" });
      }
      break;
    case "resume-ok":
      if (S.call && S.call.id === id && S.call.state === "reconnecting") await resumeCall();
      break;
    case "nscreen-offer":
      if (S.call && S.call.id === id && typeof m.sdp === "string") await acceptNative(id, m.sdp);
      break;
    case "nscreen-answer":
      if (S.nsWait && typeof m.sdp === "string") S.nsWait(m.sdp);
      break;
    case "nscreen-stop":
      closeNativeIn();
      renderStage();
      break;
    case "lan-state":
    case "lan-offer":
    case "lan-answer":
      await onLanMessage(id, m);
      break;
    case "yg-invite":
    case "yg-end":
    case "yg-open":
    case "yg-ws":
      onYgMessage(id, m);
      break;
    case "bt-offer":
    case "bt-get":
    case "bt-drop":
    case "bt-link-offer":
    case "bt-link-answer":
      await onBtMessage(id, m);
      break;
    case "state":
      if (!m.screen) closeNativeIn();
      S.remote.set(id, { mic: !!m.mic, deaf: !!m.deaf, cam: !!m.cam, screen: !!m.screen });
      if (m.screen && S.call && S.call.id === id && !S.screen) S.focus = null;
      renderStage();
      break;
  }
}

// ---------- автосоединение ----------
// Друзья добавлены один раз - дальше программы находят друг друга сами через
// зашифрованный канал пары на публичных серверах (auto.go). Предложение
// всегда делает тот, у кого ключ меньше: так встречные попытки не сталкиваются.

S.netOnline = null; // null - еще не знаем (первые секунды после запуска)

// autoBusy: попытка соединения уже идет (или шла меньше 25 с назад) - вторую не начинаем
function autoBusy(p) {
  return !!p.autoAt && Date.now() - p.autoAt < 25000;
}

function autoHello(id, reply = false) {
  api("auto/send", { to: id, kind: "hello", reply }).catch(() => {});
}

let lastHelloAll = 0;

function autoHelloAll() {
  if (Date.now() - lastHelloAll < 3000) return; // серверы подключаются пачкой - не звать друзей на каждый
  lastHelloAll = Date.now();
  for (const f of S.friends) {
    const p = getPeer(f.id);
    if (!p.connected && !autoBusy(p)) autoHello(f.id);
  }
}

function autoReconnect(id) {
  const p = fr(id) && getPeer(id);
  if (!p || p.connected || autoBusy(p)) return;
  if (S.me.id < id) autoOffer(id);
  else autoHello(id);
}

async function autoOffer(id) {
  const p = getPeer(id);
  if (p.connected || autoBusy(p)) return;
  p.autoAt = Date.now();
  try {
    const code = await p.makeOffer();
    await api("auto/send", { to: id, kind: "offer", data: code });
  } catch (e) {
    console.warn("автосоединение:", e);
  }
}

async function onAuto(ev) {
  if (ev.kind === "broker") return autoHelloAll();
  const id = ev.from;
  if (!fr(id)) return;
  const p = getPeer(id);
  if (ev.kind === "hello") {
    if (p.connected) {
      // друг зовет - значит, у него соединения нет (перезапустился), наше уже мертвое
      if (Date.now() - (p.connectedAt || 0) < 8000) return;
      if (S.call && S.call.id === id) endCall("Соединение потеряно");
      p.close(true);
    }
    if (S.me.id < id) autoOffer(id);
    else if (!ev.reply) autoHello(id, true);
  } else if (ev.kind === "offer") {
    // предложения от друга идут по нарастающей: старое или повтор (с другого сервера) пропускаем
    if (ev.ts && ev.ts <= (p.lastOfferTs || 0)) return;
    p.lastOfferTs = ev.ts || Date.now();
    const inv = await api("invite/open", { code: ev.data });
    if (S.call && S.call.id === id) endCall();
    p.autoAt = Date.now();
    const code = await p.acceptOffer(inv);
    await api("auto/send", { to: id, kind: "answer", data: code });
  } else if (ev.kind === "answer") {
    const inv = await api("invite/open", { code: ev.data });
    if (p.role === "offerer" && p.nonce === inv.nonce && p.pc && p.pc.signalingState === "have-local-offer") {
      await p.acceptAnswer(inv);
    }
  }
}

let autoStarted = false;
let autoStartedAt = 0;

async function autoLoop() {
  if (autoStarted) return;
  autoStarted = true;
  autoStartedAt = Date.now();
  setTimeout(autoHelloAll, 1000);
  setInterval(autoHelloAll, 30000);
  for (let first = true; ; first = false) {
    let r = null;
    try {
      r = await api("auto/poll", { timeout: first ? 3000 : 25000 });
    } catch (e) {
      await sleep(3000);
      continue;
    }
    // первые 15 с серверы еще подключаются - «нет связи» не показываем
    const online = r.online === 0 && Date.now() - autoStartedAt < 15000 ? null : r.online;
    if (online !== S.netOnline) {
      const was = S.netOnline;
      S.netOnline = online;
      if (!was && r.online) autoHelloAll();
      renderAll();
    }
    for (const ev of r.events || []) onAuto(ev).catch((e) => console.warn("автосоединение:", e));
  }
}

// ---------- коды ----------

async function handleCode(raw, opts) {
  const text = (raw || "").trim();
  if (text.startsWith("ZENO-FRIEND:")) {
    const r = await api("friends/add", { code: text });
    S.friends = r.friends;
    setTimeout(() => autoReconnect(r.friend.id), 1500);
    toast(`${r.friend.nick} теперь в друзьях`, "ok");
    S.homeTab = "all";
    openDM(r.friend.id);
    return true;
  }
  if (text.startsWith("ZENO:")) {
    const inv = await api("invite/open", { code: text });
    const id = inv.friend.id;
    const p = getPeer(id);
    if (inv.type === "offer") {
      if (S.call && S.call.id === id) endCall();
      closeConnectModal(id);
      openDM(id);
      if (opts && opts.short) { // ответ уйдет через тот же ящик на публичных серверах
        const code = await p.acceptOffer(inv);
        await api("rdv/answer", { short: opts.short, code });
        return true;
      }
      const m = answerModal(fr(id));
      try {
        const code = await p.acceptOffer(inv);
        m.setCode(code);
      } catch (e) {
        m.close();
        throw e;
      }
    } else {
      await p.acceptAnswer(inv);
      if (S.connectFor && S.connectFor.id === id) S.connectFor.setStatus("Соединяюсь…", true);
      openDM(id);
    }
    return true;
  }
  return false;
}

const SHORT_RE = /^\s*[A-Za-z0-9]{3}-?[A-Za-z0-9]{3}\s*$/;

async function pasteFromClipboard() {
  const text = (await readClipboard()).trim();
  if (SHORT_RE.test(text)) return enterCodeModal(text);
  try {
    if (text && (await handleCode(text))) return;
  } catch (e) {
    return fail(e);
  }
  enterCodeModal();
}

document.addEventListener("paste", (e) => {
  const t = e.target;
  if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA")) return;
  const text = e.clipboardData.getData("text");
  if (/^\s*ZENO/.test(text)) {
    e.preventDefault();
    handleCode(text).catch(fail);
  } else if (SHORT_RE.test(text)) {
    e.preventDefault();
    enterCodeModal(text);
  }
});

$("#dmLink").onclick = () => openConnect(S.sel);
$("#lbOffer").onclick = () => openConnect(S.sel);
$("#lbPaste").onclick = () => enterCodeModal();

// ---------- окна ----------

function modal({ title, sub, body, foot, wide, onClose }) {
  const box = h("div", { class: "modal", style: wide ? { width: "560px" } : null },
    h("div", { class: "modal-head" }, h("h2", {}, title), sub ? h("p", {}, sub) : null),
    h("div", { class: "modal-body" }, body || []),
    foot ? h("div", { class: "modal-foot" }, foot) : null);
  const ov = h("div", { class: "overlay" }, box);
  const close = () => {
    ov.remove();
    document.removeEventListener("keydown", esc_);
    if (onClose) onClose();
  };
  const esc_ = (e) => { if (e.key === "Escape") close(); };
  ov.addEventListener("mousedown", (e) => { if (e.target === ov) close(); });
  document.addEventListener("keydown", esc_);
  $("#modals").append(ov);
  return { box, close };
}

function codeBox(label) {
  const code = h("div", { class: "code" }, h("span", { class: "status-line" }, h("i", { class: "spinner" }), label));
  const btn = h("button", { class: "btn primary", disabled: true, html: icon("copy", 18) + "Скопировать" });
  return { el: h("div", { class: "code-box" }, code, btn), code, btn };
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function codeCells() {
  return h("div", { class: "short-code" }, [0, 1, 2, 3, 4, 5].map((i) => h("span", { class: i === 3 ? "gap" : "" }, "")));
}

function openConnect(id) {
  const f = fr(id);
  if (!f) return;
  if (isConnected(id)) {
    if (!confirm(`Вы уже соединены с ${f.nick}. Пересоздать соединение?` + (S.call && S.call.id === id ? " Звонок завершится." : ""))) return;
    if (S.call && S.call.id === id) hangup();
  }
  const p = getPeer(id);
  let short = "";
  let closed = false;
  const cells = codeCells();
  const copyBtn = h("button", { class: "btn primary", disabled: true, html: icon("copy", 18) + "Скопировать код" });
  const status = h("div", { class: "status-line center" }, h("i", { class: "spinner" }), "Готовлю код…");
  const longBox = codeBox("Готовлю…");
  const answer = h("textarea", { class: "input", rows: "3", placeholder: "Код-ответ друга (начинается с ZENO:)", spellcheck: "false" });
  const go = h("button", { class: "btn small primary" }, "Подключиться");
  const details = h("details", { class: "long-code" },
    h("summary", {}, "Не соединяется? Обменяйтесь длинными кодами"),
    h("p", { class: "hint" }, "Если публичные серверы для коротких кодов недоступны: отправьте другу этот длинный код, он вставит его в Zeno (Ctrl+V) и пришлет код-ответ."),
    longBox.el, answer, h("div", { class: "row-end" }, go));
  const m = modal({
    title: `Соединение с ${f.nick}`,
    sub: `Отправьте ${f.nick} этот код любым способом. Он вводит его у себя в Zeno - и вы на связи.`,
    wide: true,
    body: [cells, h("div", { class: "code-actions" }, copyBtn), status, details],
    foot: [h("button", { class: "btn ghost", onclick: () => m.close() }, "Закрыть")],
    onClose: () => {
      closed = true;
      if (short) api("rdv/cancel", { short }).catch(() => {});
      if (S.connectFor && S.connectFor.id === id) S.connectFor = null;
    },
  });
  const setStatus = (text, spin) => status.replaceChildren(spin ? h("i", { class: "spinner" }) : "", text);
  S.connectFor = { id, close: m.close, setStatus };
  go.onclick = async () => {
    try {
      if (!(await handleCode(answer.value))) toast("Это не код Zeno", "err");
    } catch (e) { fail(e); }
  };
  p.makeOffer().then(async (long) => {
    longBox.code.textContent = long;
    longBox.btn.disabled = false;
    longBox.btn.onclick = async () => { if (await copyText(long)) toast("Длинный код скопирован", "ok"); };
    try {
      const r = await api("rdv/offer", { to: id, code: long });
      if (closed) return api("rdv/cancel", { short: r.short }).catch(() => {});
      short = r.short;
      [...cells.children].forEach((c, i) => { c.textContent = short[i]; });
      cells.classList.add("ready");
      copyBtn.disabled = false;
      copyBtn.onclick = async () => { if (await copyText(short)) toast("Код скопирован", "ok"); };
      if (await copyText(short)) toast("Код скопирован - отправьте его другу", "ok");
      setStatus(`Ждем, пока ${f.nick} введет код. Код действует час.`, true);
      while (!closed) {
        const w = await api("rdv/wait", { short, timeout: 25000 }).catch(() => null);
        if (closed) break;
        if (!w) { await sleep(2000); continue; }
        if (w.ok) {
          setStatus("Соединяюсь…", true);
          await handleCode(w.code).catch(fail);
          break;
        }
      }
    } catch (e) {
      cells.hidden = true;
      copyBtn.hidden = true;
      setStatus(e.message, false);
      details.open = true;
    }
  }).catch((e) => { m.close(); fail(e); });
}

function enterCodeModal(prefill) {
  const inp = h("input", { class: "code-input", maxlength: "7", placeholder: "•••-•••", spellcheck: "false", autocomplete: "off" });
  const longTa = h("textarea", { class: "input", rows: "3", placeholder: "ZENO:… или ZENO-FRIEND:…", spellcheck: "false" });
  const status = h("div", { class: "status-line center" });
  const goBtn = h("button", { class: "btn primary" }, "Соединиться");
  const m = modal({
    title: "Ввести код",
    sub: "6 букв и цифр, которые прислал друг.",
    body: [inp, status, h("details", { class: "long-code" }, h("summary", {}, "У меня длинный код или код дружбы"), longTa)],
    foot: [h("button", { class: "btn ghost", onclick: () => m.close() }, "Отмена"), goBtn],
  });
  const setStatus = (text, spin, err) => {
    status.classList.toggle("err", !!err);
    status.replaceChildren(spin ? h("i", { class: "spinner" }) : "", text || "");
  };
  let busy = false;
  const go = async () => {
    if (busy) return;
    const long = longTa.value.trim();
    busy = true;
    goBtn.disabled = true;
    try {
      if (long) {
        if (await handleCode(long)) m.close();
        else setStatus("Это не код Zeno", false, true);
        return;
      }
      const short = inp.value.replace(/-/g, "");
      if (short.length !== 6) return setStatus("Нужно 6 знаков", false, true);
      setStatus("Ищу код…", true);
      const r = await api("rdv/fetch", { short });
      setStatus("Соединяюсь…", true);
      await handleCode(r.code, { short });
      const t0 = Date.now();
      while (!isConnected(r.friend) && Date.now() - t0 < 30000 && document.body.contains(m.box)) await sleep(300);
      if (isConnected(r.friend)) {
        m.close();
        api("rdv/cancel", { short }).catch(() => {});
      } else if (document.body.contains(m.box)) {
        setStatus("Напрямую не соединилось. Попробуйте длинные коды или TURN в настройках.", false, true);
      }
    } catch (e) {
      setStatus(e.message, false, true);
    } finally {
      busy = false;
      goBtn.disabled = false;
    }
  };
  inp.oninput = () => {
    const v = inp.value.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 6);
    inp.value = v.length > 3 ? v.slice(0, 3) + "-" + v.slice(3) : v;
    setStatus("");
    if (v.length === 6) go();
  };
  inp.onkeydown = (e) => { if (e.key === "Enter") go(); };
  goBtn.onclick = go;
  inp.focus();
  if (prefill) {
    inp.value = prefill.trim();
    inp.oninput();
  }
}

function closeConnectModal(id) {
  if (S.connectFor && S.connectFor.id === id) {
    const c = S.connectFor;
    S.connectFor = null;
    c.close();
  }
}

let answerOpen = null;

function answerModal(f) {
  if (answerOpen) answerOpen.close();
  const cb = codeBox("Готовлю код-ответ…");
  const m = modal({
    title: `Код-ответ для ${f.nick}`,
    sub: `Отправьте этот код ${f.nick}. Соединение установится, как только он вставит его у себя.`,
    wide: true,
    body: [cb.el, h("div", { class: "status-line" }, h("i", { class: "spinner" }), "Ждем соединения…")],
    foot: [h("button", { class: "btn ghost", onclick: () => m.close() }, "Закрыть")],
    onClose: () => { answerOpen = null; },
  });
  const id = f.id;
  const watch = () => {
    if (isConnected(id)) m.close();
    else if (answerOpen === api_) setTimeout(watch, 300);
  };
  const api_ = {
    close: m.close,
    async setCode(code) {
      cb.code.textContent = code;
      cb.btn.disabled = false;
      cb.btn.onclick = async () => { if (await copyText(code)) toast("Код-ответ скопирован", "ok"); };
      if (await copyText(code)) toast("Код-ответ скопирован - отправьте его другу", "ok");
      watch();
    },
  };
  answerOpen = api_;
  return api_;
}

function pasteModal() {
  const ta = h("textarea", { class: "input", rows: "4", placeholder: "ZENO:… или ZENO-FRIEND:…", spellcheck: "false" });
  const m = modal({
    title: "Вставить код",
    sub: "Код соединения или код дружбы от друга.",
    body: [ta],
    foot: [h("button", { class: "btn ghost", onclick: () => m.close() }, "Отмена"),
      h("button", { class: "btn primary", onclick: async () => {
        try {
          if (await handleCode(ta.value)) m.close();
          else toast("Это не код Zeno", "err");
        } catch (e) { fail(e); }
      } }, "Готово")],
  });
  ta.focus();
}

// ---------- звонок ----------

function startCall(id, video) {
  const p = peerOf(id);
  if (!p || !p.connected) return toast("Сначала соединитесь с другом", "err");
  if (S.call) return toast("Вы уже в звонке", "err");
  S.call = { id, state: "outgoing", video, started: 0 };
  p.send({ k: "call", video });
  sfx.ring(false);
  S.call.timer = setTimeout(() => {
    if (S.call && S.call.id === id && S.call.state === "outgoing") {
      p.send({ k: "cancel" });
      endCall(`${fr(id).nick} не ответил`);
    }
  }, 45000);
  if (S.view !== "dm" || S.sel !== id) openDM(id);
  else renderAll();
}

$("#dmCall").onclick = () => startCall(S.sel, false);
$("#dmVideo").onclick = () => startCall(S.sel, true);

let incomingEl = null;
let ringing = false;

// ringIncoming: мелодию входящего звонка играет программа (слышно и из трея),
// окно - только если программа не ответила
function ringIncoming(on) {
  if (on === ringing) return;
  ringing = on;
  api("ring", { on }).catch(() => { if (on) sfx.ring(true); });
}

function onCall(id, m) {
  const p = peerOf(id);
  if (S.call && S.call.id !== id) return p.send({ k: "busy" });
  if (S.call && S.call.state === "outgoing") { // позвонили друг другу одновременно
    p.send({ k: "accept" });
    return activateCall();
  }
  if (S.call) return;
  const f = fr(id);
  S.call = { id, state: "incoming", video: !!m.video, started: 0 };
  ringIncoming(true);
  api("attention", { show: true }).catch(() => {});
  api("notify", { title: "Входящий звонок", text: f.nick + " звонит вам" }).catch(() => {});
  incomingEl = h("div", { class: "incoming" }, h("div", { class: "incoming-card" },
    avatar(f, 96),
    h("h3", {}, f.nick),
    h("p", {}, m.video ? "Входящий видеозвонок…" : "Входящий звонок…"),
    h("div", { class: "row" },
      h("button", { class: "round-btn red", title: "Отклонить", html: icon("hang", 26), onclick: declineCall }),
      h("button", { class: "round-btn on", title: "Принять", html: icon("phone", 22), onclick: acceptCall }))));
  document.body.append(incomingEl);
  renderAll();
}

function closeIncoming() {
  if (incomingEl) incomingEl.remove();
  incomingEl = null;
}

function acceptCall() {
  if (!S.call || S.call.state !== "incoming") return;
  const id = S.call.id;
  peerOf(id).send({ k: "accept" });
  activateCall();
  openDM(id);
}

function declineCall() {
  if (!S.call) return;
  const p = peerOf(S.call.id);
  if (p) p.send({ k: "decline" });
  endCall("Звонок отклонен");
}

function hangup() {
  if (!S.call) return;
  const p = peerOf(S.call.id);
  if (p) p.send({ k: S.call.state === "outgoing" ? "cancel" : "hangup" });
  endCall(S.call.state === "outgoing" ? "Звонок отменен" : null);
}

$("#vpHang").onclick = hangup;
$("#sbHang").onclick = hangup;

async function activateCall(resumed) {
  const c = S.call;
  clearTimeout(c.timer);
  sfx.stopRing();
  ringIncoming(false);
  closeIncoming();
  c.state = "active";
  c.started = Date.now();
  sfx.play("join");
  const p = peerOf(c.id);
  try {
    const t = await mic.start(S.media);
    mic.setMuted(S.muted || S.deaf);
    if (S.call !== c) return mic.stop();
    await p.setTrack("mic", t);
  } catch (e) {
    toast("Микрофон недоступен: " + e.message, "err");
  }
  if (c.video) startCam().catch(fail);
  attachRemote(c.id);
  await tuneSenders();
  sendState();
  syncPTT();
  markCall(c.id);
  addSys(c.id, resumed ? "Звонок восстановлен" : "Звонок");
  renderAll();
}

// ---------- восстановление звонка ----------
// Обрыв связи (сеть моргнула, друг перезапустил Zeno для обновления) звонок
// не завершает: до 90 с он ждет в состоянии «переподключение», микрофон и
// трансляция не останавливаются. Когда соединение вернулось, стороны
// договариваются (resume / resume-ok), и дорожки отдаются заново. Метка
// звонка в localStorage дает перезапущенной стороне понять, что она была в
// звонке, и войти обратно без вызова и гудков.

const CALL_MARK = "zeno.call";
const RESUME_WAIT = 90000;

function markCall(id) {
  try { localStorage.setItem(CALL_MARK, JSON.stringify({ id, at: Date.now() })); } catch (e) { /* без метки - просто не вернемся сами */ }
}

function clearCallMark() {
  try { localStorage.removeItem(CALL_MARK); } catch (e) { /* нечего чистить */ }
}

function recentCall(id) {
  try {
    const m = JSON.parse(localStorage.getItem(CALL_MARK) || "null");
    return !!m && m.id === id && Date.now() - m.at < RESUME_WAIT;
  } catch (e) {
    return false;
  }
}

function callLost() {
  const c = S.call;
  if (c.state !== "reconnecting") {
    c.state = "reconnecting";
    toast("Связь со звонком прервалась, восстанавливаю…", "err");
  }
  // свой захват шел отдельным соединением к другу - после восстановления запустим заново
  if (S.native) {
    S.native = false;
    S.nsStats = null;
    c.resumeNative = true;
    api("nscreen/stop").catch(() => {});
  }
  closeNativeIn();
  detachRemote();
  clearTimeout(c.timer);
  c.timer = setTimeout(() => {
    if (S.call === c && c.state === "reconnecting") endCall("Звонок прервался: связь не восстановилась");
  }, RESUME_WAIT);
  renderAll();
}

async function resumeCall() {
  const c = S.call;
  const p = peerOf(c.id);
  if (!p) return;
  clearTimeout(c.timer);
  const was = c.state;
  c.state = "active";
  try {
    if (!mic.live) {
      await mic.start(S.media);
      mic.setMuted(S.muted || S.deaf);
    }
    if (S.call !== c) return;
    await p.setTrack("mic", mic.track);
  } catch (e) {
    toast("Микрофон недоступен: " + e.message, "err");
  }
  if (S.cam) await p.setTrack("cam", S.cam.getVideoTracks()[0]).catch(() => {});
  if (S.screen) {
    S.native = false;
    if (c.resumeNative && S.nativeId) S.native = await startNative(S.nativeId).catch(() => false);
    c.resumeNative = false;
    if (S.screen && !S.native) await p.setTrack("screen", S.screen.getVideoTracks()[0]).catch(() => {});
    await applyScreenAudio().catch(() => {});
  }
  attachRemote(c.id);
  await tuneSenders().catch(() => {});
  sendState();
  syncPTT();
  if (was === "reconnecting") {
    sfx.play("join");
    toast("Звонок восстановлен", "ok");
  }
  renderAll();
}

// rejoinCall: мы перезапустились посреди звонка, друг ждет - входим обратно
async function rejoinCall(id) {
  S.call = { id, state: "incoming", video: false, started: 0 };
  await activateCall(true);
  if (S.view !== "dm" || S.sel !== id) openDM(id);
}

function endCall(reason) {
  const c = S.call;
  if (!c) return;
  clearCallMark();
  clearTimeout(c.timer);
  sfx.stopRing();
  ringIncoming(false);
  closeIncoming();
  const p = peerOf(c.id);
  stopScreen(true);
  stopCam(true);
  closeNativeIn();
  mic.stop();
  if (p && p.mediaReady) for (const slot of SLOTS) p.setTrack(slot, null).catch(() => {});
  detachRemote();
  if (c.state === "active") {
    sfx.play("leave");
    addSys(c.id, "Звонок завершен, " + duration(Date.now() - c.started));
  } else if (reason) {
    addSys(c.id, reason);
  }
  S.call = null;
  S.focus = null;
  S.rtt = S.route = null;
  if (S.full) setFull(false);
  document.body.classList.remove("theater");
  syncPTT();
  renderAll();
}

function sendState() {
  if (!S.call) return;
  const p = peerOf(S.call.id);
  if (p) p.send({ k: "state", mic: !(S.muted || S.deaf), deaf: S.deaf, cam: !!S.cam, screen: !!S.screen });
}

async function tuneSenders() {
  if (!S.call) return;
  const p = peerOf(S.call.id);
  if (!p || !p.mediaReady) return;
  await p.tuneSender("mic", { maxBitrate: S.media.voiceKbps * 1000, priority: "high", networkPriority: "high" });
  if (S.cam) await p.tuneSender("cam", { maxBitrate: 2500000, maxFramerate: 30 });
  if (S.screen) await tuneScreen();
}

const VOL_DEFAULT = { voice: 100, stream: 100, voiceMuted: false, streamMuted: false };

function vol(id) {
  return { ...VOL_DEFAULT, ...((S.media.vol || {})[id] || {}) };
}

function setVol(id, patch) {
  S.media.vol = { ...(S.media.vol || {}), [id]: { ...vol(id), ...patch } };
  applyOutput();
  saveMedia();
  renderStage();
}

function attachRemote(id) {
  const p = peerOf(id);
  if (!p || !p.mediaReady) return;
  const voice = p.remoteTrack("mic");
  const sys = p.remoteTrack("sys");
  pbVoice.setTrack(voice);
  pbSys.setTrack(sys);
  remoteLevel.attach(voice);
  applyOutput();
}

function detachRemote() {
  pbVoice.stop();
  pbSys.stop();
  remoteLevel.detach();
}

function applyOutput() {
  const out = S.media.outputVolume / 100;
  const v = vol(S.call ? S.call.id : "");
  pbVoice.apply((out * v.voice) / 100, S.deaf || v.voiceMuted, S.media.output);
  pbSys.apply((out * v.stream) / 100, S.deaf || v.streamMuted, S.media.output);
}

function setMuted(v) {
  S.muted = v;
  if (!v && S.deaf) S.deaf = false;
  mic.setMuted(S.muted || S.deaf);
  applyOutput();
  sfx.play(v ? "mute" : "unmute");
  sendState();
  renderUserbar();
  renderStage();
}

function setDeaf(v) {
  S.deaf = v;
  mic.setMuted(S.muted || S.deaf);
  applyOutput();
  sfx.play(v ? "mute" : "unmute");
  sendState();
  renderUserbar();
  renderStage();
}

$("#ubMic").onclick = () => setMuted(!(S.muted || S.deaf) ? true : false);
$("#ubDeaf").onclick = () => setDeaf(!S.deaf);
$("#sbMic").onclick = () => setMuted(!(S.muted || S.deaf));

window.zenoPTT = (down) => mic.setPTT(down);

function syncPTT() {
  const vk = S.media.mode === "ptt" ? Number(S.media.pttKey) || 0 : 0;
  api("ptt", { vk }).catch(() => {});
}

// ---------- камера и экран ----------

async function startCam() {
  if (!S.call || S.call.state !== "active") return;
  const res = { "480p": [854, 480], "720p": [1280, 720], "1080p": [1920, 1080] }[S.media.camRes] || [1280, 720];
  const video = { width: { ideal: res[0] }, height: { ideal: res[1] }, frameRate: { ideal: 30 } };
  if (S.media.camera !== "default") video.deviceId = { exact: S.media.camera };
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({ video });
  } catch (e) {
    if (!video.deviceId) throw new Error("Камера недоступна: " + e.message);
    delete video.deviceId;
    stream = await navigator.mediaDevices.getUserMedia({ video });
  }
  S.cam = stream;
  const t = stream.getVideoTracks()[0];
  t.onended = () => stopCam();
  const p = peerOf(S.call.id);
  await p.setTrack("cam", t);
  await p.tuneSender("cam", { maxBitrate: 2500000, maxFramerate: 30 });
  sendState();
  renderAll();
}

function stopCam(silent) {
  if (!S.cam) return;
  S.cam.getTracks().forEach((t) => t.stop());
  S.cam = null;
  const p = S.call && peerOf(S.call.id);
  if (p) p.setTrack("cam", null).catch(() => {});
  if (!silent) {
    sendState();
    renderAll();
  }
}

async function startScreen() {
  if (!S.call || S.call.state !== "active") return toast("Трансляция работает во время звонка", "err");
  openScreenPicker();
}

// ---------- свое окно выбора трансляции ----------
// Родной диалог Chromium не настраивается. Показываем свой список (окна и
// экраны с превью дает программа), а после выбора программа сама проходит
// родной диалог, накрыв окно снимком. id = null - просто родной диалог.

const RES_CHOICES = [["720p", "720p"], ["1080p", "1080p"], ["1440p", "1440p"], ["source", "Исходное"]];
const presetParts = () => {
  const m = /^(720p|1080p|1440p|source)(30|60)$/.exec(S.media.screenPreset) || [0, "1080p", "60"];
  return { res: m[1], fps: m[2] };
};

function chipGroup(options, value, onPick) {
  const g = h("div", { class: "sp-chips" });
  const paint = (v) => [...g.children].forEach((b) => b.classList.toggle("on", b.dataset.v === v));
  for (const [v, label] of options) {
    g.append(h("button", { class: "sp-chip", "data-v": v, onclick: () => { paint(v); onPick(v); } }, label));
  }
  paint(value);
  return g;
}

function openScreenPicker() {
  let tab = "window";
  let sel = null;
  let list = [];
  let timer = 0;
  let closed = false;

  const grid = h("div", { class: "sp-grid" }, h("div", { class: "sp-empty" }, h("i", { class: "spinner" }), "Ищу окна и экраны…"));
  const tabBtn = (k, label) => h("button", { class: "sp-tab", "data-k": k, onclick: () => { tab = k; paint(); } }, label, h("span", { class: "sp-count" }));
  const tabs = h("div", { class: "sp-tabs" }, tabBtn("window", "Приложения"), tabBtn("screen", "Экраны"));
  const go = h("button", { class: "btn primary sp-go", disabled: true, html: icon("screen", 18) + "Начать трансляцию", onclick: () => live(sel) });

  const setPreset = (part) => {
    const p = { ...presetParts(), ...part };
    setMedia({ screenPreset: p.res + p.fps }, tuneScreen);
  };
  const audio = h("input", { type: "checkbox" });
  audio.checked = !!S.media.screenAudio;
  audio.onchange = () => setMedia({ screenAudio: audio.checked }, applyScreenAudio);

  const settings = h("div", { class: "sp-settings" },
    h("div", { class: "sp-set" }, h("div", { class: "sp-label" }, "Разрешение"),
      chipGroup(RES_CHOICES, presetParts().res, (v) => setPreset({ res: v }))),
    h("div", { class: "sp-set" }, h("div", { class: "sp-label" }, "Частота кадров"),
      chipGroup([["30", "30 к/с"], ["60", "60 к/с"]], presetParts().fps, (v) => setPreset({ fps: v }))),
    h("div", { class: "sp-set" }, h("div", { class: "sp-label" }, "Режим"),
      chipGroup([["motion", "Плавность"], ["detail", "Четкость"]], S.media.screenMode, (v) => setMedia({ screenMode: v }, tuneScreen))),
    h("div", { class: "sp-set" }, h("div", { class: "sp-label" }, "Способ захвата"),
      chipGroup([["native", "Видеокарта"], ["browser", "Браузер"]], S.media.captureMode === "browser" ? "browser" : "native",
        (v) => setMedia({ captureMode: v })),
      h("div", { class: "sp-hint" }, "Видеокарта: свой захват и аппаратный кодер, держит 60 к/с под игрой. Браузер - как раньше")),
    h("label", { class: "sp-set sp-audio sp-wide" },
      h("div", {}, h("div", { class: "sp-label" }, "Звук системы"), h("div", { class: "sp-hint" }, "Можно включить и выключить во время трансляции")),
      h("span", { class: "switch" }, audio, h("i"))));

  const box = h("div", { class: "modal sp-modal" },
    h("div", { class: "sp-head" },
      h("h2", {}, "Трансляция экрана"),
      h("button", { class: "sp-x", title: "Закрыть (Esc)", html: icon("x", 20), onclick: () => close() })),
    tabs, grid, settings,
    h("div", { class: "modal-foot sp-foot" },
      h("button", { class: "btn ghost sp-native", title: "Открыть стандартное окно выбора Windows", onclick: () => live(null) }, "Системное окно"),
      h("div", { class: "spacer" }),
      h("button", { class: "btn ghost", onclick: () => close() }, "Отмена"),
      go));
  const ov = h("div", { class: "overlay sp-overlay" }, box);
  const onKey = (e) => {
    if (e.key === "Escape") close();
    if (e.key === "Enter" && sel && !e.target.closest("button:not(.sp-card), input")) {
      e.preventDefault();
      live(sel);
    }
  };
  function close() {
    if (closed) return;
    closed = true;
    clearTimeout(timer);
    ov.remove();
    document.removeEventListener("keydown", onKey);
  }
  ov.addEventListener("mousedown", (e) => { if (e.target === ov) close(); });
  document.addEventListener("keydown", onKey);
  $("#modals").append(ov);

  function live(id) {
    close();
    beginScreen(id).catch(fail);
  }

  function card(s) {
    const img = h("img", { class: "sp-img", src: s.thumb || "", alt: "", draggable: "false" });
    if (!s.thumb) img.hidden = true;
    const c = h("button", { class: "sp-card" + (s.id === sel ? " on" : ""), "data-id": s.id, title: s.name,
      onclick: () => { sel = s.id; paint(); },
      ondblclick: () => live(s.id) },
    h("div", { class: "sp-thumb" }, img, s.kind === "screen" && !s.thumb ? h("span", { html: icon("screen", 36) }) : null),
    h("div", { class: "sp-name" },
      s.icon ? h("img", { class: "sp-icon", src: s.icon, alt: "" }) : h("span", { class: "sp-icon-ph", html: icon(s.kind === "screen" ? "screen" : "dots", 14) }),
      h("span", {}, s.name)));
    return c;
  }

  function paint() {
    for (const b of tabs.children) {
      b.classList.toggle("on", b.dataset.k === tab);
      b.querySelector(".sp-count").textContent = String(list.filter((s) => s.kind === b.dataset.k).length || "");
    }
    const items = list.filter((s) => s.kind === tab);
    if (sel && !list.some((s) => s.id === sel)) sel = null;
    go.disabled = !sel;
    if (!list.length) return;
    // карточки обновляем на месте, чтобы превью не мигали и прокрутка не сбрасывалась
    const have = new Map([...grid.querySelectorAll(".sp-card")].map((c) => [c.dataset.id, c]));
    const next = items.map((s) => {
      const c = have.get(s.id);
      if (!c) return card(s);
      c.classList.toggle("on", s.id === sel);
      const img = c.querySelector(".sp-img");
      if (s.thumb) { img.src = s.thumb; img.hidden = false; }
      c.querySelector(".sp-name > span:last-child").textContent = s.name;
      c.title = s.name;
      return c;
    });
    if (!next.length) next.push(h("div", { class: "sp-empty" }, tab === "window" ? "Открытых окон нет" : "Экраны не найдены"));
    grid.replaceChildren(...next);
  }

  async function load(first) {
    try {
      const r = await api("screen/sources");
      if (closed) return;
      list = r.sources || [];
      if (first && !list.some((s) => s.kind === "window")) tab = "screen";
      paint();
    } catch (e) {
      if (first) grid.replaceChildren(h("div", { class: "sp-empty" }, "Список не получен: " + e.message));
    }
    if (!closed) timer = setTimeout(() => load(false), 2500); // живые превью
  }
  paint();
  load(true);
}

const nextFrame = () => new Promise((r) => requestAnimationFrame(() => r()));

async function beginScreen(id) {
  if (!S.call || S.call.state !== "active") return;
  const pr = SCREEN_PRESETS[S.media.screenPreset] || SCREEN_PRESETS["1080p60"];
  // свой захват: картинку другу шлет программа, а поток браузера нужен только
  // для звука системы и маленького превью у себя - ему хватит 10 к/с
  let wantNative = !!id && S.media.captureMode !== "browser";
  if (id) {
    // свое окно уже закрыто - ждем пару кадров, чтобы на снимке его не было
    await nextFrame();
    await nextFrame();
    try { await api("screen/pick", { id }); } catch (e) { toast(e.message, "err"); id = null; wantNative = false; }
  }
  let stream;
  try {
    // звук берем всегда: отправлять его или нет, решает галочка, без перезапуска трансляции
    const fps = wantNative ? PREVIEW_FPS : pr.fps;
    stream = await navigator.mediaDevices.getDisplayMedia({
      video: { width: { max: pr.w }, height: { max: pr.h }, frameRate: { ideal: fps, max: fps } },
      audio: { echoCancellation: false, noiseSuppression: false, autoGainControl: false, suppressLocalAudioPlayback: false, restrictOwnAudio: true },
      systemAudio: "include",
      selfBrowserSurface: "exclude",
      surfaceSwitching: "include",
      monitorTypeSurfaces: "include",
    });
  } catch (e) {
    if (e.name !== "NotAllowedError" && e.name !== "AbortError") fail(new Error("Не удалось начать трансляцию: " + e.message));
    return;
  } finally {
    if (id) api("screen/picked").catch(() => {});
  }
  if (!S.call) {
    stream.getTracks().forEach((t) => t.stop());
    return;
  }
  stopScreen(true);
  S.screen = stream;
  const v = stream.getVideoTracks()[0];
  v.onended = () => stopScreen();
  const p = peerOf(S.call.id);
  S.native = false;
  S.nativeId = wantNative ? id : null;
  if (wantNative) {
    S.native = await startNative(id).catch((e) => {
      toast("Захват через видеокарту не запустился, транслирую через браузер: " + e.message, "err");
      return false;
    });
    if (!S.screen) return; // трансляцию остановили, пока договаривались
  }
  if (!S.native) await p.setTrack("screen", v);
  await applyScreenAudio();
  await tuneScreen();
  api("capturebar", { active: true }).catch(() => {});
  sfx.play("stream");
  sendState();
  renderAll();
}

// ---------- свой захват через видеокарту ----------
// Картинку захватывает и сжимает программа (аппаратный кодер), а шлет другу
// отдельным соединением прямо в его браузер. Договариваемся по каналу данных:
// nscreen-offer -> nscreen-answer. Друг на старой версии не ответит - тогда
// через 6 с трансляция идет как раньше, через браузер.

const PREVIEW_FPS = 10;
S.native = false;
S.nsWait = null; // ждем ответ друга
S.nsIn = null; // прием своего захвата друга: { pc, track }
S.nsStats = null;

async function startNative(id) {
  const pr = SCREEN_PRESETS[S.media.screenPreset] || SCREEN_PRESETS["1080p60"];
  const r = await api("nscreen/start", { id, w: pr.w, h: pr.h, fps: pr.fps, kbps: pr.kbps, ice: iceServers() });
  const p = S.call && peerOf(S.call.id);
  if (!p) {
    api("nscreen/stop").catch(() => {});
    return false;
  }
  const answer = await new Promise((resolve) => {
    S.nsWait = resolve;
    p.send({ k: "nscreen-offer", sdp: r.sdp });
    setTimeout(() => resolve(null), 6000);
  });
  S.nsWait = null;
  if (!answer || !S.screen) {
    api("nscreen/stop").catch(() => {});
    if (!answer) toast("У друга старая версия Zeno - транслирую через браузер", "err");
    return false;
  }
  await api("nscreen/answer", { sdp: answer });
  // соединение напрямую к другу может не пробиться - тогда как раньше, через браузер
  for (let i = 0; i < 40; i++) {
    await sleep(200);
    const r = await api("nscreen/stats").catch(() => null);
    const state = r && r.stats && r.stats.state;
    if (state === "connected") return true;
    if (!S.screen || state === "failed" || state === "closed") break;
  }
  api("nscreen/stop").catch(() => {});
  p.send({ k: "nscreen-stop" });
  if (S.screen) toast("Прямое соединение для захвата через видеокарту не установилось - транслирую через браузер", "err");
  return false;
}

function stopNative() {
  if (!S.native) return;
  S.native = false;
  S.nsStats = null;
  api("nscreen/stop").catch(() => {});
  const p = S.call && peerOf(S.call.id);
  if (p) p.send({ k: "nscreen-stop" });
}

async function acceptNative(id, sdp) {
  closeNativeIn();
  const pc = new RTCPeerConnection({ iceServers: iceServers() });
  const box = { pc, track: null };
  S.nsIn = box;
  pc.ontrack = (e) => {
    box.track = e.track;
    renderStage();
  };
  pc.onconnectionstatechange = () => {
    if (pc.connectionState === "failed" && S.nsIn === box) {
      closeNativeIn();
      renderStage();
    }
  };
  await pc.setRemoteDescription({ type: "offer", sdp });
  await pc.setLocalDescription();
  await gathered(pc, 3000);
  if (S.nsIn !== box) return;
  peerOf(id).send({ k: "nscreen-answer", sdp: pc.localDescription.sdp });
}

function closeNativeIn() {
  if (!S.nsIn) return;
  try { S.nsIn.pc.close(); } catch (e) { /* уже закрыт */ }
  S.nsIn = null;
}

// remoteScreen - картинка трансляции друга: своим захватом, если он идет, иначе обычная
function remoteScreen(p) {
  return (S.nsIn && S.nsIn.track) || p.remoteTrack("screen");
}

// ---------- встроенные приложения ----------
// Как «Активности» в Discord: приложения, которые запускают вдвоем с другом.
// virtualZeno - локальная сеть (как Hamachi): у обоих появляются адреса
// 10.77.x.y, по ним работают ping, LAN-игры и общие папки. Сеть поднимается,
// только если virtualZeno запустили оба. Договариваемся по каналу данных:
// lan-state (запущен или нет), lan-offer -> lan-answer (соединение между
// программами, internal/vlan). Соединение предлагает тот, у кого ключ меньше.

// line(id) -> { on, remote, text }: запущено у нас, запущено у друга, строка состояния
const APPS = [
  {
    id: "vlan", name: "virtualZeno", icon: "lan", tag: "Локальная сеть", color: "#157a4d",
    art: '<svg viewBox="0 0 160 100" preserveAspectRatio="xMidYMid slice" aria-hidden="true"><rect width="160" height="100" fill="#157a4d"/><path d="M0 20h160M0 40h160M0 60h160M0 80h160M20 0v100M40 0v100M60 0v100M80 0v100M100 0v100M120 0v100M140 0v100" stroke="#1f9460" stroke-width="1"/><path d="M62 44h36" stroke="#ffffff" stroke-width="2.5" stroke-dasharray="4 4"/><g stroke="#d6f8e4" stroke-width="2" stroke-linecap="round"><rect x="16" y="28" width="46" height="32" rx="4" fill="#0d3d27"/><path d="M31 67h16M39 60v7"/><rect x="98" y="28" width="46" height="32" rx="4" fill="#0d3d27"/><path d="M113 67h16M121 60v7"/></g><g font-family="Consolas, monospace" font-size="8" fill="#8ff0bd" text-anchor="middle"><text x="39" y="47">10.77.1.2</text><text x="121" y="47">10.77.9.5</text></g><circle cx="80" cy="44" r="8" fill="#ffffff"/><circle cx="80" cy="44" r="3.5" fill="#157a4d"/><path d="M60 84h40" stroke="#8ff0bd" stroke-width="2" stroke-linecap="round" opacity=".7"/></svg>',
    desc: "Локальная сеть с другом, как Hamachi: LAN-игры, общие папки и ping по адресам 10.77.x.y. Включается, когда запустили оба.",
    line: (id) => lanLine(id), start: (id) => lanStart(id), exit: (id) => lanExit(id),
  },
  {
    id: "yaguessr", name: "ЯГессер", icon: "globe", tag: "Игра", color: "#c97a26",
    art: '<svg viewBox="0 0 160 100" preserveAspectRatio="xMidYMid slice" aria-hidden="true"><rect width="160" height="100" fill="#c97a26"/><path d="M-5 70c20-8 30 4 52-6s30-26 58-18 40 2 60-8v70H-5z" fill="#b56a1d"/><path d="M10 18c14-6 26-2 34 6s4 18-8 20-26 2-30-8-4-14 4-18zM110 12c12-4 30 0 34 10s-6 16-18 16-22-4-22-12 0-12 6-14z" fill="#e0a356"/><path d="M0 52c30 6 60-14 90-6s50 10 70 2M30 0c6 30-4 60 6 100M118 0c-8 34 10 62 0 100" stroke="#f3d3a4" stroke-width="1.6" fill="none" stroke-dasharray="3 4"/><g transform="translate(80 46)"><path d="M0 26C-14 10-18 2-18-8a18 18 0 0 1 36 0C18 2 14 10 0 26z" fill="#e5484d" stroke="#ffffff" stroke-width="2.5"/><circle cx="0" cy="-8" r="6.5" fill="#ffffff"/></g><ellipse cx="80" cy="74" rx="12" ry="3" fill="#8f4f12" opacity=".5"/><path d="M126 70l8 8M134 70l-8 8" stroke="#ffffff" stroke-width="2.5" stroke-linecap="round"/></svg>',
    desc: "Угадай место по панораме Google или Яндекса: классика, дуэль, канат, ставки и другие режимы. Комната живет в Zeno того, кто запустил, свой сервер не нужен.",
    line: (id) => ygLine(id), start: (id) => ygStart(id), exit: () => ygClose(),
  },
  {
    id: "bt", name: "TarBitTorrent", icon: "download", tag: "Передача файлов", color: "#2f5fbf",
    art: '<svg viewBox="0 0 160 100" preserveAspectRatio="xMidYMid slice" aria-hidden="true"><rect width="160" height="100" fill="#2f5fbf"/><g fill="#3d70d4"><rect x="10" y="62" width="10" height="26"/><rect x="24" y="52" width="10" height="36"/><rect x="38" y="70" width="10" height="18"/><rect x="112" y="58" width="10" height="30"/><rect x="126" y="44" width="10" height="44"/><rect x="140" y="66" width="10" height="22"/></g><rect x="56" y="18" width="48" height="58" rx="5" fill="#ffffff"/><path d="M90 18v14h14" fill="#cfdcf7"/><path d="M80 34v26M70 50l10 10 10-10" stroke="#2f5fbf" stroke-width="4" fill="none" stroke-linecap="round" stroke-linejoin="round"/><rect x="62" y="82" width="36" height="5" rx="2.5" fill="#1d3f87"/><rect x="62" y="82" width="22" height="5" rx="2.5" fill="#8fe3a8"/></svg>',
    desc: "Большие файлы и папки другу, хоть 50 ГБ: движок TarBitTorrent, проверка каждого куска, докачка после обрыва и перезапуска. Раздача видна только другу; идет напрямую или через Zeno, если напрямую не достучаться.",
    line: (id) => btLine(id), start: () => {}, exit: () => {},
    buttons: (id) => btButtons(id), panel: (id) => btPanel(id),
    badge: (id, l) => l.remote ? { badge: "Вам файл", cls: "invite" } : btMine(id).some(btBusy) ? { badge: "Идет", cls: "on" } : { badge: "", cls: "" },
  },
];

S.lanRemote = new Map(); // id -> друг запустил virtualZeno у себя
S.lanSt = null; // последний ответ lan/status
S.lanOfferAt = new Map(); // id -> когда последний раз предлагали соединение

const lanOn = (id) => S.media.lanOn.includes(id);

function lanAnnounce(id) {
  const p = peerOf(id);
  if (p) p.send({ k: "lan-state", on: lanOn(id) });
}

function lanWant() {
  api("lan/want", { on: S.media.lanOn.length > 0 }).catch(() => {});
}

// lanReady: служба стоит и подходит; нет - предлагаем поставить (один раз, UAC)
async function lanReady() {
  const st = await api("lan/status");
  const old = /устарела/.test(st.error || "");
  if (st.service !== "missing" && !old) return true;
  return new Promise((resolve) => {
    const err = h("div", { class: "form-error" });
    const go = h("button", { class: "btn primary" }, old ? "Обновить" : "Установить");
    const m = modal({
      title: old ? "Обновить virtualZeno" : "Установка virtualZeno",
      body: [
        h("p", { class: "hint" }, "virtualZeno ставит сетевой адаптер Wintun - драйвер от создателей WireGuard. Для этого Windows один раз спросит разрешение администратора."),
        h("p", { class: "hint" }, "В Program Files\\Zeno ляжет служба virtualZeno: она держит адаптер и запускается вместе с Windows. Брандмауэр пропустит входящие только с адресов virtualZeno (10.77.x.y) - друг увидит ваши игры и общие папки, пока сеть включена."),
        h("p", { class: "hint" }, "Убрать адаптер и службу можно в Настройки → Соединение."),
        err,
      ],
      foot: [h("button", { class: "btn ghost", onclick: () => m.close() }, "Отмена"), go],
      onClose: () => resolve(false),
    });
    go.onclick = async () => {
      go.disabled = true;
      go.textContent = "Жду разрешения…";
      err.textContent = "";
      try {
        await api("lan/install");
        resolve(true);
        m.close();
      } catch (e) {
        err.textContent = e.message;
        go.disabled = false;
        go.textContent = "Попробовать снова";
      }
    };
  });
}

async function lanStart(id) {
  if (lanOn(id)) return;
  try {
    if (!(await lanReady())) return;
  } catch (e) { fail(e); return; }
  S.media.lanOn = [...S.media.lanOn, id];
  saveMedia();
  lanWant();
  lanAnnounce(id);
  sfx.play("connect");
  await lanTick();
}

function lanExit(id) {
  if (!lanOn(id)) return;
  S.media.lanOn = S.media.lanOn.filter((x) => x !== id);
  saveMedia();
  lanAnnounce(id);
  api("lan/close", { friend: id }).catch(() => {});
  lanWant();
  lanTick();
}

async function onLanMessage(id, m) {
  const p = peerOf(id);
  switch (m.k) {
    case "lan-state": {
      const was = S.lanRemote.get(id);
      S.lanRemote.set(id, !!m.on);
      if (!m.on) api("lan/close", { friend: id }).catch(() => {});
      else if (!was && !lanOn(id)) toast(`${fr(id).nick} запустил virtualZeno - присоединяйтесь в чате с ним`, "ok");
      await lanTick();
      break;
    }
    case "lan-offer": {
      if (!lanOn(id) || typeof m.sdp !== "string") {
        lanAnnounce(id); // мы не запускали - друг узнает и не будет ждать
        break;
      }
      const r = await api("lan/accept", { friend: id, sdp: m.sdp, ice: iceServers() });
      p.send({ k: "lan-answer", sdp: r.sdp });
      break;
    }
    case "lan-answer":
      if (typeof m.sdp === "string") await api("lan/answer", { friend: id, sdp: m.sdp }).catch(() => {});
      break;
  }
}

async function lanOffer(id) {
  const r = await api("lan/offer", { friend: id, ice: iceServers() });
  const p = peerOf(id);
  if (p && lanOn(id)) p.send({ k: "lan-offer", sdp: r.sdp });
}

// lanTick: состояние сети для полоски в чате; соединение упало или застряло - предлагаем снова
async function lanTick() {
  const ids = S.friends.map((f) => f.id).filter((id) => lanOn(id) || S.lanRemote.get(id));
  if (!ids.length && !S.lanSt) return;
  try { S.lanSt = await api("lan/status", { friends: ids }); } catch (e) { return; }
  for (const id of ids) {
    if (!lanOn(id) || !S.lanRemote.get(id) || !isConnected(id) || S.me.id > id) continue;
    const st = (S.lanSt.links[id] || {}).state || "none";
    const at = S.lanOfferAt.get(id) || 0;
    const dead = ["none", "failed", "closed"].includes(st);
    const stuck = st !== "connected" && st !== "disconnected" && Date.now() - at > 15000;
    if ((!dead && !stuck) || Date.now() - at < 8000) continue;
    S.lanOfferAt.set(id, Date.now());
    lanOffer(id).catch((e) => console.warn("virtualZeno:", e));
  }
  if (!ids.length) S.lanSt = null;
  renderLanbar();
}

setInterval(lanTick, 2000);

const LAN_STATES = {
  connected: "соединено", connecting: "соединяюсь…", new: "соединяюсь…", checking: "соединяюсь…",
  disconnected: "связь прерывается…", failed: "нет связи, пробую снова", closed: "нет связи, пробую снова", none: "соединяюсь…",
};

// lanLine - что сейчас с virtualZeno у этого друга: { text, on, remote }
function lanLine(id) {
  const f = fr(id);
  const on = lanOn(id);
  const remote = !!S.lanRemote.get(id);
  const st = S.lanSt;
  const link = (st && st.links[id]) || {};
  const me = st ? st.me : "";
  let text;
  if (on && st && st.error) text = `Ошибка: ${esc(st.error)}`;
  else if (on && remote) {
    const ping = link.state === "connected" && link.rtt ? ` · пинг ${link.rtt < 10 ? link.rtt.toFixed(1) : Math.round(link.rtt)} мс` : "";
    text = `Вы <b>${me}</b> · ${esc(f.nick)} <b>${link.addr || ""}</b> · ${LAN_STATES[link.state] || link.state || ""}${ping}`;
    if (st && !st.up) text += " · поднимаю адаптер…";
  } else if (on) text = `Ваш адрес <b>${me}</b>. Ждем, когда <b>${esc(f.nick)}</b> тоже запустит virtualZeno.`;
  else if (remote) text = `<b>${esc(f.nick)}</b> запустил virtualZeno - локальную сеть для игр и общих папок.`;
  return { text, on, remote, addr: link.addr };
}

function renderLanbar() {
  const bar = $("#lanbar");
  if (!bar) return;
  const id = S.view === "dm" ? S.sel : null;
  const l = id && fr(id) ? lanLine(id) : null;
  bar.hidden = !l || (!l.on && !l.remote);
  if (bar.hidden) return;
  const btns = [];
  if (l.on && l.remote && l.addr) {
    btns.push(h("button", { class: "btn small ghost", title: "Скопировать адрес друга", html: icon("copy", 16), onclick: () => copyText(l.addr).then(() => toast("Адрес скопирован", "ok")) }));
  }
  btns.push(l.on
    ? h("button", { class: "btn small", onclick: () => lanExit(id) }, "Выйти")
    : h("button", { class: "btn small green", onclick: () => lanStart(id) }, "Присоединиться"));
  bar.replaceChildren(h("span", { class: "lan-ico", html: icon("lan", 18) }), h("div", { class: "lb-text", html: `<b>virtualZeno</b> · ${l.text}` }), ...btns);
}

// openApps: окно приложений плитками (как активности в Discord). Клик по
// плитке открывает под сеткой описание и кнопку запуска. Открывается и из
// шапки чата, и из панели звонка.
function openApps(id, initial) {
  const f = fr(id);
  if (!f) return;
  let pick = APPS.some((a) => a.id === initial) ? initial : APPS[0].id;
  const grid = h("div", { class: "apps-tiles" });
  const detail = h("div", { class: "app-detail" });
  const m = modal({ title: "Приложения", sub: `Запускаются вдвоем с ${f.nick}`, body: [grid, detail], wide: true });
  m.box.classList.add("apps-modal");
  const state = (a) => {
    const l = a.line(id);
    if (a.badge) return { l, ...a.badge(id, l) };
    if (l.on && l.remote) return { l, badge: "Запущено", cls: "on" };
    if (l.on) return { l, badge: "Ждем " + f.nick, cls: "wait" };
    if (l.remote) return { l, badge: f.nick + " ждет", cls: "invite" };
    return { l, badge: "", cls: "" };
  };
  const draw = () => {
    grid.replaceChildren(...APPS.map((a) => {
      const st = state(a);
      return h("button", { class: "app-tile" + (a.id === pick ? " sel" : ""), onclick: () => { pick = a.id; draw(); } },
        h("div", { class: "app-tile-art", style: { background: a.color }, html: a.art || icon(a.icon, 44) },
          st.badge ? h("span", { class: "app-badge " + st.cls }, st.badge) : null),
        h("div", { class: "app-tile-name" }, a.name),
        h("div", { class: "app-tile-tag" }, a.tag));
    }), h("div", { class: "app-tile soon", "aria-hidden": "true" },
      h("div", { class: "app-tile-art", html: icon("plus", 32) }),
      h("div", { class: "app-tile-name" }, "Скоро"),
      h("div", { class: "app-tile-tag" }, "новые приложения")));
    const a = APPS.find((x) => x.id === pick);
    const { l } = state(a);
    // свои кнопки и панель (a.buttons, a.panel) - у приложений, где «запустить» мало
    const btn = a.buttons ? a.buttons(id, draw) : l.on
      ? h("button", { class: "btn", onclick: () => { a.exit(id); draw(); } }, "Выйти")
      : h("button", { class: "btn " + (l.remote ? "green" : "primary"), onclick: async () => { await a.start(id); if (a.line(id).on) m.close(); } }, l.remote ? "Присоединиться" : "Запустить");
    detail.replaceChildren(
      h("div", { class: "app-detail-head" },
        h("div", { class: "app-detail-art", style: { background: a.color }, html: a.art || icon(a.icon, 26) }),
        h("div", { class: "app-detail-title" }, h("div", { class: "app-name" }, a.name), h("div", { class: "app-tile-tag" }, a.tag)),
        btn),
      h("div", { class: "app-desc" }, a.desc),
      ...(l.text && !a.panel ? [h("div", { class: "app-state", html: l.text })] : []),
      ...(a.panel ? [a.panel(id, draw)] : []));
  };
  draw();
}

$("#dmApps").onclick = () => openApps(S.sel);
$("#sbApps").onclick = () => { if (S.call) openApps(S.call.id); };

// ---------- ЯГессер ----------
// Игра из internal/yaguessr открывается окном поверх чата (/yg/ этого же
// Zeno). Хост создает комнату у себя и шлет другу yg-invite. Окно игры гостя
// вместо WebSocket говорит с нами (postMessage), мы - с хостом по каналу
// данных: yg-open (подключи меня), yg-ws (сообщения игры и закрытие), а
// страница хоста держит за гостя WebSocket к своему /yg/ws?as=<ID гостя>.

S.yg = null; // { friend, role: "host"|"guest", room, el, frame, min }
S.ygInvite = new Map(); // id -> код комнаты, куда друг зовет
S.ygRelay = new Map(); // хост: id гостя -> WebSocket к своей комнате

function ygLine(id) {
  const f = fr(id);
  const g = S.yg && S.yg.friend === id ? S.yg : null;
  const on = !!g;
  const remote = g ? (g.role === "guest" || S.ygRelay.has(id)) : S.ygInvite.has(id);
  let text = "";
  if (g && g.role === "host") text = remote ? `Комната <b>${esc(g.room || "")}</b>, <b>${esc(f.nick)}</b> в игре.` : `Комната <b>${esc(g.room || "…")}</b>. Ждем, когда <b>${esc(f.nick)}</b> присоединится.`;
  else if (g) text = `Вы в комнате <b>${esc(f.nick)}</b>.`;
  else if (remote) text = `<b>${esc(f.nick)}</b> зовет в ЯГессер.`;
  return { on, remote, text };
}

function ygAnnounce(id) {
  const p = peerOf(id);
  if (p && S.yg && S.yg.role === "host" && S.yg.friend === id && S.yg.room) p.send({ k: "yg-invite", room: S.yg.room });
}

async function ygOpen(id, role, room) {
  ygClose();
  await api("yg/open"); // печенье для окна игры
  const f = fr(id);
  const frame = h("iframe", { class: "yg-frame", allow: "fullscreen; clipboard-write", src: "/yg/?zeno=" + role + (room ? "&room=" + encodeURIComponent(room) : "") });
  const g = { friend: id, role, room, frame, min: false };
  g.el = h("div", { class: "yg-overlay" },
    h("div", { class: "yg-head" },
      h("span", { class: "yg-ico", html: icon("globe", 18) }),
      h("b", {}, "ЯГессер"), h("span", { class: "yg-with" }, "с " + f.nick),
      h("div", { class: "spacer" }),
      h("button", { class: "btn small ghost", onclick: () => ygMin(true) }, "Свернуть"),
      h("button", { class: "btn small", onclick: () => ygClose() }, "Выйти")),
    frame);
  S.yg = g;
  $(".main").append(g.el);
  renderYgbar();
}

async function ygStart(id) {
  const room = S.ygInvite.get(id);
  try {
    await ygOpen(id, room ? "guest" : "host", room);
  } catch (e) { fail(e); }
}

function ygMin(min) {
  if (!S.yg) return;
  S.yg.min = min;
  S.yg.el.classList.toggle("min", min);
  renderYgbar();
}

function ygClose() {
  const g = S.yg;
  if (!g) return;
  S.yg = null;
  const p = peerOf(g.friend);
  if (g.role === "host") {
    for (const [fid, ws] of S.ygRelay) {
      S.ygRelay.delete(fid);
      try { ws.close(); } catch (e) { /* уже закрыт */ }
    }
    if (p) p.send({ k: "yg-end" });
  } else if (p) p.send({ k: "yg-ws", e: "close" });
  g.el.remove();
  renderYgbar();
}

function toYgFrame(msg) {
  if (S.yg && S.yg.frame.contentWindow) S.yg.frame.contentWindow.postMessage(msg, location.origin);
}

// сообщения из окна игры
window.addEventListener("message", (e) => {
  const g = S.yg;
  if (!g || e.origin !== location.origin || e.source !== g.frame.contentWindow || !e.data) return;
  const d = e.data;
  const p = peerOf(g.friend);
  if (d.yg === "room" && g.role === "host") {
    g.room = d.code;
    ygAnnounce(g.friend);
    renderYgbar();
  } else if (d.yg === "leave") ygClose();
  else if (g.role === "guest" && d.ygws) {
    if (!p || !p.connected) {
      if (d.ygws === "connect") toYgFrame({ ygws: "close" });
      return;
    }
    if (d.ygws === "connect") p.send({ k: "yg-open", room: g.room });
    else if (d.ygws === "send") p.send({ k: "yg-ws", e: "msg", d: String(d.data) });
    else if (d.ygws === "close") p.send({ k: "yg-ws", e: "close" });
  }
});

function onYgMessage(id, m) {
  const f = fr(id);
  const g = S.yg && S.yg.friend === id ? S.yg : null;
  const p = peerOf(id);
  switch (m.k) {
    case "yg-invite":
      if (typeof m.room !== "string") break;
      if (!S.ygInvite.has(id) && !g) toast(`${f.nick} зовет в ЯГессер - присоединяйтесь в чате с ним`, "ok");
      S.ygInvite.set(id, m.room);
      break;
    case "yg-end":
      S.ygInvite.delete(id);
      if (g && g.role === "guest") {
        ygClose();
        toast(`${f.nick} закрыл ЯГессер`);
      }
      break;
    case "yg-open": {
      // гость просит подключить его к нашей комнате
      if (!g || g.role !== "host") {
        p.send({ k: "yg-end" });
        break;
      }
      const old = S.ygRelay.get(id);
      if (old) old.close();
      const ws = new WebSocket("ws://" + location.host + "/yg/ws?as=" + encodeURIComponent(id));
      S.ygRelay.set(id, ws);
      ws.onopen = () => p.send({ k: "yg-ws", e: "open" });
      ws.onmessage = (e) => p.send({ k: "yg-ws", e: "msg", d: e.data });
      ws.onclose = () => {
        if (S.ygRelay.get(id) !== ws) return;
        S.ygRelay.delete(id);
        p.send({ k: "yg-ws", e: "close" });
        renderYgbar();
      };
      break;
    }
    case "yg-ws":
      if (g && g.role === "guest") toYgFrame({ ygws: m.e, data: m.d });
      else if (S.ygRelay.has(id)) {
        const ws = S.ygRelay.get(id);
        if (m.e === "msg" && ws.readyState === WebSocket.OPEN) ws.send(String(m.d));
        else if (m.e === "close") {
          S.ygRelay.delete(id);
          ws.close();
        }
      }
      break;
  }
  renderYgbar();
}

function renderYgbar() {
  const bar = $("#ygbar");
  if (!bar) return;
  const id = S.view === "dm" ? S.sel : null;
  const l = id && fr(id) ? ygLine(id) : null;
  const g = S.yg;
  bar.hidden = !l || (!l.on && !l.remote);
  if (bar.hidden) return;
  const btns = [];
  if (l.on && g.min) btns.push(h("button", { class: "btn small primary", onclick: () => ygMin(false) }, "Открыть"));
  btns.push(l.on
    ? h("button", { class: "btn small", onclick: () => ygClose() }, "Выйти")
    : h("button", { class: "btn small green", onclick: () => ygStart(id) }, "Присоединиться"));
  bar.replaceChildren(h("span", { class: "lan-ico", html: icon("globe", 18) }), h("div", { class: "lb-text", html: `<b>ЯГессер</b> · ${l.text}` }), ...btns);
}

// ---------- TarBitTorrent ----------
// Большие файлы другу (internal/btshare). Отдающий выбирает файл или папку,
// Zeno хеширует их и шлет другу bt-offer (хеш, имя, размер, свои прямые
// адреса). Друг выбирает папку и качает, его адреса уходят в bt-get. Кроме
// прямых адресов движок ходит через соединение pion в Zeno (bt-link-offer ->
// bt-link-answer), его предлагает тот, у кого ключ меньше. bt-drop - раздачу
// остановили или от нее отказались.

S.bt = { items: [], links: {}, addrs: [] }; // последний ответ bt/list
S.btOffers = new Map(); // id друга -> Map(ih -> { ih, name, size, addrs })
S.btLinkAt = new Map(); // id друга -> когда предлагали соединение
S.btPanels = new Set(); // открытые списки передач (перерисовать по таймеру)

const BT_STATE = {
  hashing: "считаю контрольные суммы", seeding: "раздаю", metadata: "получаю список файлов",
  downloading: "качаю", done: "скачано", error: "ошибка",
};
const btMine = (id) => S.bt.items.filter((x) => x.friend === id);
const btBusy = (x) => ["hashing", "metadata", "downloading"].includes(x.state);

function btLine(id) {
  const f = fr(id);
  const mine = btMine(id);
  const offers = S.btOffers.get(id);
  const pending = offers ? [...offers.values()].filter((o) => !mine.some((x) => x.ih === o.ih)) : [];
  let text = "";
  if (pending.length) text = `<b>${esc(f.nick)}</b> предлагает: ` + pending.map((o) => `<b>${esc(o.name)}</b> (${fmtSize(o.size)})`).join(", ");
  else if (mine.some(btBusy)) text = mine.filter(btBusy).map(btItemText).join("; ");
  return { on: mine.length > 0, remote: pending.length > 0, text, pending };
}

function btItemText(x) {
  let t = `<b>${esc(x.name)}</b> · ${BT_STATE[x.state] || x.state}`;
  if (x.state === "hashing" || x.state === "downloading") t += ` ${Math.floor(x.progress * 100)}%`;
  if (x.speed > 1024 && (x.state === "downloading" || x.state === "seeding")) t += ` · ${fmtSize(x.speed)}/с`;
  if (x.via && x.state !== "done") t += x.via === "zeno" ? " · через Zeno" : " · напрямую";
  if (x.error) t += ` · ${esc(x.error)}`;
  return t;
}

function btOfferTo(p, x) {
  p.send({ k: "bt-offer", ih: x.ih, name: x.name, size: x.size, addrs: S.bt.addrs || [] });
}

// btAnnounce: друг на связи - напоминаем о своих раздачах (он отсеет уже скачанные)
function btAnnounce(id) {
  const p = peerOf(id);
  if (!p) return;
  for (const x of btMine(id)) if (x.role === "send" && x.state === "seeding") btOfferTo(p, x);
}

async function btSend(id, folder) {
  try {
    const r = await api("bt/pick", { folder });
    if (!r.path) return;
    await api("bt/share", { friend: id, path: r.path });
    toast("Считаю контрольные суммы, потом предложу другу", "ok");
    await btTick();
  } catch (e) { fail(e); }
}

async function btAccept(id, ih) {
  const map = S.btOffers.get(id);
  const o = map && map.get(ih);
  if (!o) return;
  try {
    const r = await api("bt/pick", { folder: true, for: "save" });
    if (!r.path) return;
    const g = await api("bt/get", { friend: id, ih, name: o.name, size: o.size, dir: r.path, addrs: o.addrs });
    const p = peerOf(id);
    if (p) p.send({ k: "bt-get", ih, addrs: g.addrs || [] });
    map.delete(ih);
    await btTick();
  } catch (e) { fail(e); }
}

function btDecline(id, ih) {
  const map = S.btOffers.get(id);
  if (map) map.delete(ih);
  const p = peerOf(id);
  if (p) p.send({ k: "bt-drop", ih, why: "decline" });
  btTick();
}

async function btRemove(id, x) {
  if (x.role === "send" && x.state === "seeding" && !confirm(`Остановить раздачу «${x.name}»? Друг не сможет докачать. Файл на диске останется.`)) return;
  await api("bt/remove", { ih: x.ih }).catch(fail);
  const p = peerOf(id);
  if (p && !x.ih.startsWith("hashing-")) p.send({ k: "bt-drop", ih: x.ih });
  btTick();
}

async function onBtMessage(id, m) {
  const f = fr(id);
  const p = peerOf(id);
  const addrs = Array.isArray(m.addrs) ? m.addrs.filter((a) => typeof a === "string").slice(0, 16) : [];
  switch (m.k) {
    case "bt-offer": {
      if (typeof m.ih !== "string" || !/^[0-9a-f]{40}$/.test(m.ih)) break;
      if (S.bt.items.some((x) => x.ih === m.ih)) { // уже качаем - адреса могли смениться
        api("bt/peers", { ih: m.ih, addrs }).catch(() => {});
        break;
      }
      let map = S.btOffers.get(id);
      if (!map) S.btOffers.set(id, (map = new Map()));
      const o = { ih: m.ih, name: String(m.name || "файл").slice(0, 255), size: Math.max(0, Number(m.size) || 0), addrs };
      if (!map.has(m.ih)) toast(`${f.nick} предлагает «${o.name}» (${fmtSize(o.size)}) - TarBitTorrent в чате с ним`, "ok");
      map.set(m.ih, o);
      break;
    }
    case "bt-get":
      if (typeof m.ih === "string") await api("bt/peers", { ih: m.ih, addrs }).catch(() => {});
      break;
    case "bt-drop": {
      const map = S.btOffers.get(id);
      if (map && map.delete(m.ih)) toast(`${f.nick} остановил раздачу`);
      const x = S.bt.items.find((y) => y.ih === m.ih && y.friend === id);
      if (x && m.why === "decline") toast(`${f.nick} не стал качать «${x.name}»`);
      break;
    }
    case "bt-link-offer": {
      if (typeof m.sdp !== "string") break;
      const r = await api("bt/link-accept", { friend: id, sdp: m.sdp, ice: iceServers() });
      p.send({ k: "bt-link-answer", sdp: r.sdp });
      break;
    }
    case "bt-link-answer":
      if (typeof m.sdp === "string") await api("bt/link-answer", { friend: id, sdp: m.sdp }).catch(() => {});
      break;
  }
  renderBtbar();
  btRefreshPanels();
}

// btTick: состояние передач; новые раздачи - предложить другу; соединение через Zeno
async function btTick() {
  let r;
  try { r = await api("bt/list", { friends: S.friends.map((f) => f.id) }); } catch (e) { return; }
  S.bt = r;
  for (const x of r.items) {
    const p = peerOf(x.friend);
    if (x.role === "send" && x.state === "seeding" && !x.offered && p && p.connected) {
      btOfferTo(p, x);
      api("bt/offered", { ih: x.ih }).catch(() => {});
    }
  }
  const need = new Set(r.items.filter((x) => x.state !== "error" && x.state !== "hashing" && !(x.role === "get" && x.state === "done")).map((x) => x.friend));
  for (const id of need) {
    if (!isConnected(id) || S.me.id > id) continue;
    const st = r.links[id] || "none";
    const at = S.btLinkAt.get(id) || 0;
    const dead = ["none", "failed", "closed"].includes(st);
    const stuck = st !== "connected" && st !== "disconnected" && Date.now() - at > 20000;
    if ((!dead && !stuck) || Date.now() - at < 10000) continue;
    S.btLinkAt.set(id, Date.now());
    api("bt/link-offer", { friend: id, ice: iceServers() }).then((o) => {
      const p = peerOf(id);
      if (p) p.send({ k: "bt-link-offer", sdp: o.sdp });
    }).catch((e) => console.warn("TarBitTorrent:", e));
  }
  renderBtbar();
  btRefreshPanels();
}

setInterval(btTick, 2000);

function btRefreshPanels() {
  for (const fill of [...S.btPanels]) fill();
}

function btButtons(id) {
  return h("div", { class: "bt-btns" },
    h("button", { class: "btn primary", onclick: () => btSend(id, false) }, "Отправить файл"),
    h("button", { class: "btn", onclick: () => btSend(id, true) }, "Папку"));
}

// btPanel - список передач с другом в окне приложений; обновляется btTick
function btPanel(id) {
  const box = h("div", { class: "bt-list" });
  let drawn = false;
  const fill = () => {
    if (drawn && !box.isConnected) {
      S.btPanels.delete(fill);
      return;
    }
    drawn = true;
    const f = fr(id);
    const rows = btLine(id).pending.map((o) => h("div", { class: "bt-row" },
      h("span", { class: "bt-dir in", html: icon("download", 16) }),
      h("div", { class: "bt-main" }, h("div", { class: "bt-name" }, o.name), h("div", { class: "bt-sub" }, `${fmtSize(o.size)} · предлагает ${f.nick}`)),
      h("button", { class: "btn small green", onclick: () => btAccept(id, o.ih) }, "Скачать"),
      h("button", { class: "btn small ghost", onclick: () => btDecline(id, o.ih) }, "Отклонить")));
    for (const x of btMine(id).sort((a, b) => b.added - a.added)) {
      rows.push(h("div", { class: "bt-row" },
        h("span", { class: "bt-dir " + (x.role === "send" ? "out" : "in"), html: icon(x.role === "send" ? "upload" : "download", 16) }),
        h("div", { class: "bt-main" },
          h("div", { class: "bt-name" }, x.name),
          h("div", { class: "bt-sub", html: `${fmtSize(x.size)} · ` + btItemText(x).replace(/^<b>.*?<\/b> · /, "") }),
          btBusy(x) ? h("div", { class: "bt-bar" }, h("i", { style: { width: (x.progress * 100).toFixed(1) + "%" } })) : null),
        x.state === "error" || x.state === "hashing" ? null
          : h("button", { class: "btn small ghost", title: "Показать в папке", html: icon("folder", 16), onclick: () => api("bt/reveal", { ih: x.ih }).catch(fail) }),
        h("button", { class: "btn small ghost", title: x.role === "send" ? "Остановить раздачу (файл останется)" : "Убрать из списка (файлы останутся)", html: icon("x", 16), onclick: () => btRemove(id, x) })));
    }
    box.replaceChildren(...(rows.length ? rows : [h("div", { class: "hint" }, "Передач пока нет. Выберите файл или папку - друг получит предложение скачать.")]));
  };
  S.btPanels.add(fill);
  fill();
  return box;
}

function renderBtbar() {
  const bar = $("#btbar");
  if (!bar) return;
  const id = S.view === "dm" ? S.sel : null;
  const l = id && fr(id) ? btLine(id) : null;
  bar.hidden = !l || !l.text;
  if (bar.hidden) return;
  const btns = [];
  if (l.remote) {
    const o = l.pending[0];
    btns.push(h("button", { class: "btn small green", onclick: () => btAccept(id, o.ih) }, "Скачать"),
      h("button", { class: "btn small ghost", onclick: () => btDecline(id, o.ih) }, "Отклонить"));
  }
  btns.push(h("button", { class: "btn small", onclick: () => openApps(id, "bt") }, "Подробнее"));
  bar.replaceChildren(h("span", { class: "lan-ico", html: icon("download", 18) }), h("div", { class: "lb-text", html: `<b>TarBitTorrent</b> · ${l.text}` }), ...btns);
}

// nativeTick: счетчики своего захвата для статистики; источник пропал (окно закрыли) - стоп
async function nativeTick() {
  if (!S.native) return;
  const r = await api("nscreen/stats").catch(() => null);
  if (!r || !S.native) return;
  if (!r.active || r.ended) {
    toast("Источник трансляции закрылся", "err");
    stopScreen();
    return;
  }
  const now = performance.now();
  const prev = S.nsStats;
  const st = r.stats;
  const dt = prev ? (now - prev.at) / 1000 : 0;
  S.nsStats = {
    at: now, st,
    fps: dt ? (st.Frames - prev.st.Frames) / dt : 0,
    cap: dt ? (st.Captured - prev.st.Captured) / dt : 0,
    bits: dt ? ((st.Bytes - prev.st.Bytes) * 8) / dt : 0,
  };
}

// Удержание частоты кадров в режиме «Плавность»: если захват дает полную
// частоту, а наружу уходит меньше (кодеру не хватает ресурсов или канала), шаг
// за шагом снижаем разрешение, но не ниже 720p. Когда долго все ровно -
// возвращаем ступень; если после возврата снова просело, следующий возврат ждем дольше.
const FPS_STEPS = [1, 1.25, 1.5, 2];
const keeper = { step: 0, low: 0, good: 0, upWait: 20, upAt: 0, minH: 0, since: 0 };

function keepFps(st) {
  if (!S.screen || S.native || S.media.screenMode !== "motion") return;
  const o = st.out.screen;
  // первые секунды кодер и канал разгоняются - кадров меньше и без нехватки ресурсов
  if (!o || o.framesPerSecond == null || Date.now() - keeper.since < 8000) return;
  const want = (SCREEN_PRESETS[S.media.screenPreset] || SCREEN_PRESETS["1080p60"]).fps;
  const src = st.src && st.src.screen;
  const cap = src && src.framesPerSecond != null ? src.framesPerSecond : want;
  // захват сам дает мало кадров (картинка стоит или окно так рисуется) - разрешение не поможет
  if (cap < want * 0.85) {
    keeper.low = keeper.good = 0;
    return;
  }
  if (o.framesPerSecond < want * 0.85) {
    keeper.good = 0;
    const next = FPS_STEPS[keeper.step + 1];
    if (++keeper.low >= 3 && next && keeper.minH / next >= 719) {
      if (Date.now() - keeper.upAt < 30000) keeper.upWait = Math.min(keeper.upWait * 2, 240);
      keeper.step++;
      keeper.low = 0;
      tuneScreen(true).catch(() => {});
    }
    return;
  }
  keeper.low = 0;
  if (keeper.step > 0 && o.framesPerSecond >= want * 0.95 && ++keeper.good >= keeper.upWait) {
    keeper.step--;
    keeper.good = 0;
    keeper.upAt = Date.now();
    tuneScreen(true).catch(() => {});
  }
}

// tuneScreen(keepStep): keepStep - вызов от удержания кадров, ступень не сбрасывать
async function tuneScreen(keepStep) {
  if (!S.screen || !S.call) return;
  if (keepStep !== true) Object.assign(keeper, { step: 0, low: 0, good: 0, upWait: 20, upAt: 0, since: Date.now() });
  const p = peerOf(S.call.id);
  const pr = SCREEN_PRESETS[S.media.screenPreset] || SCREEN_PRESETS["1080p60"];
  const v = S.screen.getVideoTracks()[0];
  const motion = S.media.screenMode === "motion";
  v.contentHint = motion ? "motion" : "detail";
  if (S.native) {
    // картинку шлет программа: разрешение, частота и битрейт меняются сразу, без перезапуска трансляции
    await api("nscreen/quality", { w: pr.w, h: pr.h, fps: pr.fps, kbps: pr.kbps }).catch(() => {});
    if (S.screen.getAudioTracks().length && S.media.screenAudio) await p.tuneSender("sys", { maxBitrate: 128000 });
    return;
  }
  try { await v.applyConstraints({ width: { max: pr.w }, height: { max: pr.h }, frameRate: { ideal: pr.fps, max: pr.fps } }); } catch (e) { /* источник не умеет */ }
  const st = v.getSettings();
  const base = Math.max(1, (st.width || pr.w) / pr.w, (st.height || pr.h) / pr.h);
  keeper.minH = (st.height || pr.h) / base;
  const scale = base * (motion ? FPS_STEPS[keeper.step] : 1);
  // для игр - держим и кадры, и разрешение: при нехватке канала падает только
  // четкость. balanced при любом упоре (разгон канала, скачок времени кодирования)
  // резал до 25-30 к/с и снижал разрешение, maintain-framerate скатывался до 320x180
  await p.tuneSender("screen", { maxBitrate: pr.kbps * 1000, maxFramerate: pr.fps, scaleResolutionDownBy: scale },
    motion ? "maintain-framerate-and-resolution" : "maintain-resolution");
  if (S.screen.getAudioTracks().length && S.media.screenAudio) await p.tuneSender("sys", { maxBitrate: 128000 });
}

function stopScreen(silent) {
  if (!S.screen) return;
  stopNative();
  S.screen.getTracks().forEach((t) => t.stop());
  S.screen = null;
  api("capturebar", { active: false }).catch(() => {});
  if (S.focus === "myscreen") S.focus = null;
  const p = S.call && peerOf(S.call.id);
  if (p) {
    p.setTrack("screen", null).catch(() => {});
    p.setTrack("sys", null).catch(() => {});
  }
  if (!silent) {
    sendState();
    renderAll();
  }
}

const toggleCam = () => (S.cam ? stopCam() : startCam().catch(fail));
const toggleScreen = () => (S.screen ? stopScreen() : startScreen().catch(fail));
$("#vpCam").onclick = toggleCam;
$("#sbCam").onclick = toggleCam;
$("#vpScreen").onclick = toggleScreen;
$("#sbScreen").onclick = toggleScreen;

$("#sbScreenOpts").onclick = (e) => {
  const items = [h("div", { class: "menu-title" }, "Качество")];
  for (const [k, pr] of Object.entries(SCREEN_PRESETS)) {
    items.push(menuItem(pr.label, S.media.screenPreset === k, () => setMedia({ screenPreset: k }, tuneScreen)));
  }
  items.push(h("div", { class: "menu-sep" }), h("div", { class: "menu-title" }, "Режим"),
    menuItem("Плавность (игры, видео)", S.media.screenMode === "motion", () => setMedia({ screenMode: "motion" }, tuneScreen)),
    menuItem("Четкость (текст, код)", S.media.screenMode === "detail", () => setMedia({ screenMode: "detail" }, tuneScreen)),
    h("div", { class: "menu-sep" }),
    menuItem("Звук системы", S.media.screenAudio, () => setMedia({ screenAudio: !S.media.screenAudio }, applyScreenAudio)));
  openMenu(e.currentTarget, items, "up");
};

// applyScreenAudio: звук трансляции захвачен всегда, галочка только включает его отправку
async function applyScreenAudio() {
  if (!S.screen || !S.call) return;
  const a = S.screen.getAudioTracks()[0];
  if (!a) {
    if (S.media.screenAudio) toast("Этот источник не отдает звук системы", "err");
    return;
  }
  const p = peerOf(S.call.id);
  await p.setTrack("sys", S.media.screenAudio ? a : null);
  if (S.media.screenAudio) await p.tuneSender("sys", { maxBitrate: 128000 });
}

function menuItem(label, on, fn, cls = "") {
  return h("button", { class: "menu-item" + (on ? " on" : "") + (cls ? " " + cls : ""), onclick: () => { closeMenu(); fn(); } }, label);
}

// menuSlider - ползунок громкости 0-300% прямо в меню (как в Discord); двойной клик - 100%
function menuSlider(label, value, onInput) {
  const out = h("output", {}, value + "%");
  const r = h("input", { type: "range", min: "0", max: "300", step: "1", value: String(value) });
  const paint = () => {
    r.style.setProperty("--p", (r.value / 3) + "%");
    out.textContent = r.value + "%";
    out.classList.toggle("boost", Number(r.value) > 100);
  };
  r.oninput = () => { paint(); onInput(Number(r.value)); };
  r.ondblclick = () => { r.value = "100"; paint(); onInput(100); };
  paint();
  return h("div", { class: "menu-slider", onmousedown: (e) => e.stopPropagation(), onclick: (e) => e.stopPropagation() },
    h("div", { class: "ms-head" }, h("span", {}, label), out), r,
    h("div", { class: "ms-marks" }, h("span", {}, "0"), h("span", {}, "100"), h("span", {}, "200"), h("span", {}, "300")));
}

function openMenuAt(x, y, items) {
  closeMenu();
  menuEl = h("div", { class: "menu" }, items);
  document.body.append(menuEl);
  const mr = menuEl.getBoundingClientRect();
  menuEl.style.left = Math.max(8, Math.min(window.innerWidth - mr.width - 8, x)) + "px";
  menuEl.style.top = Math.max(8, Math.min(window.innerHeight - mr.height - 8, y)) + "px";
  setTimeout(() => document.addEventListener("mousedown", outside), 0);
}

let menuEl = null;

// openMenu: меню у кнопки; повторный клик по той же кнопке закрывает его
function openMenu(anchor, items, dir) {
  const again = menuEl && menuEl.anchor === anchor;
  closeMenu();
  if (again) return;
  menuEl = h("div", { class: "menu" }, items);
  menuEl.anchor = anchor;
  document.body.append(menuEl);
  const r = anchor.getBoundingClientRect();
  const mr = menuEl.getBoundingClientRect();
  menuEl.style.left = Math.max(8, Math.min(window.innerWidth - mr.width - 8, r.left + r.width / 2 - mr.width / 2)) + "px";
  menuEl.style.top = (dir === "up" ? r.top - mr.height - 8 : r.bottom + 8) + "px";
  setTimeout(() => document.addEventListener("mousedown", outside), 0);
}

function outside(e) {
  // клик по кнопке, открывшей меню, решает ее обработчик (закрыть повторным нажатием)
  if (menuEl && !menuEl.contains(e.target) && !(menuEl.anchor && menuEl.anchor.contains(e.target))) closeMenu();
}

function closeMenu() {
  if (menuEl) menuEl.remove();
  menuEl = null;
  document.removeEventListener("mousedown", outside);
}

// ---------- сцена звонка ----------

function videoFor(key, track, opts = {}) {
  let v = videos.get(key);
  if (!v) {
    v = h("video", { autoplay: true, playsinline: true });
    v.muted = true;
    videos.set(key, v);
  }
  if (!v.srcObject || v.srcObject.getTracks()[0] !== track) v.srcObject = track ? new MediaStream([track]) : null;
  v.ondblclick = opts.dbl || null;
  setTimeout(() => v.play().catch(() => {}), 0);
  return v;
}

function renderStage() {
  const stage = $("#stage");
  const c = S.call;
  const show = !!c && S.view === "dm" && S.sel === c.id;
  stage.hidden = !show;
  if (!show) {
    if (S.full) setFull(false);
    document.body.classList.remove("theater");
    return;
  }
  const f = fr(c.id);
  const p = peerOf(c.id);
  const rs = S.remote.get(c.id) || {};
  const active = c.state === "active" && p && p.mediaReady;

  const userTile = (who) => {
    const me = who === "me";
    const person = me ? S.me : f;
    const camTrack = me ? (S.cam && S.cam.getVideoTracks()[0]) : (active && rs.cam ? p.remoteTrack("cam") : null);
    const tile = h("div", { class: "tile" + (me ? " mirror" : "") + (camTrack ? " cover" : ""), "data-who": who });
    if (camTrack) tile.append(videoFor(who + "-cam", camTrack));
    else tile.append(avatar(person, 80));
    const mutedNow = me ? (S.muted || S.deaf) : (c.state === "active" && (rs.mic === false || rs.deaf));
    tile.append(h("div", { class: "tile-label" }, mutedNow ? h("span", { html: icon(rs.deaf && !me ? "headOff" : "micOff", 14) }) : null, person.nick));
    if (!me && (c.state === "outgoing" || c.state === "reconnecting")) {
      tile.append(h("div", { class: "waiting" }, c.state === "outgoing" ? "Вызов…" : "Переподключение…"));
    }
    if (!me) {
      const v = vol(f.id);
      if (active && (v.voiceMuted || v.voice !== 100)) tile.append(h("span", { class: "vol-badge" + (v.voiceMuted ? " muted" : "") }, v.voiceMuted ? "звук выкл" : v.voice + "%"));
      tile.oncontextmenu = (e) => userMenu(e, f.id);
    } else {
      tile.oncontextmenu = (e) => meMenu(e);
    }
    return tile;
  };

  const screenTile = (who) => {
    const me = who === "myscreen";
    const track = me ? S.screen.getVideoTracks()[0] : remoteScreen(p);
    const tile = h("div", { class: "tile screen", "data-who": who },
      videoFor(who, track, { dbl: () => setFull(!S.full) }),
      h("span", { class: "live" }, "В ЭФИРЕ"),
      h("div", { class: "tile-label" }, me ? "Ваша трансляция" : `${f.nick} транслирует экран`),
      h("div", { class: "stats", hidden: !S.showStats }),
      h("div", { class: "tile-actions" },
        h("button", { class: "ta-btn", title: cinema() ? "Обычный вид (T)" : "Режим кинотеатра (T)", html: icon(cinema() ? "theaterOff" : "theater", 18),
          onclick: (e) => { e.stopPropagation(); toggleTheater(); } }),
        h("button", { class: "ta-btn", title: S.full ? "Выйти из полноэкранного (Esc)" : "Во весь экран", html: icon(S.full ? "shrink" : "expand", 18),
          onclick: (e) => { e.stopPropagation(); setFull(!S.full); } }),
        h("button", { class: "ta-btn", title: "Еще", html: icon("dots", 18),
          onclick: (e) => { e.stopPropagation(); screenMenu(e, who); } })));
    if (!me) {
      const v = vol(f.id);
      if (v.streamMuted || v.stream !== 100) tile.append(h("span", { class: "vol-badge" + (v.streamMuted ? " muted" : "") }, v.streamMuted ? "звук выкл" : v.stream + "%"));
    }
    tile.oncontextmenu = (e) => screenMenu(e, who);
    return tile;
  };

  const tiles = { them: userTile("them"), me: userTile("me") };
  if (active && rs.screen) tiles.theirscreen = screenTile("theirscreen");
  if (S.screen) tiles.myscreen = screenTile("myscreen");

  const screens = ["theirscreen", "myscreen"].filter((k) => tiles[k]);
  const main = $("#stageMain");
  const strip = $("#stageStrip");
  if (screens.length) {
    let focus = S.focus && tiles[S.focus] ? S.focus : screens[0];
    const big = tiles[focus];
    big.classList.add("focus");
    main.replaceChildren(big);
    strip.replaceChildren(...Object.keys(tiles).filter((k) => k !== focus).map((k) => {
      const t = tiles[k];
      t.classList.add("small");
      t.onclick = () => { S.focus = k; renderStage(); };
      return t;
    }));
  } else {
    main.replaceChildren(tiles.them, tiles.me);
    strip.replaceChildren();
  }
  // участников под трансляцией можно скрыть; во весь экран они скрыты, пока не попросишь
  strip.classList.toggle("collapsed", stripHidden());

  const sbMic = $("#sbMic");
  sbMic.innerHTML = icon(S.muted || S.deaf ? "micOff" : "mic", 20);
  sbMic.classList.toggle("off", S.muted || S.deaf);
  sbMic.title = S.muted || S.deaf ? "Включить микрофон" : "Выключить микрофон";
  const sbCam = $("#sbCam");
  sbCam.innerHTML = icon(S.cam ? "video" : "videoOff", 20);
  sbCam.classList.toggle("on", !!S.cam);
  sbCam.title = S.cam ? "Выключить камеру" : "Включить камеру";
  const sbDeaf = $("#sbDeaf");
  sbDeaf.innerHTML = icon(S.deaf ? "headOff" : "head", 20);
  sbDeaf.classList.toggle("off", S.deaf);
  sbDeaf.title = S.deaf ? "Включить звук" : "Выключить звук";
  const sbScr = $("#sbScreen");
  sbScr.innerHTML = icon(S.screen ? "screenOff" : "screen", 20) + `<span>${S.screen ? "Остановить" : "Экран"}</span>`;
  sbScr.title = S.screen ? "Остановить трансляцию" : "Транслировать экран";
  $("#sbScreenBox").classList.toggle("live", !!S.screen);
  $("#sbScreenBox").classList.toggle("disabled", c.state !== "active");
  $("#sbScreenOpts").textContent = S.media.screenPreset.replace("source", "Исх. ");
  sbCam.disabled = sbScr.disabled = c.state !== "active";
  $("#sbStats").classList.toggle("on", S.showStats);
  $("#sbCollapse").innerHTML = icon(stripHidden() ? "up" : "down", 20);
  $("#sbCollapse").title = stripHidden() ? "Показать участников" : "Скрыть участников";
  $("#sbCollapse").hidden = !screens.length;
  $("#sbFull").innerHTML = icon(S.full ? "shrink" : "expand", 20);
  $("#sbFull").title = S.full ? "Выйти из полноэкранного (Esc)" : "Во весь экран";
  $("#sbTheater").innerHTML = icon(cinema() ? "theaterOff" : "theater", 20);
  $("#sbTheater").classList.toggle("on", cinema());
  $("#sbTheater").title = cinema() ? "Обычный вид (T)" : "Режим кинотеатра (T)";
  applyStageHeight();
}

const isTheater = () => document.body.classList.contains("theater");
// cinema: режим кинотеатра - сцена на все окно приложения, но не на весь монитор
const cinema = () => isTheater() && !S.full;

function setTheater(on) {
  if (!on && S.full) return setFull(false);
  document.body.classList.toggle("theater", on);
  renderStage();
}

// toggleTheater: из полного экрана кнопка кинотеатра возвращает окно, но оставляет сцену на все окно
function toggleTheater() {
  if (S.full) {
    setFull(false);
    return setTheater(true);
  }
  setTheater(!isTheater());
}

// setFull: трансляция на весь экран - окно без рамки на весь монитор + сцена на все окно
function setFull(on) {
  S.full = on;
  document.body.classList.toggle("theater", on);
  api("fullscreen", { on }).catch(() => {});
  renderStage();
}

async function snapshot(who) {
  const v = videos.get(who);
  if (!v || !v.videoWidth) return toast("Кадр еще не пришел", "err");
  const c = document.createElement("canvas");
  c.width = v.videoWidth;
  c.height = v.videoHeight;
  c.getContext("2d").drawImage(v, 0, 0);
  const r = await api("snapshot", { png: c.toDataURL("image/png") });
  toast("Кадр сохранен: " + r.path, "ok");
}

function screenMenu(e, who) {
  e.preventDefault();
  if (!S.call) return;
  const me = who === "myscreen";
  const id = S.call.id;
  const v = vol(id);
  const items = [
    h("div", { class: "menu-title" }, me ? "Ваша трансляция" : "Трансляция " + fr(id).nick),
    menuItem(S.full ? "Выйти из полноэкранного" : "Во весь экран", false, () => setFull(!S.full)),
    menuItem(cinema() ? "Обычный вид" : "Режим кинотеатра", false, toggleTheater),
    menuItem("Статистика", S.showStats, () => { S.showStats = !S.showStats; renderStage(); }),
    menuItem("Сохранить кадр", false, () => snapshot(who).catch(fail)),
  ];
  if (!me) {
    items.push(h("div", { class: "menu-sep" }),
      menuSlider("Громкость трансляции", v.stream, (x) => setVol(id, { stream: x, streamMuted: false })),
      menuItem("Выключить звук трансляции", v.streamMuted, () => setVol(id, { streamMuted: !v.streamMuted })));
  } else {
    items.push(h("div", { class: "menu-sep" }), h("div", { class: "menu-title" }, "Качество"));
    for (const [k, pr] of Object.entries(SCREEN_PRESETS)) {
      items.push(menuItem(pr.label, S.media.screenPreset === k, () => setMedia({ screenPreset: k }, tuneScreen)));
    }
    items.push(
      menuItem("Плавность (игры)", S.media.screenMode === "motion", () => setMedia({ screenMode: "motion" }, tuneScreen)),
      menuItem("Четкость (текст)", S.media.screenMode === "detail", () => setMedia({ screenMode: "detail" }, tuneScreen)),
      h("div", { class: "menu-sep" }),
      menuItem("Остановить трансляцию", false, () => stopScreen(), "danger"));
  }
  openMenuAt(e.clientX, e.clientY, items);
}

function userMenu(e, id) {
  e.preventDefault();
  const f = fr(id);
  if (!f) return;
  const v = vol(id);
  const on = isConnected(id);
  openMenuAt(e.clientX, e.clientY, [
    h("div", { class: "menu-title" }, f.nick),
    menuSlider("Громкость пользователя", v.voice, (x) => setVol(id, { voice: x, voiceMuted: false })),
    menuItem("Выключить звук", v.voiceMuted, () => setVol(id, { voiceMuted: !v.voiceMuted })),
    h("div", { class: "menu-sep" }),
    menuItem("Написать", false, () => openDM(id)),
    on ? menuItem("Позвонить", false, () => startCall(id, false)) : menuItem("Соединиться", false, () => openConnect(id)),
    h("div", { class: "menu-sep" }),
    menuItem("Удалить из друзей", false, () => removeFriend(id), "danger"),
  ]);
}

function meMenu(e) {
  e.preventDefault();
  openMenuAt(e.clientX, e.clientY, [
    menuItem(S.muted ? "Включить микрофон" : "Выключить микрофон", S.muted, () => setMuted(!S.muted)),
    menuItem(S.deaf ? "Включить звук" : "Выключить звук", S.deaf, () => setDeaf(!S.deaf)),
    h("div", { class: "menu-sep" }),
    menuItem("Настройки голоса", false, () => openSettings("voice")),
  ]);
}

// ---------- высота окна звонка ----------

function applyStageHeight() {
  const st = $("#stage");
  const want = S.media.stageHeight;
  // во весь экран сцена на все окно - сохраненная высота не должна ее урезать
  st.style.height = want && !isTheater() ? Math.max(220, Math.min(window.innerHeight - 170, want)) + "px" : "";
}

(() => {
  const grip = $("#stageResizer");
  let startY = 0;
  let startH = 0;
  grip.addEventListener("pointerdown", (e) => {
    startY = e.clientY;
    startH = $("#stage").offsetHeight;
    grip.setPointerCapture(e.pointerId);
    document.body.classList.add("resizing");
  });
  grip.addEventListener("pointermove", (e) => {
    if (!grip.hasPointerCapture(e.pointerId)) return;
    S.media.stageHeight = Math.round(startH + e.clientY - startY);
    applyStageHeight();
  });
  const done = (e) => {
    if (!grip.hasPointerCapture(e.pointerId)) return;
    grip.releasePointerCapture(e.pointerId);
    document.body.classList.remove("resizing");
    saveMedia();
  };
  grip.addEventListener("pointerup", done);
  grip.addEventListener("pointercancel", done);
  grip.addEventListener("dblclick", () => {
    S.media.stageHeight = 0;
    applyStageHeight();
    saveMedia();
  });
  window.addEventListener("resize", applyStageHeight);
})();

$("#sbFull").onclick = () => setFull(!S.full);
$("#sbTheater").onclick = toggleTheater;
// stripHidden: в обычном виде участники видны, пока их не скрыли; во весь экран - наоборот
S.hideStrip = false;
const stripHidden = () => (isTheater() ? !S.showStrip : S.hideStrip);

$("#sbCollapse").onclick = () => {
  if (isTheater()) S.showStrip = !S.showStrip;
  else S.hideStrip = !S.hideStrip;
  renderStage();
};

async function deviceItems(kind, current, key) {
  const list = await deviceList(kind);
  return list.map(([id, label]) => menuItem(label, id === current, () => setMedia({ [key]: id })));
}

$("#sbMicOpts").onclick = async (e) => {
  const anchor = e.currentTarget;
  const [ins, outs] = await Promise.all([deviceItems("audioinput", S.media.input, "input"), deviceItems("audiooutput", S.media.output, "output")]);
  openMenu(anchor, [
    h("div", { class: "menu-title" }, "Микрофон"), ...ins,
    h("div", { class: "menu-sep" }), h("div", { class: "menu-title" }, "Динамики"), ...outs,
    h("div", { class: "menu-sep" }),
    menuSlider("Громкость микрофона", S.media.inputVolume, (x) => setMedia({ inputVolume: x })),
    menuItem("Настройки голоса", false, () => openSettings("voice")),
  ], "up");
};

$("#sbCamOpts").onclick = async (e) => {
  const anchor = e.currentTarget;
  const cams = await deviceItems("videoinput", S.media.camera, "camera");
  openMenu(anchor, [
    h("div", { class: "menu-title" }, "Камера"), ...cams,
    h("div", { class: "menu-sep" }), h("div", { class: "menu-title" }, "Качество"),
    ...["480p", "720p", "1080p"].map((r) => menuItem(r, S.media.camRes === r, () => setMedia({ camRes: r }))),
  ], "up");
};

$("#sbDeaf").onclick = () => setDeaf(!S.deaf);

$("#sbMore").onclick = (e) => {
  const scr = S.screen ? "myscreen" : (S.call && (S.remote.get(S.call.id) || {}).screen ? "theirscreen" : null);
  openMenu(e.currentTarget, [
    menuItem(cinema() ? "Обычный вид" : "Режим кинотеатра", false, toggleTheater),
    menuItem(S.full ? "Выйти из полноэкранного" : "Во весь экран", false, () => setFull(!S.full)),
    menuItem("Статистика", S.showStats, () => { S.showStats = !S.showStats; renderStage(); }),
    scr ? menuItem("Сохранить кадр трансляции", false, () => snapshot(scr).catch(fail)) : null,
    h("div", { class: "menu-sep" }),
    menuItem("Настройки голоса и видео", false, () => openSettings("voice")),
  ], "up");
};

$("#sbVolume").onclick = (e) => {
  if (!S.call) return;
  const id = S.call.id;
  const v = vol(id);
  openMenu(e.currentTarget, [
    menuSlider("Громкость " + fr(id).nick, v.voice, (x) => setVol(id, { voice: x, voiceMuted: false })),
    menuSlider("Громкость трансляции", v.stream, (x) => setVol(id, { stream: x, streamMuted: false })),
    h("div", { class: "menu-sep" }),
    menuItem(S.deaf ? "Включить звук" : "Выключить весь звук", S.deaf, () => setDeaf(!S.deaf)),
  ], "up");
};

// во весь экран кнопки и подписи прячутся, пока мышь не двигается (как в Discord)
(() => {
  const stage = $("#stage");
  let timer = null;
  // пока курсор на кнопках или в открытом меню - ничего не прячем, даже если мышь стоит
  const busy = () => !!menuEl || !!stage.querySelector(".stage-bar:hover, .tile-actions:hover");
  const sleepLater = () => {
    clearTimeout(timer);
    if (isTheater()) timer = setTimeout(() => { if (isTheater() && !busy()) stage.classList.add("idle"); else sleepLater(); }, 4000);
  };
  const wake = () => {
    stage.classList.remove("idle");
    sleepLater();
  };
  stage.addEventListener("mousemove", wake);
  stage.addEventListener("mousedown", wake);
  stage.addEventListener("mouseleave", (e) => {
    // меню громкости и прочие лежат вне сцены - уход курсора в меню не считается уходом
    if (isTheater() && !busy() && !(menuEl && menuEl.contains(e.relatedTarget))) stage.classList.add("idle");
  });
  new MutationObserver(wake).observe(document.body, { attributes: true, attributeFilter: ["class"] });
})();

$("#sbStats").onclick = () => {
  S.showStats = !S.showStats;
  renderStage();
};

// ---------- быстрый цикл: индикаторы речи, «печатает» ----------

function fastTick() {
  const c = S.call;
  const meSpeaking = !!c && c.state === "active" && mic.live && mic.speaking;
  const them = !!c && c.state === "active" && remoteLevel.level() > SPEAK_DB && !S.deaf;
  $$('.tile[data-who="me"]').forEach((t) => t.classList.toggle("speaking", meSpeaking));
  $$('.tile[data-who="them"]').forEach((t) => t.classList.toggle("speaking", them));
  const meAv = $(".me-av");
  if (meAv) meAv.classList.toggle("speaking", meSpeaking);
  if (S.view === "dm" && S.typing.has(S.sel) && S.typing.get(S.sel) < Date.now()) {
    S.typing.delete(S.sel);
    renderTyping();
  }
}

// ---------- статистика соединения раз в секунду ----------

const prevBytes = {};

function rate(key, bytes, ts) {
  const prev = prevBytes[key];
  prevBytes[key] = { bytes, ts };
  if (!prev || ts <= prev.ts || bytes < prev.bytes) return null; // счетчики сменились (другое соединение)
  return ((bytes - prev.bytes) * 8) / ((ts - prev.ts) / 1000);
}

const mbps = (bps) => (bps === null ? "…" : (bps / 1e6).toFixed(1) + " Мбит/с");

async function statsTick() {
  const c = S.call;
  if (!c || c.state !== "active") return;
  const p = peerOf(c.id);
  if (!p) return;
  const st = await p.stats().catch(() => null);
  if (!st) return;
  S.rtt = st.rtt;
  S.route = st.route;
  renderVoicePanel();
  markCall(c.id);
  keepFps(st);
  await nativeTick();
  if (!S.showStats) return;
  // прием своего захвата друга идет отдельным соединением - его счетчики оттуда
  if (S.nsIn) {
    const r = await S.nsIn.pc.getStats().catch(() => null);
    if (r) {
      r.forEach((s) => {
        if (s.type === "inbound-rtp" && s.kind === "video") st.in.screen = { ...s, id: "ns-" + s.id };
        if (s.type === "codec") st.codecs[s.id] = s.mimeType;
      });
    }
  }
  const lines = (s, out) => {
    if (!s) return "нет данных";
    const codec = (st.codecs[s.codecId] || "").replace("video/", "");
    const bits = out ? rate("out-screen", s.bytesSent, s.timestamp) : rate("in-screen", s.bytesReceived, s.timestamp);
    const res = s.frameWidth ? `${s.frameWidth}×${s.frameHeight}` : "—";
    const l = [`${res}  ${Math.round(s.framesPerSecond || 0)} к/с`, `${mbps(bits)}  ${codec}`];
    if (out) {
      const src = st.src && st.src.screen;
      if (src && src.framesPerSecond != null) l.push(`захват: ${Math.round(src.framesPerSecond)} к/с`);
      if (keeper.step > 0) l.push(`держу частоту: разрешение снижено (${keeper.step} из ${FPS_STEPS.length - 1})`);
      const ri = st.remoteIn[s.id];
      if (ri && ri.fractionLost != null) l.push(`потери у друга: ${(ri.fractionLost * 100).toFixed(1)}%`);
      if (st.availOut) l.push(`канал наружу: ${mbps(st.availOut)}`);
      l.push("кодер: " + (s.encoderImplementation || "?") + (s.powerEfficientEncoder ? " (аппаратный)" : ""));
      if (s.qualityLimitationReason && s.qualityLimitationReason !== "none") {
        l.push("упор: " + ({ bandwidth: "канал", cpu: "процессор" }[s.qualityLimitationReason] || s.qualityLimitationReason));
      }
    } else {
      if (S.nsIn && S.nsIn.track) l.push("друг шлет: захват через видеокарту");
      l.push("декодер: " + (s.decoderImplementation || "?") + (s.powerEfficientDecoder ? " (аппаратный)" : ""));
      if (s.framesDropped) l.push("потеряно кадров: " + s.framesDropped);
      if (s.packetsReceived) l.push(`потери пакетов: ${((100 * (s.packetsLost || 0)) / (s.packetsReceived + (s.packetsLost || 0))).toFixed(1)}%`);
    }
    l.push(`пинг: ${st.rtt ?? "?"} мс  ${{ lan: "LAN", direct: "напрямую", relay: "TURN" }[st.route] || ""}`);
    if (st.path) l.push(`я: ${st.path.local}`, `друг: ${st.path.remote}`);
    return l.join("\n");
  };
  const nativeLines = () => {
    const n = S.nsStats;
    if (!n) return "свой захват: запускается…";
    const l = [`${n.st.W}×${n.st.H}  ${Math.round(n.fps)} к/с`, `${mbps(n.bits)}  H264 (кодер ${(n.st.kbps / 1000).toFixed(1)} Мбит/с)`,
      `канал до друга: ~${(n.st.target / 1000).toFixed(1)} Мбит/с`, `захват: ${Math.round(n.cap)} к/с`, "кодер: " + (n.st.Encoder || "?") + " (видеокарта)"];
    if (n.st.BaseW && n.st.W < n.st.BaseW) l.push(`узкий канал: разрешение снижено с ${n.st.BaseW}×${n.st.BaseH}`);
    if (n.st.HDR) l.push(`HDR у источника: переводится в обычный (белый ${Math.round(n.st.WhiteNit)} нит)`);
    if (n.st.Skipped) l.push("пропущено (кодер занят): " + n.st.Skipped);
    l.push("соединение: " + ({ connected: "есть", connecting: "устанавливается", new: "ждет друга", failed: "не удалось", disconnected: "прервано" }[n.st.state] || n.st.state));
    l.push(`пинг: ${st.rtt ?? "?"} мс`);
    return l.join("\n");
  };
  const mine = $('.tile[data-who="myscreen"] .stats');
  if (mine) mine.textContent = S.native ? nativeLines() : lines(st.out.screen, true);
  const theirs = $('.tile[data-who="theirscreen"] .stats');
  if (theirs) theirs.textContent = lines(st.in.screen, false);
}

// ---------- настройки ----------

let saveTimer = null;

function saveMedia() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(() => api("settings", { media: S.media }).catch(fail), 300);
}

// setMedia меняет настройки и применяет то, что можно, сразу
function setMedia(changes, after) {
  const before = Mic.captureKey(S.media);
  Object.assign(S.media, changes);
  saveMedia();
  sfx.enabled = S.media.sounds;
  if ("output" in changes) sfx.setSink(S.media.output);
  if ("mode" in changes || "pttKey" in changes) syncPTT();
  if (mic.live) {
    if (Mic.captureKey(S.media) !== before) restartMic().catch(fail);
    else mic.apply(S.media);
  }
  applyOutput();
  if (after) Promise.resolve(after()).catch(fail);
}

async function restartMic() {
  if (!S.call || S.call.state !== "active") return;
  const t = await mic.start(S.media);
  mic.setMuted(S.muted || S.deaf);
  await peerOf(S.call.id).setTrack("mic", t);
}

$("#ubSettings").onclick = () => openSettings("voice");
$("#ubMe").onclick = () => openSettings("account");

let testMic = null;
let camTest = null;

function openSettings(section) {
  const nav = [
    ["account", "Моя учетная запись"],
    ["voice", "Голос и видео"],
    ["screen", "Трансляция экрана"],
    ["net", "Соединение"],
    ["app", "Приложение"],
  ];
  const content = h("div", { class: "st-content-in" });
  const navIn = h("div", { class: "st-nav-in" }, h("h4", {}, "Настройки"));
  const btns = {};
  for (const [k, label] of nav) {
    btns[k] = h("button", { onclick: () => show(k) }, label);
    navIn.append(btns[k]);
  }
  navIn.append(h("div", { class: "st-sep" }), h("button", { class: "st-danger", onclick: logout }, "Выйти"));
  const close = () => {
    stopTests();
    el.remove();
    document.removeEventListener("keydown", onKey);
  };
  const onKey = (e) => { if (e.key === "Escape" && !document.querySelector(".keybind.listening")) close(); };
  const el = h("div", { class: "settings" },
    h("nav", { class: "st-nav" }, navIn),
    h("div", { class: "st-content" }, content,
      h("div", { class: "st-close" }, h("button", { class: "icon-btn", html: icon("x", 18), onclick: () => close() }), "ESC")));
  document.addEventListener("keydown", onKey);
  document.body.append(el);

  function show(k) {
    stopTests();
    Object.entries(btns).forEach(([n, b]) => b.classList.toggle("active", n === k));
    content.replaceChildren(...({ account: secAccount, voice: secVoice, screen: secScreen, net: secNet, app: secApp }[k])());
  }
  show(section);
}

function stopTests() {
  if (testMic) {
    testMic.stop();
    testMic = null;
    const a = document.getElementById("aTest");
    if (a) a.remove();
  }
  if (camTest) {
    camTest.getTracks().forEach((t) => t.stop());
    camTest = null;
  }
}

function switchRow(title, sub, on, onChange, disabled) {
  const inp = h("input", { type: "checkbox", disabled: !!disabled });
  inp.checked = !!on;
  inp.onchange = () => onChange(inp.checked);
  return h("div", { class: "row-set" }, h("div", { class: "rs-text" }, h("div", { class: "rs-title" }, title), sub ? h("div", { class: "rs-sub" }, sub) : null),
    h("label", { class: "switch" }, inp, h("i")));
}

function selectEl(options, value, onChange) {
  const s = h("select", { class: "input" }, options.map(([v, l]) => h("option", { value: v }, l)));
  s.value = value;
  if (s.value !== value && options.length) s.value = options[0][0];
  s.onchange = () => onChange(s.value);
  return s;
}

function rangeRow(min, max, value, fmt, onChange) {
  const out = h("output", {}, fmt(value));
  const r = h("input", { type: "range", min: String(min), max: String(max), value: String(value) });
  r.oninput = () => {
    out.textContent = fmt(Number(r.value));
    onChange(Number(r.value));
  };
  return h("div", { class: "range-row" }, r, out);
}

function secAccount() {
  let color = S.me.color;
  const nick = h("input", { class: "input", value: S.me.nick, maxlength: "32" });
  const preview = h("div", {});
  const banner = h("div", { class: "profile-banner" });
  const swatches = h("div", { class: "swatches", style: { justifyContent: "flex-start" } });
  const draw = () => {
    banner.style.background = color;
    preview.replaceChildren(avatar({ nick: nick.value || S.me.login, color }, 80));
    swatches.replaceChildren(...COLORS.map((c) => h("button", { class: "swatch" + (c === color ? " on" : ""), style: { background: c }, onclick: () => { color = c; draw(); } })));
  };
  nick.oninput = draw;
  draw();
  const err = h("div", { class: "form-error" });
  return [
    h("h2", {}, "Моя учетная запись"),
    h("div", { class: "profile-card" }, banner, h("div", { class: "profile-row" }, preview,
      h("div", { class: "pn" }, h("b", {}, S.me.nick), h("span", {}, "@" + S.me.login + " · закрытая бета")))),
    h("label", { class: "field" }, h("span", {}, "Ник"), nick),
    h("div", { class: "field" }, h("span", {}, "Цвет"), swatches),
    err,
    h("button", { class: "btn primary", onclick: async () => {
      try {
        applyState(await api("profile", { nick: nick.value, color }));
        err.textContent = "";
        toast("Сохранено", "ok");
        for (const p of S.peers.values()) p.send({ k: "hello", nick: S.me.nick, color: S.me.color, v: S.version });
        renderAll();
      } catch (e) { err.textContent = e.message; }
    } }, "Сохранить"),
    h("h3", {}, "Ваш код дружбы"),
    h("p", { class: "hint" }, "Отправьте его другу, чтобы он добавил вас."),
    myCodeBox(),
  ];
}

async function deviceList(kind) {
  let list = await navigator.mediaDevices.enumerateDevices();
  if (list.some((d) => d.kind === kind && !d.label)) {
    try {
      const s = await navigator.mediaDevices.getUserMedia(kind === "videoinput" ? { video: true } : { audio: true });
      s.getTracks().forEach((t) => t.stop());
      list = await navigator.mediaDevices.enumerateDevices();
    } catch (e) { /* без названий */ }
  }
  const out = [["default", "По умолчанию (как в Windows)"]];
  for (const d of list) {
    if (d.kind === kind && d.deviceId !== "default" && d.deviceId !== "communications") out.push([d.deviceId, d.label || "Устройство"]);
  }
  return out;
}

function meterEl() {
  const fill = h("i");
  const mark = h("b");
  const m = h("div", { class: "meter" }, fill, mark);
  return { el: m, update(src) {
    fill.style.width = Math.max(0, Math.min(100, src.level + 100)) + "%";
    fill.classList.toggle("open", src.gateOpen && src.transmitting);
    mark.style.left = Math.max(0, Math.min(100, src.threshold + 100)) + "%";
    mark.hidden = S.media.mode === "ptt" || !S.media.gate;
  } };
}

function secVoice() {
  const inSel = h("div", {}, "…");
  const outSel = h("div", {}, "…");
  const camSel = h("div", {}, "…");
  deviceList("audioinput").then((l) => inSel.replaceChildren(selectEl(l, S.media.input, (v) => setMedia({ input: v }, () => testMic && startTest()))));
  deviceList("audiooutput").then((l) => outSel.replaceChildren(selectEl(l, S.media.output, (v) => setMedia({ output: v }))));
  deviceList("videoinput").then((l) => camSel.replaceChildren(selectEl(l, S.media.camera, (v) => setMedia({ camera: v }))));

  const meter = meterEl();
  const source = () => (mic.live ? mic : testMic);
  const onLevel = () => { const s = source(); if (s) meter.update(s); };
  mic.addEventListener("level", onLevel);
  const testBtn = h("button", { class: "btn primary small" }, "Проверить микрофон");
  async function startTest() {
    if (testMic) { testMic.stop(); }
    testMic = new Mic();
    testMic.addEventListener("level", onLevel);
    const t = await testMic.start(S.media);
    let a = document.getElementById("aTest");
    if (!a) {
      a = h("audio", { id: "aTest", autoplay: true });
      document.body.append(a);
    }
    a.srcObject = new MediaStream([t]);
    if (a.setSinkId && S.media.output !== "default") a.setSinkId(S.media.output).catch(() => {});
    testBtn.textContent = "Остановить проверку";
  }
  testBtn.onclick = async () => {
    if (mic.live) return toast("Вы в звонке - индикатор показывает ваш микрофон", "");
    if (testMic) {
      stopTests();
      testBtn.textContent = "Проверить микрофон";
      return;
    }
    startTest().catch(fail);
  };

  const sensSlider = h("input", { type: "range", min: "-100", max: "0", value: String(S.media.threshold), disabled: S.media.gateAuto });
  sensSlider.oninput = () => setMedia({ threshold: Number(sensSlider.value), gateAuto: false });

  const keyBtn = h("button", { class: "keybind" }, S.media.pttLabel || "Не назначена");
  keyBtn.onclick = () => {
    keyBtn.classList.add("listening");
    keyBtn.textContent = "Нажмите клавишу…";
    const done = (vk, label) => {
      document.removeEventListener("keydown", onKey, true);
      document.removeEventListener("mousedown", onMouse, true);
      keyBtn.classList.remove("listening");
      if (vk) setMedia({ pttKey: vk, pttLabel: label });
      keyBtn.textContent = S.media.pttLabel || "Не назначена";
    };
    const onKey = (e) => {
      e.preventDefault();
      e.stopPropagation();
      if (e.key === "Escape") return done(0);
      done(e.keyCode, e.code.replace(/^Key/, "").replace(/^Digit/, "") || e.key);
    };
    const onMouse = (e) => {
      if (e.button === 0 && e.target === keyBtn) return;
      const map = { 1: [4, "Колесо мыши"], 3: [5, "Мышь 4"], 4: [6, "Мышь 5"] }[e.button];
      if (map) {
        e.preventDefault();
        done(map[0], map[1]);
      }
    };
    setTimeout(() => {
      document.addEventListener("keydown", onKey, true);
      document.addEventListener("mousedown", onMouse, true);
    }, 0);
  };

  const modeCard = (val, title, sub) => {
    const inp = h("input", { type: "radio", name: "vmode" });
    inp.checked = S.media.mode === val;
    const card = h("label", { class: "radio-card" + (inp.checked ? " on" : "") }, inp, h("div", {}, h("div", { class: "rs-title" }, title), h("div", { class: "rs-sub hint" }, sub)));
    inp.onchange = () => {
      setMedia({ mode: val });
      $$(".radio-card").forEach((c) => c.classList.toggle("on", c === card));
      pttRow.hidden = val !== "ptt";
      sensBox.hidden = val === "ptt";
    };
    return card;
  };
  const pttRow = h("div", { class: "row-set", hidden: S.media.mode !== "ptt" },
    h("div", { class: "rs-text" }, h("div", { class: "rs-title" }, "Клавиша"), h("div", { class: "rs-sub" }, "Работает во всей системе, даже когда Zeno свернут (в игре).")), keyBtn);
  const sensBox = h("div", { hidden: S.media.mode === "ptt" },
    h("h3", {}, "Чувствительность ввода"),
    switchRow("Определять автоматически", "Порог сам подстраивается под фоновый шум комнаты.", S.media.gateAuto, (v) => {
      setMedia({ gateAuto: v });
      sensSlider.disabled = v;
    }),
    h("div", { class: "sens" }, meter.el, sensSlider),
    h("p", { class: "hint" }, "Полоска зеленая - звук уходит другу. Белая черта - порог: все тише нее (шорох, клавиатура) отрезается."));

  const iso = supportsVoiceIsolation();
  const isoRow = switchRow("Изоляция голоса", iso ? "Для стандартного шумоподавления: сильнее отделяет голос от фона." : "Недоступно в этой версии WebView2.",
    S.media.isolation && iso, (v) => setMedia({ isolation: v }), !iso || S.media.nsMode !== "standard");
  const nsSub = h("div", { class: "rs-sub" });
  const dfnRow = h("div", { class: "row-set" }, h("div", { class: "rs-text" }, h("div", { class: "rs-title" }, "Сила DeepFilterNet"),
    h("div", { class: "rs-sub" }, "Насколько глушить шум. Меньше - фон слышен чуть-чуть, зато голос естественнее.")),
    rangeRow(10, 100, S.media.dfnLevel, (v) => v + " дБ", (v) => setMedia({ dfnLevel: v })));
  const syncNS = () => {
    const m = S.media.nsMode;
    const err = m === "dfn" && (mic.dfnError || (testMic && testMic.dfnError));
    nsSub.textContent = err ? "DeepFilterNet не запустился, вместо него работает RNNoise: " + err : NS_MODES.find(([v]) => v === m)?.[2] || "";
    dfnRow.style.display = m === "dfn" ? "" : "none";
  };
  syncNS();
  const camBox = h("div", { class: "cam-preview" }, "Камера выключена");
  const camBtn = h("button", { class: "btn small", onclick: async () => {
    if (camTest) {
      stopTests();
      camBox.replaceChildren("Камера выключена");
      camBtn.textContent = "Проверить камеру";
      return;
    }
    try {
      const v = {};
      if (S.media.camera !== "default") v.deviceId = { exact: S.media.camera };
      camTest = await navigator.mediaDevices.getUserMedia({ video: Object.keys(v).length ? v : true });
      const vid = h("video", { autoplay: true, playsinline: true });
      vid.muted = true;
      vid.srcObject = camTest;
      camBox.replaceChildren(vid);
      camBtn.textContent = "Выключить";
    } catch (e) { fail(e); }
  } }, "Проверить камеру");

  return [
    h("h2", {}, "Голос и видео"),
    h("div", { class: "grid2" },
      h("div", { class: "field" }, h("span", {}, "Микрофон"), inSel),
      h("div", { class: "field" }, h("span", {}, "Динамики"), outSel)),
    h("div", { class: "grid2" },
      h("div", { class: "field" }, h("span", {}, "Громкость микрофона"), rangeRow(0, 200, S.media.inputVolume, (v) => v + "%", (v) => setMedia({ inputVolume: v }))),
      h("div", { class: "field" }, h("span", {}, "Громкость звука"), rangeRow(0, 100, S.media.outputVolume, (v) => v + "%", (v) => setMedia({ outputVolume: v })))),
    h("div", { class: "row-set" }, h("div", { class: "rs-text" }, h("div", { class: "rs-title" }, "Проверка микрофона"),
      h("div", { class: "rs-sub" }, "Вы услышите себя с обработкой, как вас слышит друг.")), testBtn),
    h("h3", {}, "Режим ввода"),
    modeCard("vad", "Активация по голосу", "Микрофон передает звук, когда вы говорите."),
    modeCard("ptt", "Push-to-talk", "Звук передается, только пока зажата клавиша."),
    pttRow,
    sensBox,
    h("h3", {}, "Обработка голоса"),
    h("div", { class: "row-set" }, h("div", { class: "rs-text" }, h("div", { class: "rs-title" }, "Шумоподавление"), nsSub),
      selectEl(NS_MODES.map(([v, l]) => [v, l]), S.media.nsMode, (v) => {
        setMedia({ nsMode: v });
        isoRow.querySelector("input").disabled = !iso || v !== "standard";
        syncNS();
      })),
    dfnRow,
    switchRow("Передавать только голос", "Пока вы не говорите, звуки не уходят вовсе: жевание, клавиатура, щелчки мыши, дыхание.", S.media.vad, (v) => setMedia({ vad: v })),
    isoRow,
    switchRow("Эхоподавление", "Друг не слышит себя из ваших колонок. Не выключайте без наушников.", S.media.ec, (v) => setMedia({ ec: v })),
    switchRow("Автоматическая регулировка усиления", "Выравнивает громкость: тихий голос громче, крик тише.", S.media.agc, (v) => setMedia({ agc: v })),
    switchRow("Шумовой гейт", "Полная тишина между фразами (порог выше).", S.media.gate, (v) => setMedia({ gate: v })),
    h("div", { class: "row-set" }, h("div", { class: "rs-text" }, h("div", { class: "rs-title" }, "Качество голоса"),
      h("div", { class: "rs-sub" }, "Битрейт Opus. 64 кбит/с - как в Discord; больше - для музыки. Меняется со следующего соединения.")),
      selectEl([["32", "32 кбит/с"], ["48", "48 кбит/с"], ["64", "64 кбит/с"], ["96", "96 кбит/с"], ["128", "128 кбит/с"]], String(S.media.voiceKbps), (v) => setMedia({ voiceKbps: Number(v) }, tuneSenders))),
    h("h3", {}, "Видео"),
    h("div", { class: "grid2" },
      h("div", { class: "field" }, h("span", {}, "Камера"), camSel),
      h("div", { class: "field" }, h("span", {}, "Качество камеры"),
        selectEl([["480p", "480p"], ["720p", "720p"], ["1080p", "1080p"]], S.media.camRes, (v) => setMedia({ camRes: v })))),
    camBtn, camBox,
  ];
}

function secScreen() {
  return [
    h("h2", {}, "Трансляция экрана"),
    h("div", { class: "field" }, h("span", {}, "Качество"),
      selectEl(Object.entries(SCREEN_PRESETS).map(([k, p]) => [k, `${p.label} (до ${p.kbps / 1000} Мбит/с)`]), S.media.screenPreset, (v) => setMedia({ screenPreset: v }, tuneScreen))),
    h("p", { class: "hint" }, "Для 1080p 60 к/с нужна исходящая скорость около 10 Мбит/с у того, кто показывает. Если канала не хватает, движок сам снизит качество."),
    h("h3", {}, "Режим"),
    h("div", { class: "field" }, selectEl([["motion", "Плавность - держать частоту кадров (игры, видео)"], ["detail", "Четкость - держать резкость (текст, код)"]], S.media.screenMode, (v) => setMedia({ screenMode: v }, tuneScreen))),
    switchRow("Звук системы", "Друг слышит звук игры или видео вместе с картинкой.", S.media.screenAudio, (v) => setMedia({ screenAudio: v }, applyScreenAudio)),
    h("div", { class: "row-set" }, h("div", { class: "rs-text" }, h("div", { class: "rs-title" }, "Кодек"),
      h("div", { class: "rs-sub" }, "H.264 кодируется видеокартой и почти не грузит процессор. Применяется со следующего соединения.")),
      selectEl([["h264", "H.264 (рекомендуется)"], ["vp9", "VP9"], ["av1", "AV1"], ["vp8", "VP8"], ["auto", "Как решит движок"]], S.media.codec, (v) => setMedia({ codec: v }))),
  ];
}

function secNet() {
  const stun = h("textarea", { class: "input", rows: "4", spellcheck: "false", placeholder: "Пусто - выбирать автоматически" });
  stun.value = S.media.stun;
  const a = S.stunAuto;
  const autoInfo = h("p", { class: "hint" }, !a ? "Проверяю серверы…"
    : (a.vpn ? `Обнаружен VPN: зарубежные серверы видят адрес ${a.other}, российские - ${a.direct}. Звонки идут мимо VPN, через ${a.direct}.`
      : `Внешний адрес: ${a.direct || "не определен"}.`) + " Серверы: " + a.servers.map((x) => x.replace("stun:", "")).join(", "));
  stun.onchange = () => setMedia({ stun: stun.value });
  const field = (label, key, type = "text") => {
    const i = h("input", { class: "input", type, value: S.media[key], spellcheck: "false" });
    i.onchange = () => setMedia({ [key]: i.value });
    return h("label", { class: "field" }, h("span", {}, label), i);
  };
  const brokers = h("textarea", { class: "input", rows: "4", spellcheck: "false", placeholder: "tcp://broker.emqx.io:1883" });
  brokers.value = S.settings.brokers || "";
  brokers.onchange = () => {
    S.settings.brokers = brokers.value;
    api("settings", { brokers: brokers.value }).catch(fail);
  };
  return [
    h("h2", {}, "Соединение"),
    h("p", { class: "hint" }, "Звук, видео и сообщения идут напрямую между ПК. STUN-серверы нужны только чтобы узнать свой внешний адрес - через них ничего не передается. Изменения действуют со следующего соединения."),
    h("label", { class: "field" }, h("span", {}, "STUN-серверы, по одному в строке"), stun),
    autoInfo,
    h("h3", {}, "TURN (запасной путь)"),
    h("p", { class: "hint" }, "Нужен, только если у обоих провайдер с серыми IP и напрямую не соединяется. Тогда трафик пойдет через ваш сервер."),
    field("Адрес (turn:host:3478)", "turnUrl"),
    h("div", { class: "grid2" }, field("Логин", "turnUser"), field("Пароль", "turnPass", "password")),
    h("h3", {}, "Серверы для коротких кодов"),
    h("p", { class: "hint" }, "Публичные MQTT-брокеры - через них передаются только зашифрованные длинные коды. По одному в строке (tcp://host:1883). Пусто - встроенный список."),
    brokers,
    ...secLan(),
  ];
}

// secLan - адаптер virtualZeno: что стоит и кнопка «убрать все»
function secLan() {
  const info = h("p", { class: "hint" }, "Проверяю…");
  const del = h("button", { class: "btn small danger", hidden: true }, "Удалить адаптер и службу");
  api("lan/status").then((st) => {
    const svc = { missing: "не установлены", running: "служба работает", stopped: "служба остановлена" }[st.service] || st.service;
    info.textContent = `Адаптер и служба: ${svc}. Ваш адрес в сети virtualZeno: ${st.me}.` + (st.version ? ` Служба из Zeno ${st.version}.` : "");
    del.hidden = st.service === "missing";
  }).catch((e) => { info.textContent = e.message; });
  del.onclick = async () => {
    if (!confirm("Убрать адаптер virtualZeno, службу и правило брандмауэра? Windows спросит разрешение администратора. Запустить virtualZeno потом можно снова - с установкой.")) return;
    del.disabled = true;
    try {
      const ids = S.media.lanOn;
      S.media.lanOn = [];
      saveMedia();
      ids.forEach(lanAnnounce);
      await api("lan/uninstall");
      info.textContent = "Адаптер и служба убраны.";
      del.hidden = true;
    } catch (e) { fail(e); }
    del.disabled = false;
  };
  return [
    h("h3", {}, "virtualZeno (локальная сеть)"),
    h("p", { class: "hint" }, "Встроенное приложение «Приложения» в чате с другом: адаптер Wintun и адреса 10.77.x.y для LAN-игр и общих папок. Работает, только пока его запустили оба."),
    info,
    del,
  ];
}

function secApp() {
  const u = S.update || {};
  const status = h("div", { class: "rs-sub" }, u.available ? `Доступна версия ${u.version}` : (u.message || u.error || ""));
  // кнопка одна: «Проверить обновления», а если новая версия уже найдена - сразу «Обновить»
  const updBtn = h("button", { class: "btn small" });
  const showBtn = () => {
    const avail = S.update && S.update.available;
    updBtn.className = "btn small" + (avail ? " primary" : "");
    updBtn.textContent = avail ? `Обновить до ${S.update.version}` : "Проверить обновления";
  };
  updBtn.onclick = async () => {
    if (S.update && S.update.available) {
      if (S.call && !confirm("Обновление перезапустит Zeno. Звонок прервется на несколько секунд и восстановится сам, если у друга Zeno 0.5.4 или новее. Продолжить?")) return;
      updBtn.disabled = true;
      status.textContent = "Скачиваю обновление, Zeno перезапустится сам…";
      try { await api("update/install"); } catch (e) { status.textContent = e.message; updBtn.disabled = false; }
      return;
    }
    status.textContent = "Проверяю…";
    updBtn.disabled = true;
    try {
      S.update = await api("update/check");
      status.textContent = S.update.available ? `Доступна версия ${S.update.version}` : (S.update.message || S.update.error || "");
      renderUpdate();
    } catch (e) { status.textContent = e.message; }
    updBtn.disabled = false;
    showBtn();
  };
  showBtn();
  return [
    h("h2", {}, "Приложение"),
    switchRow("Сворачивать в трей при закрытии", "Крестик прячет окно, звонок и чат продолжают работать.", S.settings.close_to_tray !== false, (v) => {
      S.settings.close_to_tray = v;
      api("settings", { close_to_tray: v }).catch(fail);
    }),
    switchRow("Запускать вместе с Windows", "Zeno стартует свернутым в трей.", S.autostart, async (v) => {
      try { S.autostart = (await api("autostart", { on: v })).autostart; } catch (e) { fail(e); }
    }),
    switchRow("Звуки", "Звонок, сообщения, подключение.", S.media.sounds, (v) => setMedia({ sounds: v })),
    switchRow("Уведомления о сообщениях", "Когда окно свернуто или открыт другой разговор.", S.media.notifyMessages, (v) => setMedia({ notifyMessages: v })),
    switchRow("Проверять обновления", "Раз в 6 часов смотреть новую версию на GitHub.", S.settings.check_updates !== false, (v) => {
      S.settings.check_updates = v;
      api("settings", { check_updates: v }).catch(fail);
    }),
    h("div", { class: "row-set" }, h("div", { class: "rs-text" }, h("div", { class: "rs-title" }, "Zeno " + S.version), status), updBtn),
  ];
}

async function logout() {
  if (!confirm("Выйти из учетной записи? Ключ и список друзей на этом ПК удалятся: друзьям нужно будет заново обменяться с вами кодами дружбы.")) return;
  try {
    if (S.call) hangup();
    for (const p of S.peers.values()) p.close(true);
    await api("logout");
    location.reload();
  } catch (e) { fail(e); }
}

// ---------- клавиатура ----------

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && isTheater() && !document.querySelector(".overlay, .settings, .menu")) {
    if (S.full) setFull(false);
    else setTheater(false);
  }
  if (e.ctrlKey && e.shiftKey && e.code === "KeyM") {
    e.preventDefault();
    setMuted(!(S.muted || S.deaf));
  }
  if (e.ctrlKey && e.shiftKey && e.code === "KeyD") {
    e.preventDefault();
    setDeaf(!S.deaf);
  }
  // T - кинотеатр, как на YouTube; не когда печатают
  if (e.code === "KeyT" && !e.ctrlKey && !e.altKey && !e.metaKey && S.call && S.call.state === "active" &&
      !e.target.closest("input, textarea, [contenteditable]") && !document.querySelector(".overlay, .settings, .menu")) {
    e.preventDefault();
    toggleTheater();
  }
});

window.addEventListener("focus", () => {
  if (S.view === "dm" && S.unread.has(S.sel)) {
    S.unread.delete(S.sel);
    renderSide();
  }
});

boot().catch((e) => {
  document.body.innerHTML = `<div style="padding:40px;color:#f23f43">Не удалось запустить интерфейс: ${esc(e.message)}</div>`;
});

// ---------- окно программы, а не страница ----------
// Без перезагрузки по F5/Ctrl+R (оборвала бы звонок), масштаба, печати, поиска
// и перехода по перетащенным файлам и ссылкам.

document.addEventListener("keydown", (e) => {
  const k = e.key.toLowerCase();
  const ctrl = e.ctrlKey || e.metaKey;
  if (e.key === "F5" || e.key === "F3" || e.key === "F7" || e.key === "F12" || (e.key === "BrowserBack" || e.key === "BrowserForward") ||
    (ctrl && ["r", "p", "f", "g", "j", "h", "u", "s", "o", "n", "t", "w", "+", "=", "-", "0"].includes(k)) ||
    (ctrl && e.shiftKey && ["i", "j", "c", "r", "delete"].includes(k)) || (e.altKey && (e.key === "ArrowLeft" || e.key === "ArrowRight"))) {
    const t = e.target;
    const typing = t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA") && ctrl && ["f", "a", "z"].includes(k);
    if (!typing) e.preventDefault();
  }
}, true);
window.addEventListener("wheel", (e) => { if (e.ctrlKey) e.preventDefault(); }, { passive: false });
document.addEventListener("dragover", (e) => e.preventDefault());
document.addEventListener("drop", (e) => e.preventDefault());
document.addEventListener("auxclick", (e) => { if (e.button === 1) e.preventDefault(); });
document.addEventListener("mousedown", (e) => { if (e.button === 1) e.preventDefault(); });
