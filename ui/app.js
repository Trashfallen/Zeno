// Zeno: интерфейс. Вход, друзья, чат, звонок, трансляция экрана, настройки.

import { api, $, $$, h, esc, copyText, readClipboard, icon, avatar, hhmm, dayLabel, duration } from "./api.js";
import { Mic, LevelWatcher, Playback, Sfx, VOICE_DEFAULTS, supportsVoiceIsolation } from "./voice.js";
import { Peer, SLOTS, DEFAULT_STUN, SCREEN_PRESETS } from "./peer.js";

const MEDIA_DEFAULTS = {
  ...VOICE_DEFAULTS,
  screenPreset: "1080p60", screenMode: "motion", screenAudio: true, codec: "h264", camRes: "720p",
  stun: DEFAULT_STUN.join("\n"), turnUrl: "", turnUser: "", turnPass: "",
  sounds: true, notifyMessages: true,
  stageHeight: 0, // высота окна звонка, 0 - по умолчанию
  vol: {}, // громкость друзей: { id: { voice, stream, voiceMuted, streamMuted } }, 0-300%
};
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
    autoLoop();
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
  $("#sendBtn").innerHTML = icon("send", 20);
  $("#vpHang").innerHTML = icon("hang", 22);
  $("#sbHang").innerHTML = icon("hang", 24);
  $("#sbScreenOpts").innerHTML = icon("sliders", 20);
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
      ? h("div", { class: "dm-sub in-call" }, S.call.state === "active" ? "в звонке" : "вызов…")
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
    : c.state === "incoming" ? "Входящий звонок" : "Вызов…";
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
  if (S.call && !confirm("Обновление перезапустит Zeno и завершит звонок. Продолжить?")) return;
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
    el.append(h("div", { class: "text", html: linkify(m.text) }));
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

// ---------- соединение ----------

function iceServers() {
  const stun = S.media.stun.split(/\s+/).map((s) => s.trim()).filter((s) => /^stuns?:/.test(s));
  const list = [{ urls: stun.length ? stun : DEFAULT_STUN }];
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
  if (S.call && S.call.id === id) endCall("Соединение потеряно");
  if (f) toast(`Соединение с ${f.nick} потеряно, переподключаюсь…`, "err");
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
      if (S.call && S.call.id === id) endCall();
      break;
    case "state":
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

async function activateCall() {
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
  addSys(c.id, "Звонок");
  renderAll();
}

function endCall(reason) {
  const c = S.call;
  if (!c) return;
  clearTimeout(c.timer);
  sfx.stopRing();
  ringIncoming(false);
  closeIncoming();
  const p = peerOf(c.id);
  stopScreen(true);
  stopCam(true);
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
  const pr = SCREEN_PRESETS[S.media.screenPreset] || SCREEN_PRESETS["1080p60"];
  const withAudio = S.media.screenAudio;
  let stream;
  try {
    stream = await navigator.mediaDevices.getDisplayMedia({
      video: { width: { max: pr.w }, height: { max: pr.h }, frameRate: { ideal: pr.fps, max: pr.fps } },
      audio: withAudio ? { echoCancellation: false, noiseSuppression: false, autoGainControl: false, suppressLocalAudioPlayback: false, restrictOwnAudio: true } : false,
      systemAudio: withAudio ? "include" : "exclude",
      selfBrowserSurface: "exclude",
      surfaceSwitching: "include",
      monitorTypeSurfaces: "include",
    });
  } catch (e) {
    if (e.name !== "NotAllowedError" && e.name !== "AbortError") fail(new Error("Не удалось начать трансляцию: " + e.message));
    return;
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
  await p.setTrack("screen", v);
  const a = stream.getAudioTracks()[0];
  if (a) await p.setTrack("sys", a);
  await tuneScreen();
  api("capturebar", { active: true }).catch(() => {});
  sfx.play("stream");
  sendState();
  renderAll();
}

async function tuneScreen() {
  if (!S.screen || !S.call) return;
  const p = peerOf(S.call.id);
  const pr = SCREEN_PRESETS[S.media.screenPreset] || SCREEN_PRESETS["1080p60"];
  const v = S.screen.getVideoTracks()[0];
  const motion = S.media.screenMode === "motion";
  v.contentHint = motion ? "motion" : "detail";
  try { await v.applyConstraints({ width: { max: pr.w }, height: { max: pr.h }, frameRate: { ideal: pr.fps, max: pr.fps } }); } catch (e) { /* источник не умеет */ }
  const st = v.getSettings();
  const scale = Math.max(1, (st.width || pr.w) / pr.w, (st.height || pr.h) / pr.h);
  // для игр - balanced: при нехватке канала или процессора понемногу снижаются и кадры,
  // и разрешение; maintain-framerate в таких условиях скатывался до 320x180
  await p.tuneSender("screen", { maxBitrate: pr.kbps * 1000, maxFramerate: pr.fps, scaleResolutionDownBy: scale },
    motion ? "balanced" : "maintain-resolution");
  if (S.screen.getAudioTracks().length) await p.tuneSender("sys", { maxBitrate: 128000 });
}

function stopScreen(silent) {
  if (!S.screen) return;
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
    menuItem("Звук системы", S.media.screenAudio, () => {
      setMedia({ screenAudio: !S.media.screenAudio });
      if (S.screen) toast("Звук включится при следующем запуске трансляции");
    }));
  openMenu(e.currentTarget, items, "up");
};

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

function openMenu(anchor, items, dir) {
  closeMenu();
  menuEl = h("div", { class: "menu" }, items);
  document.body.append(menuEl);
  const r = anchor.getBoundingClientRect();
  const mr = menuEl.getBoundingClientRect();
  menuEl.style.left = Math.max(8, Math.min(window.innerWidth - mr.width - 8, r.left + r.width / 2 - mr.width / 2)) + "px";
  menuEl.style.top = (dir === "up" ? r.top - mr.height - 8 : r.bottom + 8) + "px";
  setTimeout(() => document.addEventListener("mousedown", outside), 0);
}

function outside(e) {
  if (menuEl && !menuEl.contains(e.target)) closeMenu();
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
    if (!me && c.state === "outgoing") tile.append(h("div", { class: "waiting" }, "Вызов…"));
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
    const track = me ? S.screen.getVideoTracks()[0] : p.remoteTrack("screen");
    const tile = h("div", { class: "tile screen", "data-who": who },
      videoFor(who, track, { dbl: () => setFull(!S.full) }),
      h("span", { class: "live" }, "В ЭФИРЕ"),
      h("div", { class: "tile-label" }, me ? "Ваша трансляция" : `${f.nick} транслирует экран`),
      h("div", { class: "stats", hidden: !S.showStats }),
      h("div", { class: "tile-actions" },
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
  const sbScr = $("#sbScreen");
  sbScr.innerHTML = icon(S.screen ? "screenOff" : "screen", 20);
  sbScr.classList.toggle("on", !!S.screen);
  sbScr.title = S.screen ? "Остановить трансляцию" : "Транслировать экран";
  sbCam.disabled = sbScr.disabled = c.state !== "active";
  $("#sbStats").classList.toggle("on", S.showStats);
  $("#sbCollapse").innerHTML = icon(stripHidden() ? "up" : "down", 20);
  $("#sbCollapse").title = stripHidden() ? "Показать участников" : "Скрыть участников";
  $("#sbCollapse").hidden = !screens.length;
  $("#sbFull").innerHTML = icon(S.full ? "shrink" : "expand", 20);
  $("#sbFull").title = S.full ? "Выйти из полноэкранного (Esc)" : "Во весь экран";
  applyStageHeight();
}

const isTheater = () => document.body.classList.contains("theater");

function setTheater(on) {
  if (!on && S.full) return setFull(false);
  document.body.classList.toggle("theater", on);
  renderStage();
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
    menuItem(isTheater() && !S.full ? "Обычный вид" : "Во всё окно", false, () => setTheater(!(isTheater() && !S.full))),
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
  st.style.height = want ? Math.max(220, Math.min(window.innerHeight - 170, want)) + "px" : "";
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

$("#sbMore").onclick = (e) => {
  const scr = S.screen ? "myscreen" : (S.call && (S.remote.get(S.call.id) || {}).screen ? "theirscreen" : null);
  openMenu(e.currentTarget, [
    menuItem(isTheater() && !S.full ? "Обычный вид" : "Во всё окно", false, () => setTheater(!(isTheater() && !S.full))),
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
  const wake = () => {
    stage.classList.remove("idle");
    clearTimeout(timer);
    if (isTheater()) timer = setTimeout(() => { if (isTheater() && !menuEl) stage.classList.add("idle"); }, 2500);
  };
  stage.addEventListener("mousemove", wake);
  stage.addEventListener("mousedown", wake);
  stage.addEventListener("mouseleave", () => { if (isTheater()) stage.classList.add("idle"); });
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
  if (!prev || ts <= prev.ts) return null;
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
  if (!S.showStats) return;
  const lines = (s, out) => {
    if (!s) return "нет данных";
    const codec = (st.codecs[s.codecId] || "").replace("video/", "");
    const bits = out ? rate("out-screen", s.bytesSent, s.timestamp) : rate("in-screen", s.bytesReceived, s.timestamp);
    const res = s.frameWidth ? `${s.frameWidth}×${s.frameHeight}` : "—";
    const l = [`${res}  ${Math.round(s.framesPerSecond || 0)} к/с`, `${mbps(bits)}  ${codec}`];
    if (out) {
      const src = st.src && st.src.screen;
      if (src && src.framesPerSecond != null) l.push(`захват: ${Math.round(src.framesPerSecond)} к/с`);
      const ri = st.remoteIn[s.id];
      if (ri && ri.fractionLost != null) l.push(`потери у друга: ${(ri.fractionLost * 100).toFixed(1)}%`);
      if (st.availOut) l.push(`канал наружу: ${mbps(st.availOut)}`);
      l.push("кодер: " + (s.encoderImplementation || "?") + (s.powerEfficientEncoder ? " (аппаратный)" : ""));
      if (s.qualityLimitationReason && s.qualityLimitationReason !== "none") {
        l.push("упор: " + ({ bandwidth: "канал", cpu: "процессор" }[s.qualityLimitationReason] || s.qualityLimitationReason));
      }
    } else {
      l.push("декодер: " + (s.decoderImplementation || "?") + (s.powerEfficientDecoder ? " (аппаратный)" : ""));
      if (s.framesDropped) l.push("потеряно кадров: " + s.framesDropped);
      if (s.packetsReceived) l.push(`потери пакетов: ${((100 * (s.packetsLost || 0)) / (s.packetsReceived + (s.packetsLost || 0))).toFixed(1)}%`);
    }
    l.push(`пинг: ${st.rtt ?? "?"} мс  ${{ lan: "LAN", direct: "напрямую", relay: "TURN" }[st.route] || ""}`);
    if (st.path) l.push(`я: ${st.path.local}`, `друг: ${st.path.remote}`);
    return l.join("\n");
  };
  const mine = $('.tile[data-who="myscreen"] .stats');
  if (mine) mine.textContent = lines(st.out.screen, true);
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
    h("div", { class: "row-set" }, h("div", { class: "rs-text" }, h("div", { class: "rs-title" }, "Шумоподавление"),
      h("div", { class: "rs-sub" }, "Нейросеть RNNoise убирает вентиляторы, улицу, клавиатуру и чавканье - как Krisp в Discord.")),
      selectEl([["rnnoise", "Нейросеть (рекомендуется)"], ["standard", "Стандартное"], ["off", "Выключено"]], S.media.nsMode, (v) => {
        setMedia({ nsMode: v });
        isoRow.querySelector("input").disabled = !iso || v !== "standard";
      })),
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
    switchRow("Звук системы", "Друг слышит звук игры или видео вместе с картинкой.", S.media.screenAudio, (v) => setMedia({ screenAudio: v })),
    h("div", { class: "row-set" }, h("div", { class: "rs-text" }, h("div", { class: "rs-title" }, "Кодек"),
      h("div", { class: "rs-sub" }, "H.264 кодируется видеокартой и почти не грузит процессор. Применяется со следующего соединения.")),
      selectEl([["h264", "H.264 (рекомендуется)"], ["vp9", "VP9"], ["av1", "AV1"], ["vp8", "VP8"], ["auto", "Как решит движок"]], S.media.codec, (v) => setMedia({ codec: v }))),
  ];
}

function secNet() {
  const stun = h("textarea", { class: "input", rows: "5", spellcheck: "false" });
  stun.value = S.media.stun;
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
    h("h3", {}, "TURN (запасной путь)"),
    h("p", { class: "hint" }, "Нужен, только если у обоих провайдер с серыми IP и напрямую не соединяется. Тогда трафик пойдет через ваш сервер."),
    field("Адрес (turn:host:3478)", "turnUrl"),
    h("div", { class: "grid2" }, field("Логин", "turnUser"), field("Пароль", "turnPass", "password")),
    h("h3", {}, "Серверы для коротких кодов"),
    h("p", { class: "hint" }, "Публичные MQTT-брокеры - через них передаются только зашифрованные длинные коды. По одному в строке (tcp://host:1883). Пусто - встроенный список."),
    brokers,
  ];
}

function secApp() {
  const u = S.update || {};
  const status = h("div", { class: "rs-sub" }, u.available ? `Доступна версия ${u.version}` : (u.message || u.error || ""));
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
    h("div", { class: "row-set" }, h("div", { class: "rs-text" }, h("div", { class: "rs-title" }, "Zeno " + S.version), status),
      h("button", { class: "btn small", onclick: async () => {
        status.textContent = "Проверяю…";
        try {
          S.update = await api("update/check");
          status.textContent = S.update.available ? `Доступна версия ${S.update.version}` : (S.update.message || S.update.error || "");
          renderUpdate();
        } catch (e) { status.textContent = e.message; }
      } }, "Проверить обновления")),
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
