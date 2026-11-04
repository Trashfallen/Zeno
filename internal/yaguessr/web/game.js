(function () {
  'use strict';

  const $ = (id) => document.getElementById(id);
  const KEY_STORAGE = 'yaguessr.apikey';
  const SETTINGS_STORAGE = 'yaguessr.settings';
  const MAX_SCORE = 5000;
  const GRACE_MS = 3000; // сервер дает 3 с запаса на загрузку панорамы
  const COLORS = (window.YGSkin && YGSkin.colors) || ['#e6493a', '#3a7be6', '#a23ae6', '#e68a3a', '#29a39b', '#d63a9b', '#6b8e23', '#8b5a2b'];

  // Внутри Zeno (встроенное приложение): ник и вход дает Zeno, комнату хост
  // создает сразу, гость ходит в комнату через Zeno друга (ZenoSocket)
  const ZQ = new URLSearchParams(location.search);
  const ZENO = window.ZENO ? ZQ.get('zeno') : null; // 'host', 'guest' или null
  if (ZENO) document.documentElement.classList.add('zeno');
  function toZeno(msg) { if (ZENO) parent.postMessage(msg, location.origin); }

  // ZenoSocket - WebSocket гостя: сообщения идут через страницу Zeno (postMessage)
  // и канал данных в Zeno друга, где живет комната
  class ZenoSocket {
    constructor() {
      this.readyState = 0;
      this.onopen = this.onmessage = this.onclose = null;
      this.h = (e) => {
        if (e.origin !== location.origin || e.source !== parent || !e.data || !e.data.ygws) return;
        const d = e.data;
        if (d.ygws === 'open') {
          this.readyState = 1;
          if (this.onopen) this.onopen();
        } else if (d.ygws === 'msg' && this.readyState === 1) {
          if (this.onmessage) this.onmessage({ data: d.data });
        } else if (d.ygws === 'close') this.closed();
      };
      addEventListener('message', this.h);
      toZeno({ ygws: 'connect' });
    }
    send(s) { if (this.readyState === 1) toZeno({ ygws: 'send', data: s }); }
    close() {
      if (this.readyState >= 2) return;
      toZeno({ ygws: 'close' });
      this.closed();
    }
    closed() {
      if (this.readyState === 3) return;
      this.readyState = 3;
      removeEventListener('message', this.h);
      if (this.onclose) this.onclose();
    }
  }

  function storageGet(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
  function storageSet(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* приватный режим */ } }

  function toast(text) {
    const t = $('toast');
    t.textContent = text;
    t.classList.remove('hidden');
    clearTimeout(toast.id);
    toast.id = setTimeout(() => t.classList.add('hidden'), 2500);
  }

  function esc(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }

  // ---------- Вход в меню ----------

  // Свои карты с сервера, id региона = 'm_' + id карты
  const customMaps = {};
  let savedRegion = '';

  function getRegion(id) { return REGIONS[id] || GREGIONS[id] || TREGIONS[id] || customMaps[id] || null; }

  function fillRegionSelect() {
    const sel = $('region');
    const cur = sel.value || savedRegion;
    const google = $('provider').value === 'google';
    sel.innerHTML = '';
    const group = (label, entries) => {
      if (!entries.length) return;
      const og = document.createElement('optgroup');
      og.label = label;
      for (const [id, r] of entries) {
        const o = document.createElement('option');
        o.value = id;
        o.textContent = r.name;
        og.appendChild(o);
      }
      sel.appendChild(og);
    };
    group('Готовые', Object.entries(google ? GREGIONS : REGIONS));
    // тематические карты по всему миру только для Google, по России и для Яндекса
    group('Тематические', Object.entries(TREGIONS).filter(([, r]) => google || r.both));
    group('Свои карты', Object.entries(customMaps));
    if (sel.querySelector('option[value="' + cur + '"]')) sel.value = cur;
  }

  async function loadCustomMaps() {
    try {
      const list = await (await fetch('api/maps')).json();
      for (const k of Object.keys(customMaps)) delete customMaps[k];
      for (const m of list) customMaps['m_' + m.id] = m;
    } catch (e) { /* без своих карт тоже можно играть */ }
    fillRegionSelect();
    return customMaps;
  }

  // fillRegionSelect зовется после восстановления настроек, когда уже известен сервис

  const configKey = (window.YA_API_KEY || '').trim();
  if (!configKey) {
    $('keyBlock').classList.remove('hidden');
    $('apiKey').value = storageGet(KEY_STORAGE) || '';
  }

  try {
    const s = JSON.parse(storageGet(SETTINGS_STORAGE) || '{}');
    if (s.provider) $('provider').value = s.provider;
    if (s.region) savedRegion = s.region;
    if (s.mode) $('mode').value = s.mode;
    if (s.coopLevel) $('coopLevel').value = s.coopLevel;
    if (s.coopRule) $('coopRule').value = s.coopRule;
    if (s.rounds) $('rounds').value = s.rounds;
    if (s.timer !== undefined) $('timer').value = s.timer;
    if (s.guessTimer !== undefined) $('guessTimer').value = s.guessTimer;
    if (s.layer) $('layer').value = s.layer;
    if (s.move) $('move').value = s.move;
    else if (s.noMove) $('move').value = 'nomove';
    if (s.hideMarkers !== undefined) $('hideMarkers').checked = s.hideMarkers;
  } catch (e) { /* битые настройки игнорируем */ }
  fillRegionSelect();
  loadCustomMaps();

  const urlRoom = new URLSearchParams(location.search).get('room');
  if (urlRoom) $('roomCode').value = urlRoom.toUpperCase();

  let me = { login: '', nick: '' };
  fetch('api/me').then((r) => r.json()).then((d) => {
    me = d;
    $('name').value = d.nick || '';
    $('meLine').textContent = 'Аккаунт: ' + d.login;
    if (!d.nick) {
      $('menuError').textContent = 'Придумай ник, его увидят другие игроки';
      $('name').focus();
    }
  }).catch(() => {});

  async function saveNick() {
    const nick = $('name').value.trim();
    if (!nick) throw new Error('Введи ник');
    if (nick === me.nick) return;
    const r = await fetch('api/nick', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ nick }),
    });
    if (!r.ok) throw new Error('Не удалось сохранить ник');
    me = await r.json();
    $('name').value = me.nick;
  }

  // ---------- API Яндекс Карт ----------

  let ymapsPromise = null;
  let loadedKey = null;

  function apiKey() {
    return configKey || $('apiKey').value.trim();
  }

  function loadYmaps() {
    const key = apiKey();
    if (loadedKey && loadedKey !== key) {
      // API уже загружен с другим ключом, второй раз его не подключить
      location.reload();
      return new Promise(() => {});
    }
    if (ymapsPromise) return ymapsPromise;
    if (!configKey) storageSet(KEY_STORAGE, key);
    loadedKey = key;
    ymapsPromise = new Promise((resolve, reject) => {
      const s = document.createElement('script');
      s.src = 'https://api-maps.yandex.ru/2.1/?apikey=' + encodeURIComponent(key) + '&lang=ru_RU';
      s.onload = () => ymaps.ready(() => resolve(window.ymaps), reject);
      s.onerror = async () => {
        ymapsPromise = null;
        loadedKey = null;
        s.remove();
        // браузер не дает прочитать ответ Яндекса с ошибкой, причину узнает сервер игры
        let reason = '';
        try { reason = (await (await fetch('api/ymaps-status')).json()).reason; } catch (e) { /* сеть */ }
        if (reason === 'limit') {
          reject(new Error('Дневной лимит Яндекс Карт (100 запросов в сутки) исчерпан. Он обновится завтра'));
        } else if (reason === 'key') {
          reject(new Error('Яндекс не принял ключ API'));
        } else {
          reject(new Error('Не удалось загрузить API Яндекс Карт'));
        }
      };
      document.head.appendChild(s);
    });
    return ymapsPromise;
  }

  // ---------- Соединение с сервером ----------

  let ws = null;
  let roomCode = null;   // комната, в которой мы сейчас (для переподключения)
  let myId = null;
  let state = null;      // последний снимок комнаты
  let leaving = false;

  function connect(code) {
    leaving = false;
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    ws = ZENO === 'guest' ? new ZenoSocket() : new WebSocket(proto + '://' + location.host + location.pathname.replace(/[^/]*$/, '') + 'ws');
    ws.onopen = () => ws.send(JSON.stringify({ t: 'hello', room: code || '' }));
    ws.onmessage = (e) => onMessage(JSON.parse(e.data));
    ws.onclose = () => {
      ws = null;
      if (leaving) return;
      if (roomCode) {
        toast('Связь потеряна, переподключаемся...');
        setTimeout(() => { if (!ws && roomCode) connect(roomCode); }, 1500);
      } else {
        $('menuError').textContent = 'Нет связи с сервером';
      }
    };
  }

  function send(msg) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
  }

  function onMessage(m) {
    if (m.t === 'error') {
      if (!roomCode) {
        $('menuError').textContent = m.msg;
        leaving = true;
        ws && ws.close();
      } else {
        toast(m.msg);
        $('lobbyError').textContent = m.msg;
      }
      return;
    }
    if (m.t === 'kicked') {
      leaving = true;
      roomCode = null;
      state = null;
      history.replaceState(null, '', location.pathname);
      show('menu');
      $('menuError').textContent = 'Хост убрал тебя из комнаты';
      return;
    }
    if (m.t === 'needLocs') {
      supplyLocs(m.count);
      return;
    }
    if (m.t === 'needReroll') {
      rerollLoc(m.round, m.by);
      return;
    }
    if (m.t === 'welcome') {
      roomCode = m.room;
      myId = m.you;
      history.replaceState(null, '', '?room=' + m.room + (ZENO ? '&zeno=' + ZENO : ''));
      toZeno({ yg: 'room', code: m.room });
      return;
    }
    if (m.t === 'pool' && poolWaiter) {
      poolWaiter(m.locs || []);
      poolWaiter = null;
      return;
    }
    if (m.t === 'picked') YGModes.picked(m);
    if (m.t === 'pickError') YGModes.pickError(m.msg);
    if (m.t === 'state') onState(m);
  }

  // ---------- Состояние игры ----------

  let player = null;
  let startPano = null;
  let miniMap = null;
  let resultMap = null;
  let guess = null;
  let guessMark = null;
  let sentRound = 0;      // раунд, в котором мы уже ответили
  let shownRound = 0;     // раунд, панорама которого загружена
  let shownResult = '';   // какой итог уже нарисован
  let shownLoc = '';      // место текущей панорамы
  let timerId = null;
  let deadline = 0;
  let roundStartedAt = 0; // кто быстрее: множитель считается от начала раунда
  let bet = 1;            // ставки: x1..x3
  let poolWaiter = null;  // ждем ответ сервера с местами из запаса
  const panoCache = {};   // панорамы, найденные хостом: второй раз их не ищем

  function locKey(lat, lon) { return lat.toFixed(5) + ',' + lon.toFixed(5); }

  // расстояние по сфере в метрах, точки [lat, lon]
  function distance(a, b) {
    const rad = Math.PI / 180;
    const dLat = (b[0] - a[0]) * rad;
    const dLon = (b[1] - a[1]) * rad;
    const h = Math.sin(dLat / 2) ** 2 + Math.cos(a[0] * rad) * Math.cos(b[0] * rad) * Math.sin(dLon / 2) ** 2;
    return 2 * 6371008.8 * Math.asin(Math.min(1, Math.sqrt(h)));
  }

  function isGoogle(st) { return (st || state.settings).provider === 'google'; }

  // Google: точки ищет сервер по бесплатным плиткам покрытия, без ключа и лимита
  async function googleFind(regionId, count) {
    const reg = getRegion(regionId);
    const res = await fetch('api/gsv/find', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ zones: reg.zones, count }),
    });
    const d = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(d.error || 'Сервер не нашел мест');
    if (!d.locs || d.locs.length < count) {
      throw new Error('В этих областях почти нет панорам Google. Попробуй другую карту');
    }
    return d.locs;
  }

  // слой Яндекса: уличные панорамы или воздушные (на карте это шарики)
  function panoLayer(layer) { return layer === 'air' ? 'yandex#airPanorama' : 'yandex#panorama'; }
  function poolKey(regionId, layer) { return layer === 'air' ? regionId + '@air' : regionId; }

  // Каждый ymaps.panorama.locate тратит 1 поиск из суточного лимита
  async function locate(point, layer) {
    send({ t: 'spent', n: 1 });
    const list = await ymaps.panorama.locate(point, { layer: panoLayer(layer) });
    return list[0] || null;
  }

  function poolGet(region, count) {
    return new Promise((resolve) => {
      poolWaiter = resolve;
      send({ t: 'poolGet', region, count });
      setTimeout(() => { if (poolWaiter === resolve) { poolWaiter = null; resolve([]); } }, 3000);
    });
  }

  function isHost() { return state && state.host === myId; }
  function isDuel() { return state && state.settings.mode === 'duel'; }
  function mode() { return state ? state.settings.mode : $('mode').value; }
  // штурман: в режиме разведчика отвечает он, панораму видит разведчик
  function isScout() { return state && state.scout === myId; }
  function isNavigator() { return state && state.scout && state.scout !== myId && meP() && !meP().out; }
  function meP() { return state && state.players.find((p) => p.id === myId); }
  function region() { return getRegion(state.settings.region) || REGIONS.moscow; }
  function colorOf(id) {
    const i = state.players.findIndex((p) => p.id === id);
    return COLORS[(i < 0 ? 0 : i) % COLORS.length];
  }

  function show(id) {
    for (const s of ['menu', 'lobby', 'game', 'result', 'maps', 'editor', 'pick']) $(s).classList.toggle('hidden', s !== id);
    // под итогами раунда экран игры остается: в нем грузится панорама следующего раунда
    if (id === 'result') $('game').classList.remove('hidden');
  }

  // Панорама во встроенном окне: два iframe, видимый и запасной. Пока открыты итоги раунда,
  // в запасном невидимо грузится следующее место, на старте раунда окна меняются местами.
  const frames = [$('panoFrame'), $('panoFrame2')];
  let activeFrame = 0;
  function frame() { return frames[activeFrame]; }
  function spareFrame() { return frames[1 - activeFrame]; }
  function setFrame(f, url) {
    f.dataset.url = url;
    f.dataset.loaded = '';
    f.src = url;
  }
  function clearFrames() {
    for (const f of frames) {
      setFrame(f, 'about:blank');
      f.classList.add('hidden');
      f.classList.remove('preload');
    }
  }
  // встроенное окно показывает Google и уличные панорамы Яндекса, шары идут через плеер API
  function usesEmbed() { return isGoogle() || state.settings.layer !== 'air'; }

  function preloadNext(loc) {
    if (!loc || !usesEmbed()) return;
    const url = embedUrl(loc);
    const f = spareFrame();
    if (f.dataset.url === url) return;
    f.onload = () => { if (f.dataset.url === url) f.dataset.loaded = '1'; };
    f.classList.remove('hidden');
    f.classList.add('preload');
    setFrame(f, url);
  }

  function onState(s) {
    const prev = state;
    state = s;
    if (s.state === 'lobby') {
      // хост мог закончить игру посреди раунда: гасим таймер и панораму
      stopTimer();
      clearFrames();
      shownRound = 0;
      shownResult = '';
      if (!prev || prev.state !== 'lobby') YGModes.resetConquer();
      renderLobby(prev && prev.state === 'lobby');
      show('lobby');
    } else if (s.state === 'pick') {
      stopTimer();
      clearFrames();
      if (!prev || prev.state === 'lobby' || prev.state === 'final') YGModes.resetConquer();
      YGModes.enterPick(s);
      $('pickEnd').classList.toggle('hidden', !isHost() || s.round === 0);
    } else if (s.state === 'round') {
      if (prev && (prev.state === 'lobby' || prev.state === 'final')) YGModes.resetConquer();
      if (shownRound !== s.round || (s.loc && locKey(s.loc.lat, s.loc.lon) !== shownLoc)) {
        // новый раунд или хост заменил место, на котором не было панорамы
        if (shownRound === s.round) toast('Место заменено');
        startRound();
      } else if (s.left) {
        // после первого ответа сервер сокращает время раунда
        const nd = Date.now() + s.left - GRACE_MS;
        if (!timerId || Math.abs(nd - deadline) > 1500) startTimer(s.left);
      }
      if (prev && prev.state === 'round' && prev.round === s.round) announceGuesses(prev, s);
      renderGuessStatus();
    } else if (s.state === 'reveal' || s.state === 'final') {
      const key = s.state + s.round;
      if (shownResult !== key) {
        shownResult = key;
        showResult();
      }
      if (s.next) preloadNext(s.next);
      $('nextBtn').classList.toggle('hidden', !isHost() || s.waiting);
      $('endBtn2').classList.toggle('hidden', !isHost() || s.state === 'final' || s.last);
      $('nextWait').classList.toggle('hidden', isHost() && !s.waiting);
      $('nextWait').textContent = s.waiting ? 'Хост ищет следующее место...' : 'Ждем, пока хост продолжит';
    }
  }

  // соперник ответил: звуковой сигнал (таймер в этот момент мог сократиться)
  function announceGuesses(prev, s) {
    const was = new Set(prev.players.filter((p) => p.guessed).map((p) => p.id));
    if (s.players.some((p) => p.guessed && !was.has(p.id) && p.id !== myId)) YGSound.opponent();
  }

  // ---------- Лобби ----------

  const SETTING_IDS = ['provider', 'region', 'mode', 'rounds', 'timer', 'guessTimer', 'layer', 'move', 'hideMarkers',
    'coopLevel', 'coopRule'];

  function settingsFromForm() {
    return {
      provider: $('provider').value,
      region: $('region').value,
      mode: $('mode').value,
      coopLevel: $('coopLevel').value,
      coopRule: $('coopRule').value,
      rounds: Number($('rounds').value),
      timer: Number($('timer').value),
      guessTimer: Number($('guessTimer').value),
      layer: $('layer').value,
      move: $('move').value,
      hideMarkers: $('hideMarkers').checked,
    };
  }

  const MODE_HINTS = {
    classic: 'Очки за раунд: до 5000, чем ближе метка, тем больше. Побеждает сумма.',
    duel: 'Все начинают с 6000 HP. Каждый раунд игрок теряет разницу между лучшим результатом раунда и своим. ' +
      'С 5-го раунда урон растет: x1.5, x2 и дальше. Кто дошел до 0, выбывает и смотрит. Нужно минимум 2 игрока.',
    tug: 'Двое тянут канат: разница очков раунда сдвигает узел к тому, кто ответил точнее. С 5-го раунда сдвиг ' +
      'растет. Кто перетянул на 10 000, тот победил. Нужно ровно 2 игрока.',
    speed: 'Чем быстрее ответ, тем больше множитель: от x1 сразу до x0.5 на последней секунде. Первый ответивший ' +
      'получает еще x1.2, остальным остается 10 с. Без таймера раунд идет 1 минуту.',
    bets: 'Перед ответом выбери ставку. x2 и x3 умножают очки, если промах не больше 25 км (на маленьких картах ' +
      'еще меньше, 5% размаха карты), ' +
      'иначе штраф 1000 за каждую ступень. Счет может уйти в минус.',
    conquer: 'Страна, где была панорама, достается тому, кто ответил точнее всех (последний захват перебивает ' +
      'прошлый). Побеждает тот, у кого больше стран. Лучше играть на картах мира и Европы.',
    coop: 'Вы одна команда против цели: в зачет идет лучший ответ раунда или середина между ответами. ' +
      'Сложность задает, сколько нужно набрать за раунд.',
    scout: 'Разведчик видит панораму, но не карту. Штурман видит карту, но не панораму. Разведчик описывает ' +
      'увиденное голосом, штурман ставит метку. Роли меняются каждый раунд, счет общий. Нужно ровно 2 игрока.',
    twoviews: 'Вы стоите в одном месте, но в разных точках в 100-300 м друг от друга. Обменивайтесь подсказками ' +
      'голосом, каждый угадывает свою точку. Только панорамы Google, минимум 2 игрока.',
    custom: 'Перед каждым раундом каждый загадывает место для соперника: ставит точку на карте, игра находит ' +
      'панораму рядом. Потом угадываете места друг друга. Только панорамы Google, минимум 2 игрока.',
  };

  function updateModeHint() {
    const m = $('mode').value;
    const duel = m === 'duel';
    // свои места рядом и загаданные точки ищутся только по покрытию Google
    const googleOnly = m === 'twoviews' || m === 'custom';
    $('optYandex').disabled = googleOnly;
    if (googleOnly && $('provider').value !== 'google') {
      $('provider').value = 'google';
      fillRegionSelect();
    }
    const google = $('provider').value === 'google';
    // встроенным панорамам нельзя запретить шаги, это умеет только плеер Яндекса для шаров
    const yaPlayer = !google && $('layer').value === 'air';
    $('layerBox').classList.toggle('hidden', google);
    $('hideMarkersBox').classList.toggle('hidden', !yaPlayer);
    $('optNomove').disabled = !yaPlayer;
    if (!yaPlayer && $('move').value === 'nomove') $('move').value = 'move';
    $('roundsBox').classList.toggle('hidden', duel || m === 'tug');
    $('coopBox').classList.toggle('hidden', m !== 'coop' && m !== 'scout');
    $('coopRuleBox').classList.toggle('hidden', m !== 'coop');
    $('modeHint').textContent = MODE_HINTS[m] || MODE_HINTS.classic;
    if (!google && $('layer').value === 'air') {
      $('modeHint').textContent += ' Воздушных панорам меньше, чем уличных, в основном они в крупных городах: ' +
        'на маленьких своих картах их может не найтись.';
    }
  }

  function renderLobby(wasLobby) {
    $('lobbyCode').textContent = state.room;
    $('inviteLink').value = location.origin + location.pathname + '?room=' + state.room;
    const host = isHost();
    $('playerList').innerHTML = state.players.map((p) =>
      '<li class="' + (p.online ? '' : 'offline') + '"><span class="dot" data-i="' + esc((Array.from(p.name)[0] || '?').toUpperCase()) +
      '" style="background:' + colorOf(p.id) + '"></span>' +
      esc(p.name) + (p.id === state.host ? ' (хост)' : '') + (p.id === myId ? ' (ты)' : '') +
      (host && p.id !== myId
        ? (p.online ? '<button class="pl-btn" data-host="' + p.id + '" title="Передать хоста">хост</button>' : '') +
          '<button class="pl-btn" data-kick="' + p.id + '" title="Выгнать из комнаты">&times;</button>'
        : '') + '</li>').join('');
    $('settingsBox').disabled = !host;
    $('startBtn').classList.toggle('hidden', !host);
    $('lobbyStatus').textContent = host ? '' : 'Хост выбирает настройки и запускает игру';
    const usage = state.usage || 0;
    $('usageLine').textContent = isGoogle(settingsFromForm()) ? 'Google: поиск мест без лимита'
      : 'Поисков Яндекса сегодня: ' + usage + ' из 100. Готовых мест в запасе: ' + (state.poolFree || 0);
    $('usageLine').classList.toggle('error', !isGoogle(settingsFromForm()) && usage >= 85);

    if (host && !wasLobby) {
      // хост отдает свои сохраненные настройки остальным
      send({ t: 'settings', settings: settingsFromForm() });
    } else if (!host && state.settings.region) {
      const st = state.settings;
      $('provider').value = st.provider || 'yandex';
      fillRegionSelect();
      if (!getRegion(st.region)) loadCustomMaps().then(() => { $('region').value = st.region; });
      $('region').value = st.region;
      $('mode').value = st.mode || 'classic';
      $('coopLevel').value = st.coopLevel || 'traveler';
      $('coopRule').value = st.coopRule || 'best';
      $('rounds').value = st.rounds;
      $('timer').value = st.timer;
      $('guessTimer').value = st.guessTimer || 0;
      $('layer').value = st.layer || 'ground';
      $('move').value = st.move || 'move';
      $('hideMarkers').checked = st.hideMarkers;
    }
    updateModeHint();
    if (window.YGSkin) YGSkin.lobby();
  }

  function regionBounds(reg) {
    let s = 90, w = 180, n = -90, e = -180;
    for (const z of reg.zones) {
      const dLat = z.r / 111.32;
      const dLon = z.r / (111.32 * Math.cos(z.c[0] * Math.PI / 180));
      s = Math.min(s, z.c[0] - dLat);
      n = Math.max(n, z.c[0] + dLat);
      w = Math.min(w, z.c[1] - dLon);
      e = Math.max(e, z.c[1] + dLon);
    }
    return [[s, w], [n, e]];
  }

  function randomPoint(reg) {
    const total = reg.zones.reduce((sum, z) => sum + z.w, 0);
    let x = Math.random() * total;
    let zone = reg.zones[reg.zones.length - 1];
    for (const z of reg.zones) {
      x -= z.w;
      if (x <= 0) { zone = z; break; }
    }
    // равномерно по кругу
    const d = zone.r * Math.sqrt(Math.random());
    const a = Math.random() * 2 * Math.PI;
    return [
      zone.c[0] + d * Math.cos(a) / 111.32,
      zone.c[1] + d * Math.sin(a) / (111.32 * Math.cos(zone.c[0] * Math.PI / 180)),
    ];
  }

  // Хост набирает места на все раунды: сначала из запаса на сервере,
  // недостающие ищет сам. Лишние найденные места уходят в запас.
  async function findLocations(regionId, count, onProgress, layer) {
    const reg = getRegion(regionId);
    const locs = await poolGet(poolKey(regionId, layer), count);
    // на своих картах области могут быть там, где панорам нет: ограничиваем расход
    const budget = Math.max(12, count * 6);
    let spent = 0;
    const extra = [];
    onProgress(locs.length);
    let lastError = null;
    const near = (list, pos) => list.some((l) => distance([l.lat, l.lon], pos) < 300);
    while (locs.length < count && spent < budget) {
      const batch = Math.min(4, (count - locs.length) * 2, budget - spent);
      spent += batch;
      const points = Array.from({ length: batch }, () => randomPoint(reg));
      let errors = 0;
      const found = await Promise.all(points.map((p) =>
        locate(p, layer).catch((err) => { lastError = err; errors++; return null; })));
      // вся пачка с ошибкой = ключ или сеть, дальше только зря тратить лимит
      if (errors === points.length) break;
      for (const pano of found) {
        if (!pano) continue;
        const pos = pano.getPosition();
        if (near(locs, pos) || near(extra, pos)) continue;
        const loc = { lat: pos[0], lon: pos[1], dir: Math.round(Math.random() * 360) };
        panoCache[locKey(loc.lat, loc.lon)] = pano;
        if (locs.length < count) {
          locs.push(loc);
          onProgress(locs.length);
        } else {
          extra.push(loc);
        }
      }
    }
    if (extra.length) send({ t: 'poolAdd', region: poolKey(regionId, layer), locs: extra });
    if (locs.length < count) {
      const msg = lastError ? String(lastError.message || lastError) : '';
      if (msg === 'scriptError') {
        throw new Error('Яндекс не принял ключ API. Новый ключ начинает работать примерно через 15 минут после создания');
      }
      if (msg) throw new Error('Не удалось найти панорамы: ' + msg);
      throw new Error('В областях этой карты мало панорам: потрачено ' + spent + ' поисков, найдено мест ' +
        locs.length + ' из ' + count + '. Рисуй области по городам России и соседних стран');
    }
    // порядок раундов перемешиваем, чтобы места из запаса не шли первыми
    locs.sort(() => Math.random() - 0.5);
    return locs;
  }

  async function startGame() {
    $('lobbyError').textContent = '';
    const settings = settingsFromForm();
    storageSet(SETTINGS_STORAGE, JSON.stringify(settings));
    const online = state.players.filter((p) => p.online).length;
    if (['duel', 'twoviews', 'custom'].includes(settings.mode) && online < 2) {
      $('lobbyError').textContent = 'Для этого режима нужно хотя бы 2 игрока';
      return;
    }
    if (['tug', 'scout'].includes(settings.mode) && online !== 2) {
      $('lobbyError').textContent = 'Для этого режима нужно ровно 2 игрока';
      return;
    }
    // сначала ищем до 5 мест, остальные сервер попросит по ходу игры
    const endless = settings.mode === 'duel' || settings.mode === 'tug';
    const first = Math.min(endless ? 5 : settings.rounds, 5);
    $('startBtn').disabled = true;
    try {
      const b = regionBounds(getRegion(settings.region));
      settings.maxDist = distance(b[0], b[1]);
      let locs;
      if (settings.mode === 'custom') {
        // места загадают сами игроки
        send({ t: 'start', settings });
        return;
      }
      if (isGoogle(settings)) {
        $('lobbyStatus').textContent = 'Ищем места...';
        locs = await googleFind(settings.region, first);
        if (settings.mode === 'twoviews') {
          $('lobbyStatus').textContent = 'Ищем вторые точки обзора...';
          locs = await withAlts(locs);
        }
      } else {
        if (!apiKey()) throw new Error('Для панорам Яндекса нужен API-ключ');
        await loadYmaps();
        if (!ymaps.panorama.isSupported()) throw new Error('Браузер не поддерживает панорамы');
        locs = await findLocations(settings.region, first, (n) => {
          $('lobbyStatus').textContent = 'Ищем места: ' + n + ' из ' + first;
        }, settings.layer);
      }
      send({ t: 'start', settings, locs });
    } catch (e) {
      $('lobbyError').textContent = e.message || String(e);
    } finally {
      $('startBtn').disabled = false;
      $('lobbyStatus').textContent = '';
    }
  }

  // Сервер просит хоста найти еще мест для следующих раундов
  let supplying = false;
  async function supplyLocs(count) {
    if (supplying || !state || !isHost()) return;
    supplying = true;
    try {
      // сбой сети или лимита бывает разовым: еще две попытки с паузой
      for (let attempt = 0; ; attempt++) {
        try {
          send({ t: 'moreLocs', locs: await findMore(count) });
          return;
        } catch (e) {
          toast(e.message || String(e));
          if (attempt >= 2 || !state || !isHost()) break;
          await new Promise((res) => setTimeout(res, 10000));
        }
      }
      // пустой ответ снимает запрос на сервере, иначе раунд ждет вечно
      send({ t: 'moreLocs', locs: [] });
    } finally {
      supplying = false;
    }
  }

  // «Два взгляда»: к каждому месту вторая точка обзора в 100-300 м. Если рядом
  // панорам нет, второй игрок смотрит с той же точки, но в другую сторону.
  function withAlts(locs) {
    return Promise.all(locs.map(async (l) => {
      try {
        const res = await fetch('api/gsv/near', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ lat: l.lat, lon: l.lon }),
        });
        const d = await res.json();
        if (res.ok && d.loc) return Object.assign({}, l, { alt: d.loc });
      } catch (e) { /* ниже запасной вариант */ }
      return Object.assign({}, l, { alt: { lat: l.lat, lon: l.lon, dir: (l.dir + 180) % 360 } });
    }));
  }

  // места для следующих раундов или на замену, у выбранного сервиса
  async function findMore(count) {
    if (isGoogle() && state.settings.mode === 'twoviews') return withAlts(await googleFind(state.settings.region, count));
    if (isGoogle()) return googleFind(state.settings.region, count);
    await loadYmaps();
    return findLocations(state.settings.region, count, () => {}, state.settings.layer);
  }

  // кто-то не увидел панораму: хост подбирает другое место на этот раунд
  let rerolling = 0;
  async function rerollLoc(round, by) {
    if (!isHost() || rerolling === round) return;
    rerolling = round;
    toast((by || 'Игрок') + ': нет панорамы, ищем другое место');
    try {
      const locs = await findMore(1);
      if (state.state === 'round' && state.round === round) send({ t: 'replaceLoc', round, locs });
    } catch (e) {
      toast(e.message || String(e));
    } finally {
      rerolling = 0;
    }
  }

  // верхняя строка и полоски режимов: урон, канат, счет команды, множитель скорости
  function renderHp() {
    const m = mode();
    const duel = m === 'duel';
    const tug = m === 'tug';
    const team = m === 'coop' || m === 'scout';
    $('multBox').classList.toggle('hidden', !duel && !tug);
    $('multBox').firstChild.textContent = tug ? 'Сила ' : 'Урон ';
    $('scoreBox').classList.toggle('hidden', duel || tug || team);
    $('roundOf').classList.toggle('hidden', duel || tug);
    $('teamBox').classList.toggle('hidden', !team);
    $('speedBox').classList.toggle('hidden', m !== 'speed');
    $('tugBar').classList.toggle('hidden', !tug);
    if (duel || tug) $('multVal').textContent = 'x' + String(state.mult || 1).replace('.', ',');
    if (team) {
      $('teamVal').textContent = state.team;
      $('teamGoal').textContent = state.teamGoal;
    }
    if (tug) renderTug($('tugMe'), $('tugThem'), $('tugKnot'));
  }

  // канат глазами игрока: свой край слева, узел сдвигается к тому, кто перетягивает
  function renderTug(meEl, themEl, knot) {
    const me = meP();
    const them = state.players.find((p) => p.id !== myId && !p.out);
    const pull = me ? me.pull : (them ? -them.pull : 0);
    const win = state.tugWin || 10000;
    meEl.textContent = me ? me.name : '';
    themEl.textContent = them ? them.name : '';
    knot.style.left = (50 - Math.max(-1, Math.min(1, pull / win)) * 50) + '%';
    knot.title = (pull > 0 ? '+' : '') + pull;
  }

  // ---------- Раунд ----------

  function patchMarkers(pano) {
    // Номера домов приходят из getMarkers(), подменяем его у класса панорамы
    const proto = Object.getPrototypeOf(pano);
    if (!proto || typeof proto.getMarkers !== 'function' || proto.__yaguessrPatched) return;
    const orig = proto.getMarkers;
    proto.getMarkers = function () { return state && state.settings.hideMarkers ? [] : orig.call(this); };
    proto.__yaguessrPatched = true;
  }

  async function startRound() {
    const round = state.round;
    shownRound = round;
    shownResult = '';
    sentRound = 0;
    show('game');
    $('roundNum').textContent = round;
    $('roundTotal').textContent = state.settings.rounds;
    renderHp();
    updateMyScore();
    const move = state.settings.move || 'move';
    const loc = state.loc;
    const navigator = isNavigator();
    shownLoc = loc ? locKey(loc.lat, loc.lon) : '';
    roundStartedAt = Date.now();
    $('panoBlock').classList.toggle('hidden', move !== 'nmpz');
    $('backToStart').classList.toggle('hidden', move !== 'move' || navigator);
    $('rerollBtn').classList.toggle('hidden', mode() === 'custom' || navigator);
    $('roleCover').classList.toggle('hidden', !navigator);
    $('roleNote').classList.toggle('hidden', !isScout());
    $('mini').classList.toggle('scout-hide', isScout());
    $('loading').classList.remove('hidden');
    $('loading').textContent = 'Загружаем панораму...';
    setBet(1);
    $('betBox').classList.toggle('hidden', mode() !== 'bets');
    // как на сервере: 5% размаха карты, но не больше 25 км
    $('betSafe').textContent = 'если промах до ' + formatDist(Math.min(state.settings.maxDist * 0.05, 25000));
    setGuess(null);
    startTimer(state.left);

    initMiniMap();
    // пока экран игры был скрыт, у карты был нулевой размер: сначала размер, потом границы
    miniMap.invalidateSize();
    miniMap.fitBounds(regionBounds(region()));

    // штурман панорамы не видит: место ему сервер не присылает
    if (navigator || !loc) {
      if (player) { player.destroy(); player = null; }
      clearFrames();
      $('loading').classList.add('hidden');
      return;
    }

    // Google и уличные панорамы Яндекса показываем встроенным окном без ключа,
    // плеер Яндекса с API нужен только для воздушных шаров
    if (usesEmbed()) {
      if (player) { player.destroy(); player = null; }
      $('pano').classList.add('hidden');
      $('addrCover').classList.remove('hidden');
      const url = embedUrl(loc);
      const old = frame();
      const sp = spareFrame();
      const t0 = performance.now();
      const ready = (pre) => {
        $('loading').classList.add('hidden');
        YGMetrics.pano(performance.now() - t0, pre);
      };
      if (sp.dataset.url === url) {
        // панорама уже загружена, пока были итоги: меняем окна местами
        activeFrame = 1 - activeFrame;
        sp.classList.remove('preload', 'hidden');
        if (sp.dataset.loaded) ready(true);
        else sp.onload = () => ready(true);
      } else {
        old.onload = () => ready(false);
        setFrame(old, url);
        old.classList.remove('hidden', 'preload');
      }
      // второе окно освобождаем: в нем потом загрузится следующий раунд
      const other = spareFrame();
      setFrame(other, 'about:blank');
      other.classList.add('hidden');
      other.classList.remove('preload');
      return;
    }
    clearFrames();
    $('addrCover').classList.add('hidden');
    $('pano').classList.remove('hidden');

    try {
      await loadYmaps();
    } catch (e) {
      $('loading').textContent = e.message || String(e);
      return;
    }
    if (state.round !== round || state.state !== 'round') return;

    let pano = panoCache[locKey(loc.lat, loc.lon)] || null;
    if (!pano) {
      try {
        pano = await locate([loc.lat, loc.lon], state.settings.layer);
      } catch (e) { /* ниже покажем ошибку */ }
    }
    if (state.round !== round) return;
    if (!pano) {
      $('loading').textContent = 'Панорама не загрузилась, ставь метку наугад';
      return;
    }
    patchMarkers(pano);
    startPano = pano;
    if (player) player.destroy();
    player = new ymaps.panorama.Player('pano', pano, {
      // с воздуха смотрим чуть вниз, на город
      direction: [loc.dir, state.settings.layer === 'air' ? -15 : 0],
      // без panoramaName, там название улицы; в NMPZ и зума нет
      controls: move === 'nmpz' ? [] : ['zoomControl'],
      suppressMapOpenBlock: true,     // без ссылки «Открыть в Яндекс Картах»
      hotkeysEnabled: move !== 'nmpz',
    });
    player.events.add('panoramachange', () => {
      // без перемещения: любой шаг по стрелке возвращаем на старт
      if (move !== 'move' && player.getPanorama() !== startPano) player.setPanorama(startPano);
    });
    $('loading').classList.add('hidden');
  }

  function embedUrl(loc) {
    const dir = Math.round(loc.dir || 0);
    if (isGoogle()) {
      return 'https://www.google.com/maps?layer=c&cbll=' + loc.lat + ',' + loc.lon +
        '&cbp=11,' + dir + ',0,0,0&output=svembed&hl=ru';
    }
    const ll = loc.lon + '%2C' + loc.lat;
    return 'https://yandex.ru/map-widget/v1/?ll=' + ll + '&z=17&l=stv&panorama%5Bpoint%5D=' + ll +
      '&panorama%5Bdirection%5D=' + dir + '%2C0&panorama%5Bspan%5D=120%2C60';
  }

  function updateMyScore() {
    const mine = state.players.find((p) => p.id === myId);
    $('scoreTotal').textContent = mine ? mine.score : 0;
  }

  function renderGuessStatus() {
    const duel = isDuel();
    $('endBtn').classList.toggle('hidden', !isHost());
    $('guessStatus').innerHTML = state.players.length < 2 && !duel ? '' : state.players.filter((p) => p.online).map((p) =>
      '<li class="' + (p.guessed ? 'done' : '') + (p.out ? ' out' : '') + '"><span class="dot" style="background:' +
      colorOf(p.id) + '"></span>' + esc(p.name) + (p.id === state.scout ? ' <i>(разведчик)</i>' : '') +
      (duel ? ' <span class="hpbar"><i style="width:' + (p.hp / 60) + '%"></i></span> ' + p.hp : '') + '</li>').join('');
    renderHp();
    const mine = meP();
    if (mine && mine.out) {
      $('guessBtn').disabled = true;
      $('guessBtn').textContent = duel ? 'Ты выбыл, смотришь' : 'Ты зритель';
    } else if (isScout()) {
      $('guessBtn').disabled = true;
      $('guessBtn').textContent = 'Метку ставит штурман';
    } else if (mine && mine.guessed) {
      const left = state.players.filter((p) => p.online && !p.guessed && !p.out && p.id !== state.scout).length;
      $('guessBtn').disabled = true;
      $('guessBtn').textContent = left ? 'Ждем остальных: ' + left : 'Ответ принят';
    }
  }

  // Карты игры на Leaflet: бесплатные тайлы, лимит Яндекса не тратят. Подложку игрок
  // выбирает переключателем (mapstyle.js), stylePos - угол переключателя.
  function makeMap(id, stylePos) {
    const m = L.map(id, { worldCopyJump: true, zoomSnap: 0.5 });
    YGMapStyle.add(m, stylePos);
    m.attributionControl.setPrefix(false); // без флажка и ссылки Leaflet, на мини-карте мало места
    // карта меняет размер (наведение, уголок): Leaflet должен узнать новый размер.
    // Во время плавного раскрытия событий много, пересчитываем не чаще раза за кадр
    let pending = false;
    new ResizeObserver(() => {
      if (pending) return;
      pending = true;
      requestAnimationFrame(() => { pending = false; m.invalidateSize(); });
    }).observe($(id));
    return m;
  }

  function wrapLon(lon) { return ((lon + 540) % 360) - 180; }

  function pinIcon(color) {
    return L.divIcon({ className: '', html: '<div class="yg-pin" style="background:' + color + '"></div>', iconSize: [18, 18], iconAnchor: [9, 9] });
  }

  function initMiniMap() {
    if (miniMap) return;
    miniMap = makeMap('minimap');
    miniMap.on('click', (e) => {
      if (sentRound !== state.round && !isScout()) setGuess([e.latlng.lat, wrapLon(e.latlng.lng)]);
    });
  }

  // ставка задается до ответа, сервер видит ее и в метке (на случай конца времени)
  function setBet(n) {
    bet = n;
    for (const b of document.querySelectorAll('#betBox button')) b.classList.toggle('on', Number(b.dataset.bet) === n);
    sendMark();
  }
  $('betBox').addEventListener('click', (e) => {
    const n = Number(e.target.dataset.bet);
    if (n && state && sentRound !== state.round) setBet(n);
  });

  function setGuess(coords) {
    guess = coords;
    const btn = $('guessBtn');
    btn.disabled = !coords;
    btn.textContent = coords ? 'Угадать' : 'Поставь метку на карте';
    if (!miniMap) return;
    if (!coords) {
      if (guessMark) guessMark.remove();
      guessMark = null;
      return;
    }
    if (!guessMark) {
      guessMark = L.marker(coords, { icon: pinIcon('#e6493a'), draggable: true }).addTo(miniMap);
      guessMark.on('dragend', () => {
        const p = guessMark.getLatLng();
        guess = [p.lat, wrapLon(p.lng)];
        sendMark();
      });
    } else {
      guessMark.setLatLng(coords);
    }
    sendMark();
  }

  // сервер помнит неподтвержденную метку и сам засчитает ее, если время раунда выйдет
  function sendMark() {
    if (guess && state && state.state === 'round' && sentRound !== state.round) {
      send({ t: 'mark', round: state.round, lat: guess[0], lon: guess[1], bet });
    }
  }

  function submitGuess() {
    if (!state || state.state !== 'round' || sentRound === state.round || !guess || isScout()) return;
    sentRound = state.round;
    stopTimer();
    if (guessMark) guessMark.dragging.disable();
    send({ t: 'guess', round: state.round, lat: guess[0], lon: guess[1], bet });
    $('guessBtn').disabled = true;
    $('guessBtn').textContent = 'Отправлено';
  }

  // ---------- Таймер ----------

  function startTimer(leftMs) {
    stopTimer();
    $('timerBox').classList.toggle('hidden', !leftMs);
    if (!leftMs) return;
    deadline = Date.now() + leftMs - GRACE_MS;
    let prevLeft = null;
    const tick = () => {
      const left = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
      $('timerVal').textContent = Math.floor(left / 60) + ':' + String(left % 60).padStart(2, '0');
      $('timerBox').classList.toggle('low', left <= 10);
      if (mode() === 'speed') {
        // как считает сервер: x1 сразу, x0.5 к концу полного времени раунда
        const el = (Date.now() - roundStartedAt - GRACE_MS) / 1000;
        const f = 0.5 + 0.5 * Math.max(0, Math.min(1, 1 - el / state.settings.timer));
        $('speedVal').textContent = 'x' + f.toFixed(2).replace('.', ',') +
          (state.players.some((p) => p.guessed) ? '' : ' (+20% первому)');
      }
      // звук только на смене секунды, с первого тика таймера молчим
      if (prevLeft !== null && left !== prevLeft) {
        if (prevLeft > 30 && left <= 30) YGSound.warn();
        if (left >= 1 && left <= 15) YGSound.tick(left);
      }
      prevLeft = left;
      if (left === 0) {
        stopTimer();
        submitGuess(); // что успел поставить, то и отправляем
      }
    };
    tick();
    timerId = setInterval(tick, 250);
  }

  function stopTimer() {
    clearInterval(timerId);
    timerId = null;
  }

  // ---------- Итоги ----------

  function formatDist(d) {
    if (d < 1000) return Math.round(d) + ' м';
    return (d / 1000).toLocaleString('ru-RU', { maximumFractionDigits: d < 100000 ? 1 : 0 }) + ' км';
  }

  function panoramaLink(a) {
    if (isGoogle()) {
      return { url: 'https://www.google.com/maps/@?api=1&map_action=pano&viewpoint=' + a.lat + ',' + a.lon, name: 'Google Картах' };
    }
    const ll = a.lon + '%2C' + a.lat;
    return { url: 'https://yandex.ru/maps/?ll=' + ll + '&panorama%5Bpoint%5D=' + ll + '&z=17', name: 'Яндекс Картах' };
  }

  let resultLayer = null;
  let resultPoints = [];

  function prepareResultMap() {
    show('result');
    if (!resultMap) {
      resultMap = makeMap('resultMap', 'topleft');
      resultLayer = L.layerGroup().addTo(resultMap);
    }
    resultMap.invalidateSize();
    resultLayer.clearLayers();
    resultPoints = [];
  }

  function addRound(rv, label) {
    const icon = L.divIcon({ className: '', html: '<div class="yg-answer">' + (label || '&#10003;') + '</div>', iconSize: null, iconAnchor: [12, 12] });
    // у каждого игрока свое место (два взгляда, загадай место): флажок на каждом
    const answers = {};
    for (const l of rv.targets ? Object.values(rv.targets) : [rv.answer]) answers[locKey(l.lat, l.lon)] = l;
    for (const l of Object.values(answers)) {
      resultPoints.push([l.lat, l.lon]);
      L.marker([l.lat, l.lon], { icon, zIndexOffset: 1000 }).bindTooltip('Здесь была панорама').addTo(resultLayer);
    }
    for (const g of rv.guesses) {
      const color = colorOf(g.id);
      const p = [g.lat, g.lon];
      const a = g.ans ? [g.ans.lat, g.ans.lon] : [rv.answer.lat, rv.answer.lon];
      resultPoints.push(p);
      L.polyline([p, a], { color, weight: 3, dashArray: '6 6' }).addTo(resultLayer);
      L.marker(p, { icon: pinIcon(color) }).bindTooltip(esc(g.name) + ': ' + formatDist(g.d)).addTo(resultLayer);
    }
    if (rv.mid) {
      // общая экспедиция: в зачет середина между ответами
      const midIcon = L.divIcon({ className: '', html: '<div class="yg-mid">&#9733;</div>', iconSize: [22, 22], iconAnchor: [11, 11] });
      L.marker([rv.mid.lat, rv.mid.lon], { icon: midIcon, zIndexOffset: 900 }).bindTooltip('Середина ответов: ' + rv.team).addTo(resultLayer);
      L.polyline([[rv.mid.lat, rv.mid.lon], [rv.answer.lat, rv.answer.lon]], { color: '#2B2A26', weight: 2 }).addTo(resultLayer);
    }
  }

  function signed(n) { return (n > 0 ? '+' : n < 0 ? '-' : '') + Math.abs(n); }
  function mult(x) { return 'x' + String(x).replace('.', ','); }

  function fitResultMap(fallback, final) {
    if (resultPoints.length < 2) resultMap.setView(fallback, 15);
    else resultMap.fitBounds(resultPoints, window.YGSkin ? YGSkin.resultPad(final) : { padding: [40, 40], maxZoom: 16 });
  }

  // канат в итогах: та же полоска, что в игре
  function tugHtml() {
    return '<div class="tug-bar inline"><span class="tug-name tug-me"></span><div class="tug-track"><i></i></div>' +
      '<span class="tug-name tug-them"></span></div>';
  }

  function nameCell(p) {
    return '<td class="' + (p.id === myId ? 'me' : '') + '"><span class="dot" style="background:' +
      colorOf(p.id) + '"></span> ' + esc(p.name) + '</td>';
  }

  async function showResult() {
    stopTimer();
    // уходим с панорамы: встроенное окно больше не нужно, следующее место начнет грузиться в запасном
    setFrame(frame(), 'about:blank');
    frame().classList.add('hidden');
    const s = state;
    const final = s.state === 'final';
    const rounds = s.results;
    const last = rounds[rounds.length - 1];
    prepareResultMap();
    rounds.forEach((rv, i) => addRound(rv, final ? String(i + 1) : ''));
    fitResultMap([last.answer.lat, last.answer.lon], final);

    const m = s.settings.mode;
    const duel = m === 'duel';
    const tug = m === 'tug';
    const team = m === 'coop' || m === 'scout';
    // захват стран: владельцев считает клиент по контурам стран
    let conq = null;
    if (m === 'conquer') {
      try {
        conq = await YGModes.conquer(final ? rounds : [last], final ? 1 : s.round, resultMap, final);
      } catch (e) {
        toast(e.message || String(e));
      }
    }
    const lands = (p) => (conq && conq.counts[p.id]) || 0;
    const players = s.players.slice().sort((a, b) => {
      if (duel) return b.hp - a.hp || b.score - a.score;
      if (tug) return b.pull - a.pull;
      if (conq) return lands(b) - lands(a) || b.score - a.score;
      return b.score - a.score;
    });
    const roundHead = rounds.map((_, i) => '<th>' + (i + 1) + '</th>').join('');
    let html;
    if (final && tug) {
      const lead = players[0];
      $('resultText').innerHTML = (Math.abs(lead.pull) >= (s.tugWin || 10000)
        ? 'Канат перетянул <b>' + esc(lead.name) + '</b>'
        : lead.pull > 0 ? 'Канат ближе к <b>' + esc(lead.name) + '</b> (' + signed(lead.pull) + ')' : 'Ничья') + tugHtml();
      html = '<tr><th>Игрок</th>' + roundHead + '<th>Канат</th></tr>';
      for (const p of players) {
        html += '<tr>' + nameCell(p) + rounds.map((rv) => '<td>' + signed((rv.damage || {})[p.id] || 0) + '</td>').join('') +
          '<td><b>' + signed(p.pull) + '</b></td></tr>';
      }
      $('nextBtn').textContent = 'В лобби';
    } else if (final && team) {
      const ok = s.team >= s.teamGoal;
      $('resultText').innerHTML = 'Команда: <b>' + s.team + '</b> из ' + s.teamGoal + '<br>' +
        (ok ? 'Экспедиция удалась' : 'Экспедиция не дотянула ' + (s.teamGoal - s.team));
      html = '<tr><th>Игрок</th>' + roundHead + '<th>Всего</th></tr>';
      for (const p of players) {
        html += '<tr>' + nameCell(p) + rounds.map((rv) => {
          const g = rv.guesses.find((x) => x.id === p.id);
          return '<td>' + (rv.scout === p.id ? '<i>разв.</i>' : g ? g.score : 0) + '</td>';
        }).join('') + '<td><b>' + p.score + '</b></td></tr>';
      }
      html += '<tr class="team-row"><td>Команда</td>' + rounds.map((rv) => '<td>' + (rv.team || 0) + '</td>').join('') +
        '<td><b>' + s.team + '</b></td></tr>';
      $('nextBtn').textContent = 'В лобби';
    } else if (final && conq) {
      $('resultText').innerHTML = lands(players[0]) && lands(players[0]) !== lands(players[1] || {})
        ? 'Больше всех стран у <b>' + esc(players[0].name) + '</b>: ' + lands(players[0])
        : 'По странам ничья, решают очки: <b>' + esc(players[0].name) + '</b>';
      html = '<tr><th>Игрок</th><th>Стран</th><th>Очки</th></tr>';
      for (const p of players) html += '<tr>' + nameCell(p) + '<td><b>' + lands(p) + '</b></td><td>' + p.score + '</td></tr>';
      $('nextBtn').textContent = 'В лобби';
    } else if (final && duel) {
      $('resultText').innerHTML = 'Победитель дуэли: <b>' + esc(players[0].name) + '</b>, осталось ' + players[0].hp + ' HP';
      html = '<tr><th>Игрок</th>' + rounds.map((_, i) => '<th>' + (i + 1) + '</th>').join('') + '<th>HP</th></tr>';
      for (const p of players) {
        html += '<tr>' + nameCell(p) + rounds.map((rv) => {
          const d = (rv.damage || {})[p.id] || 0;
          return '<td class="' + (d ? 'dmg' : '') + '">' + (d ? '-' + d : '0') + '</td>';
        }).join('') + '<td><b>' + p.hp + '</b></td></tr>';
      }
      $('nextBtn').textContent = 'В лобби';
    } else if (final) {
      const max = MAX_SCORE * rounds.length;
      $('resultText').innerHTML = players.length > 1
        ? 'Победитель: <b>' + esc(players[0].name) + '</b>, ' + players[0].score + ' из ' + max
        : 'Итого: <b>' + players[0].score + '</b> из ' + max;
      html = '<tr><th>Игрок</th>' + rounds.map((_, i) => '<th>' + (i + 1) + '</th>').join('') + '<th>Всего</th></tr>';
      for (const p of players) {
        html += '<tr>' + nameCell(p) + rounds.map((rv) => {
          const g = rv.guesses.find((x) => x.id === p.id);
          return '<td>' + (g ? g.score : 0) + '</td>';
        }).join('') + '<td><b>' + p.score + '</b></td></tr>';
      }
      $('nextBtn').textContent = 'В лобби';
    } else {
      const mine = last.guesses.find((g) => g.id === myId);
      const myDmg = duel ? (last.damage || {})[myId] || 0 : 0;
      const myAns = (mine && mine.ans) || (last.targets && last.targets[myId]) || last.answer;
      let extra = '';
      if (mine && m === 'speed') extra += ' (' + mult(mine.mult) + ' за скорость)';
      if (mine && m === 'bets' && mine.bet > 1) extra += '<br>Ставка ' + mult(mine.bet) + (mine.score < 0 ? ' не сыграла' : ' сыграла');
      if (duel) {
        extra += '<br>Урон: <b>' + (myDmg ? '-' + myDmg : '0') + '</b>' + (last.mult > 1 ? ' (' + mult(last.mult) + ')' : '');
      }
      if (tug) {
        const sh = (last.damage || {})[myId] || 0;
        extra += '<br>Канат: <b>' + signed(sh) + '</b> ' + (sh >= 0 ? 'к тебе' : 'к сопернику') +
          (last.mult > 1 ? ' (' + mult(last.mult) + ')' : '') + tugHtml();
      }
      if (team) extra += '<br>Команде за раунд: <b>' + (last.team || 0) + '</b>, всего ' + s.team + ' из ' + s.teamGoal;
      if (last.from && last.from[myId]) {
        const author = s.players.find((p) => p.id === last.from[myId]);
        extra += '<br>Место загадал' + (author ? ': ' + esc(author.name) : 'и соперники');
      }
      if (conq) {
        const owner = s.players.find((p) => p.id === conq.owner);
        extra += '<br>' + (conq.country ? 'Страна: ' + esc(conq.country) + ', ' : '') +
          (owner ? 'захватил <b>' + esc(owner.name) + '</b>' : 'ничья, страна свободна');
      }
      $('resultText').innerHTML = (mine
        ? 'Промах: <b>' + formatDist(mine.d) + '</b>, очки: <b>' + mine.score + '</b>' + (m === 'bets' || m === 'speed' ? '' : ' из ' + MAX_SCORE)
        : last.scout === myId ? 'В этом раунде ты был разведчиком' : 'Ты не успел ответить, очки: <b>0</b>') + extra +
        '<br><a href="' + panoramaLink(myAns).url + '" target="_blank" rel="noopener">Открыть место в ' +
        panoramaLink(myAns).name + '</a>';
      const g0 = (p) => last.guesses.find((x) => x.id === p.id);
      if (tug) {
        html = '<tr><th>Игрок</th><th>Промах</th><th>Очки</th><th>Канат</th></tr>';
        for (const p of players) {
          const g = g0(p);
          html += '<tr>' + nameCell(p) + '<td>' + (g ? formatDist(g.d) : '-') + '</td><td>' + (g ? g.score : 0) +
            '</td><td><b>' + signed((last.damage || {})[p.id] || 0) + '</b></td></tr>';
        }
      } else if (m === 'speed' || m === 'bets') {
        html = '<tr><th>Игрок</th><th>Промах</th><th>Очки</th><th>' + (m === 'speed' ? 'Скорость' : 'Ставка') +
          '</th><th>Раунд</th><th>Всего</th></tr>';
        for (const p of players) {
          const g = g0(p);
          html += '<tr>' + nameCell(p) + '<td>' + (g ? formatDist(g.d) : '-') + '</td><td>' + (g ? g.base : 0) + '</td><td>' +
            (g ? mult(m === 'speed' ? g.mult : g.bet) : '-') + '</td><td class="' + (g && g.score < 0 ? 'dmg' : '') + '">' +
            (g ? g.score : 0) + '</td><td><b>' + p.score + '</b></td></tr>';
        }
      } else if (conq) {
        html = '<tr><th>Игрок</th><th>Промах</th><th>Раунд</th><th>Стран</th></tr>';
        for (const p of players) {
          const g = g0(p);
          html += '<tr>' + nameCell(p) + '<td>' + (g ? formatDist(g.d) : '-') + '</td><td>' + (g ? g.score : 0) +
            '</td><td><b>' + lands(p) + '</b></td></tr>';
        }
      } else if (duel) {
        html = '<tr><th>Игрок</th><th>Промах</th><th>Очки</th><th>Урон</th><th>HP</th></tr>';
        for (const p of players) {
          const g = last.guesses.find((x) => x.id === p.id);
          const d = (last.damage || {})[p.id] || 0;
          html += '<tr>' + nameCell(p) + '<td>' + (g ? formatDist(g.d) : '-') + '</td><td>' + (g ? g.score : 0) +
            '</td><td class="' + (d ? 'dmg' : '') + '">' + (d ? '-' + d : '0') + '</td><td><b>' +
            (p.out ? 'выбыл' : p.hp) + '</b></td></tr>';
        }
      } else {
        html = '<tr><th>Игрок</th><th>Промах</th><th>Раунд</th><th>Всего</th></tr>';
        for (const p of players) {
          const g = last.guesses.find((x) => x.id === p.id);
          html += '<tr>' + nameCell(p) + '<td>' + (g ? formatDist(g.d) : '-') + '</td><td>' + (g ? g.score : 0) +
            '</td><td><b>' + p.score + '</b></td></tr>';
        }
      }
      $('nextBtn').textContent = s.last ? 'Итоги игры' : 'Следующий раунд';
    }
    $('resultTable').innerHTML = '<table>' + html + '</table>';
    const rt = document.querySelector('#resultText .tug-bar');
    if (rt) renderTug(rt.querySelector('.tug-me'), rt.querySelector('.tug-them'), rt.querySelector('i'));
    if (window.YGSkin) YGSkin.result({ final, duel, rounds, myId });
    if (final) YGSound.final();
    else YGSound.end();
    YGMetrics.flush(s.state, { round: s.round, mode: m, provider: s.settings.provider, region: s.settings.region,
      style: (document.querySelector('#minimap .ms-cur') || {}).textContent || '' });
  }

  // ---------- Обработчики ----------

  async function enter(code) {
    $('menuError').textContent = '';
    try {
      if (!ZENO) await saveNick();
    } catch (e) {
      $('menuError').textContent = e.message;
      return;
    }
    // API Яндекса грузим только когда он реально нужен: каждая загрузка тратит лимит
    connect(code);
  }

  $('createBtn').addEventListener('click', () => enter(''));
  $('joinBtn').addEventListener('click', () => {
    const code = $('roomCode').value.trim().toUpperCase();
    if (code) enter(code);
  });
  $('roomCode').addEventListener('keydown', (e) => { if (e.key === 'Enter') $('joinBtn').click(); });

  $('leaveBtn').addEventListener('click', () => {
    if (ZENO) {
      toZeno({ yg: 'leave' });
      return;
    }
    leaving = true;
    roomCode = null;
    state = null;
    if (ws) ws.close();
    history.replaceState(null, '', location.pathname);
    show('menu');
  });
  $('copyLink').addEventListener('click', () => {
    const inp = $('inviteLink');
    inp.select();
    (navigator.clipboard ? navigator.clipboard.writeText(inp.value) : Promise.reject())
      .then(() => toast('Ссылка скопирована'), () => document.execCommand('copy'));
  });

  $('playerList').addEventListener('click', (e) => {
    const kick = e.target.dataset.kick;
    const host = e.target.dataset.host;
    if (kick) {
      const p = state.players.find((x) => x.id === kick);
      if (p && confirm('Выгнать ' + p.name + ' из комнаты?')) send({ t: 'kick', id: kick });
    }
    if (host) send({ t: 'makeHost', id: host });
  });

  for (const id of SETTING_IDS) {
    $(id).addEventListener('change', () => {
      if (id === 'provider') fillRegionSelect();
      updateModeHint();
      if (!isHost()) return;
      const st = settingsFromForm();
      storageSet(SETTINGS_STORAGE, JSON.stringify(st));
      send({ t: 'settings', settings: st });
    });
  }

  $('startBtn').addEventListener('click', startGame);
  $('mapsBtn').addEventListener('click', () => YGEditor.openList());

  // общее для редактора карт (editor.js)
  window.YG = {
    show, toast, esc, loadCustomMaps, customMaps, makeMap, me: () => me,
    // для modes.js: загадай место, захват стран
    send, colorOf, formatDist, regionBounds, region, myId: () => myId,
  };
  $('guessBtn').addEventListener('click', submitGuess);
  $('nextBtn').addEventListener('click', () => send({ t: 'next' }));
  $('backToStart').addEventListener('click', () => {
    if (player && startPano) player.setPanorama(startPano);
    // во встроенном окне вернуться на старт = открыть место заново
    else if (!frame().classList.contains('hidden') && state && state.loc) setFrame(frame(), embedUrl(state.loc));
  });
  // хост заканчивает игру досрочно, например когда в регионе кончились панорамы
  function endGame() {
    if (!state || !isHost() || !['round', 'reveal', 'pick'].includes(state.state)) return;
    if (confirm('Закончить игру? Итоги будут по сыгранным раундам.')) send({ t: 'end' });
  }
  $('endBtn').addEventListener('click', endGame);
  $('endBtn2').addEventListener('click', endGame);
  $('pickEnd').addEventListener('click', endGame);

  function renderSound() { $('soundBtn').textContent = YGSound.isOn() ? 'Звук' : 'Без звука'; }
  $('soundBtn').addEventListener('click', (e) => {
    YGSound.toggle();
    renderSound();
    e.currentTarget.blur(); // иначе пробел (ответ) снова нажмет кнопку
  });
  renderSound();
  $('rerollBtn').addEventListener('click', () => {
    if (!state || state.state !== 'round') return;
    if (confirm('Панорама не открылась? Хост подберет другое место, ответы этого раунда сбросятся.')) {
      send({ t: 'reroll', round: state.round });
    }
  });
  // ---------- Размер мини-карты ----------
  // Свернутая карта маленькая, при наведении или закреплении разворачивается
  // до размера, который игрок выбрал кнопками или потянув за уголок.
  const MINI_STORAGE = 'yaguessr.mini';
  const MINI_SIZES = [[320, 240], [480, 360], [640, 480], [860, 620], [1200, 860]];
  const mini = { w: 480, h: 360, pinned: false };
  try { Object.assign(mini, JSON.parse(storageGet(MINI_STORAGE) || '{}')); } catch (e) { /* по умолчанию */ }

  function applyMini(save) {
    mini.w = Math.max(260, Math.min(mini.w, window.innerWidth - 32));
    mini.h = Math.max(200, Math.min(mini.h, window.innerHeight - 90));
    $('mini').style.setProperty('--mw', mini.w + 'px');
    $('mini').style.setProperty('--mh', mini.h + 'px');
    $('mini').classList.toggle('pinned', mini.pinned);
    if (save) storageSet(MINI_STORAGE, JSON.stringify(mini));
  }

  function stepMini(dir) {
    // следующий размер из списка больше или меньше текущего
    const list = dir > 0 ? MINI_SIZES : MINI_SIZES.slice().reverse();
    const next = list.find(([w]) => (dir > 0 ? w > mini.w + 10 : w < mini.w - 10));
    if (next) [mini.w, mini.h] = next;
    applyMini(true);
  }

  $('pinMini').addEventListener('click', () => { mini.pinned = !mini.pinned; applyMini(true); });
  $('miniBigger').addEventListener('click', () => stepMini(1));
  $('miniSmaller').addEventListener('click', () => stepMini(-1));

  // карта прижата к правому нижнему углу, поэтому тянем за левый верхний
  $('miniResize').addEventListener('pointerdown', (e) => {
    e.preventDefault();
    const el = $('mini');
    const rect = el.getBoundingClientRect();
    const start = { x: e.clientX, y: e.clientY, w: rect.width, h: rect.height };
    el.classList.add('dragging');
    $('miniResize').setPointerCapture(e.pointerId);
    const move = (ev) => {
      mini.w = start.w + (start.x - ev.clientX);
      mini.h = start.h + (start.y - ev.clientY);
      applyMini(false);
    };
    const up = () => {
      el.classList.remove('dragging');
      $('miniResize').removeEventListener('pointermove', move);
      $('miniResize').removeEventListener('pointerup', up);
      $('miniResize').removeEventListener('pointercancel', up);
      applyMini(true);
    };
    $('miniResize').addEventListener('pointermove', move);
    $('miniResize').addEventListener('pointerup', up);
    $('miniResize').addEventListener('pointercancel', up);
  });

  window.addEventListener('resize', () => applyMini(false));
  applyMini(false);

  document.addEventListener('keydown', (e) => {
    if (e.key !== ' ' && e.key !== 'Enter') return;
    if (e.target.tagName === 'INPUT') return;
    if (!$('result').classList.contains('hidden') && isHost()) {
      e.preventDefault();
      send({ t: 'next' });
    } else if (!$('game').classList.contains('hidden') && e.key === ' ') {
      e.preventDefault();
      submitGuess();
    }
  });
  if (ZENO) {
    // ключ Яндекса - в настройках комнаты, меню в Zeno не показывается
    $('provider').after($('keyBlock'));
    const keyVis = () => $('keyBlock').classList.toggle('hidden', !!configKey || $('provider').value !== 'yandex');
    $('provider').addEventListener('change', keyVis);
    keyVis();
    enter(ZENO === 'guest' ? (ZQ.get('room') || '').toUpperCase() : (ZQ.get('room') || ''));
  }
})();
