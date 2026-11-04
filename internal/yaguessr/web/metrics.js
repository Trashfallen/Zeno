// Замеры загрузки у игроков: тайлы карт, пустая мини-карта, панорама.
// Раз в раунд сводка уходит на сервер и пишется в его журнал (journalctl -u yaguessr).
window.YGMetrics = (function () {
  'use strict';

  let tiles = [];      // время загрузки тайлов, мс
  let tileErrors = 0;
  let blanks = [];     // сколько мс мини-карта стояла с недорисованными тайлами
  let emptyMs = 0;     // из них сколько мс на мини-карте не было ни одного тайла
  let panos = [];      // [мс до панорамы, была ли предзагрузка]
  const hosts = {};

  function tile(ms, ok, url) {
    if (!ok) tileErrors++;
    else tiles.push(Math.round(ms));
    try { const h = new URL(url).host; hosts[h] = (hosts[h] || 0) + 1; } catch (e) { /* без адреса */ }
  }

  function pano(ms, preloaded) { panos.push([Math.round(ms), preloaded ? 1 : 0]); }

  // мини-карту проверяем 4 раза в секунду: есть ли в видимой части недогруженные тайлы
  let blankSince = 0;
  let emptySince = 0;
  setInterval(() => {
    const box = document.getElementById('minimap');
    const game = document.getElementById('game');
    if (!box || !game || game.classList.contains('hidden') || !document.getElementById('result').classList.contains('hidden')) {
      blankSince = emptySince = 0;
      return;
    }
    const r = box.getBoundingClientRect();
    if (!r.width) return;
    let visible = 0;
    let missing = 0;
    for (const img of box.querySelectorAll('img.leaflet-tile')) {
      const b = img.getBoundingClientRect();
      if (b.right <= r.left || b.left >= r.right || b.bottom <= r.top || b.top >= r.bottom) continue;
      visible++;
      if (!img.classList.contains('leaflet-tile-loaded')) missing++;
    }
    const now = performance.now();
    if (missing) {
      if (!blankSince) blankSince = now;
    } else if (blankSince) {
      blanks.push(Math.round(now - blankSince));
      blankSince = 0;
    }
    const empty = !visible || missing === visible;
    if (empty && !emptySince) emptySince = now;
    if (!empty && emptySince) {
      emptyMs += now - emptySince;
      emptySince = 0;
    }
  }, 250);

  function pct(list, p) {
    if (!list.length) return 0;
    const s = list.slice().sort((a, b) => a - b);
    return s[Math.min(s.length - 1, Math.floor(s.length * p))];
  }

  // flush отправляет сводку и начинает новую; пустые сводки не шлем
  function flush(reason, extra) {
    if (!tiles.length && !tileErrors && !blanks.length && !panos.length) return;
    const body = Object.assign({
      reason,
      tiles: tiles.length, tileP50: pct(tiles, 0.5), tileP90: pct(tiles, 0.9), tileMax: pct(tiles, 1), tileErrors,
      blanks: blanks.length, blankP90: pct(blanks, 0.9), blankMax: pct(blanks, 1), emptyMs: Math.round(emptyMs),
      panos, hosts: Object.assign({}, hosts),
      conn: navigator.connection ? navigator.connection.effectiveType + ' ' + navigator.connection.rtt + 'ms' : '',
      screen: innerWidth + 'x' + innerHeight,
    }, extra || {});
    tiles = []; tileErrors = 0; blanks = []; emptyMs = 0; panos = [];
    for (const k of Object.keys(hosts)) delete hosts[k];
    try {
      navigator.sendBeacon('api/metrics', new Blob([JSON.stringify(body)], { type: 'application/json' }));
    } catch (e) { /* замеры не важнее игры */ }
  }

  addEventListener('pagehide', () => flush('pagehide'));

  return { tile, pano, flush };
})();
