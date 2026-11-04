// Оформление «Экспедиция»: полевой журнал, штампы, полароиды. Включено по умолчанию,
// старый вид по ссылке ?skin=classic (обратно ?skin=exp), выбор помнит браузер.
// В старом виде YGSkin нет, и game.js с editor.js работают как раньше.
(function () {
  'use strict';

  const KEY = 'yaguessr.skin';
  let skin = new URLSearchParams(location.search).get('skin');
  try {
    if (skin) localStorage.setItem(KEY, skin);
    else skin = localStorage.getItem(KEY);
  } catch (e) { /* приватный режим */ }
  if (skin === 'classic') return;

  document.documentElement.classList.add('exp');
  for (const href of [
    'https://fonts.googleapis.com/css2?family=PT+Serif:ital@0;1&family=PT+Mono&family=Caveat:wght@400;600&display=swap',
    'expedition.css',
  ]) {
    const l = document.createElement('link');
    l.rel = 'stylesheet';
    l.href = href;
    document.head.appendChild(l);
  }

  const $ = (id) => document.getElementById(id);
  // цвета игроков: чернила вместо ярких маркеров
  const INKS = ['#B23A2E', '#3F6A8A', '#6B7A3A', '#B07A1F', '#6A4C7A', '#2F6F6A', '#8C4A6B', '#5A4A36'];

  // ---------- Маршрутный лист: выбор обводится, а не выпадает списком ----------
  // Настоящие select остаются (их читает game.js), кружки только меняют их значение.
  const CHIPS = ['provider', 'mode', 'rounds', 'timer', 'guessTimer', 'layer', 'move', 'coopLevel', 'coopRule'];

  function shortText(o) {
    return o.textContent.split(': ')[0].replace(/\s*\(.*\)\s*/, '').trim();
  }

  function renderChips(sel) {
    const box = sel.nextElementSibling;
    if (!box || !box.classList.contains('chips')) return;
    box.innerHTML = '';
    for (const o of sel.options) {
      const s = document.createElement('span');
      s.textContent = shortText(o);
      s.title = o.textContent;
      s.dataset.v = o.value;
      if (o.disabled) s.className = 'off';
      else if (o.value === sel.value) s.className = 'on';
      box.appendChild(s);
    }
  }

  function syncChips() {
    for (const id of CHIPS) if ($(id)) renderChips($(id));
  }

  function initChips() {
    for (const id of CHIPS) {
      const sel = $(id);
      if (!sel) continue;
      const box = document.createElement('div');
      box.className = 'chips';
      sel.after(box);
      box.addEventListener('click', (e) => {
        const v = e.target.dataset.v;
        const o = v !== undefined && Array.from(sel.options).find((x) => x.value === v);
        if (!o || o.disabled || sel.value === v || sel.closest('fieldset').disabled) return;
        sel.value = v;
        sel.dispatchEvent(new Event('change'));
      });
      // game.js по change может поменять и соседние настройки, поэтому обновляем все
      sel.addEventListener('change', syncChips);
    }
    syncChips();
  }

  // ---------- Название места по координатам ----------
  // Nominatim (OpenStreetMap) просит не чаще запроса в секунду: запросы идут очередью.
  const cities = {};
  let queue = Promise.resolve();

  function city(lat, lon) {
    const k = lat.toFixed(3) + ',' + lon.toFixed(3);
    if (!cities[k]) {
      const p = queue.then(() => fetch('https://nominatim.openstreetmap.org/reverse?format=jsonv2&zoom=10&accept-language=ru&lat=' +
        lat + '&lon=' + lon))
        .then((r) => r.json())
        .then((d) => {
          const a = d.address || {};
          const name = a.city || a.town || a.village || a.hamlet || a.suburb || a.municipality || a.county || a.state || '';
          return { name, country: name && a.country !== name ? a.country || '' : '' };
        })
        .catch(() => ({ name: '', country: '' }));
      queue = p.then(() => new Promise((r) => setTimeout(r, 1100)));
      cities[k] = p;
    }
    return cities[k];
  }

  // ---------- Итоги: открытка со штампом, в конце паспорт с визами ----------

  function slot(id, before) {
    let el = $(id);
    if (!el) {
      el = document.createElement('div');
      el.id = id;
      before.parentNode.insertBefore(el, before);
    }
    el.innerHTML = '';
    return el;
  }

  function result(r) {
    const panel = document.querySelector('.result-panel');
    panel.classList.toggle('final', r.final);
    const head = slot('skinHead', $('resultText'));
    const visas = slot('skinVisas', $('resultTable'));
    const last = r.rounds[r.rounds.length - 1];

    if (!r.final) {
      const g = last.guesses.find((x) => x.id === r.myId);
      const dmg = r.duel ? (last.damage || {})[r.myId] || 0 : 0;
      head.innerHTML = '<span class="hand">это было</span><div class="place">...</div><div class="hand country"></div>' +
        '<div class="stamp">' + (r.duel ? (dmg ? '-' + dmg : '0') + '<small>урон</small>'
          : (g ? g.score : 0) + '<small>очков</small>') + '</div>';
      const place = head.querySelector('.place');
      city(last.answer.lat, last.answer.lon).then((c) => {
        place.textContent = c.name || 'где-то на карте';
        head.querySelector('.country').textContent = c.country;
      });
      return;
    }

    const total = r.rounds.reduce((sum, rv) => {
      const g = rv.guesses.find((x) => x.id === r.myId);
      return sum + (g ? g.score : 0);
    }, 0);
    head.innerHTML = '<span class="hand">итог экспедиции</span>' + (r.duel ? ''
      : '<div class="total">' + total.toLocaleString('ru-RU') + '<small> из ' + (r.rounds.length * 5000).toLocaleString('ru-RU') + '</small></div>');
    visas.innerHTML = '<div class="lbl">отметки о въезде</div>' + r.rounds.map((rv, i) => {
      const g = rv.guesses.find((x) => x.id === r.myId);
      const rot = ((i * 37) % 27) - 13;
      return '<div class="visa s' + (i % 4) + '" style="color:' + INKS[i % INKS.length] + ';--r:' + rot +
        'deg;animation-delay:' + (0.15 + i * 0.12).toFixed(2) + 's"><span class="city">' + (i + 1) +
        '</span><b>' + (g ? g.score : 0) + '</b></div>';
    }).join('');
    visas.querySelectorAll('.city').forEach((el, i) => {
      const a = r.rounds[i].answer;
      city(a.lat, a.lon).then((c) => { if (c.name) el.textContent = c.name; });
    });
  }

  // карта итогов уходит под открытку справа (или снизу на телефоне): точки держим в видимой части
  function resultPad(final) {
    if (window.innerWidth > 700) {
      return { paddingTopLeft: [40, 40], paddingBottomRight: [(final ? 480 : 390) + 60, 40], maxZoom: 16 };
    }
    return { paddingTopLeft: [30, 30], paddingBottomRight: [30, Math.round(window.innerHeight * 0.52) + 30], maxZoom: 16 };
  }

  window.YGSkin = {
    colors: INKS,
    ink: '#2B2A26',
    zoneFill: { stroke: false, fillColor: '#B23A2E', fillOpacity: 0.28, interactive: false },
    lobby: syncChips,
    result,
    resultPad,
  };

  document.addEventListener('DOMContentLoaded', initChips);
})();
