// Стили карт игры (мини-карта, итоги, редактор): переключатель в углу карты.
// Выбор общий для всех карт и запоминается в браузере.
window.YGMapStyle = (function () {
  'use strict';

  const KEY = 'yaguessr.mapstyle';
  const OSM = 'https://tile.openstreetmap.org/{z}/{x}/{y}.png';
  const ESRI = 'https://server.arcgisonline.com/ArcGIS/rest/services/';
  const OSM_ATTR = '&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap</a>';
  const SAT = [ESRI + 'World_Imagery/MapServer/tile/{z}/{y}/{x}', { maxZoom: 19, attribution: 'Esri, Maxar, Earthstar Geographics' }];

  // base: обычные карты, остальные стилизованы под старые карты (cls добавляет фильтры и бумагу).
  // bg - цвет под тайлами в тон карте: пока тайл грузится, на его месте нет яркого квадрата
  const STYLES = {
    scheme: { name: 'Схема', base: true, bg: '#f2efe9', layers: [[OSM, { maxZoom: 19, attribution: OSM_ATTR }]] },
    sat: { name: 'Спутник', base: true, bg: '#2b3326', layers: [SAT] },
    hybrid: {
      name: 'Гибрид', base: true, bg: '#2b3326', layers: [
        SAT,
        [ESRI + 'Reference/World_Transportation/MapServer/tile/{z}/{y}/{x}', { maxZoom: 19 }],
        [ESRI + 'Reference/World_Boundaries_and_Places/MapServer/tile/{z}/{y}/{x}', { maxZoom: 19 }],
      ],
    },
    parchment: { name: 'Пергамент', cls: 'ms-parchment ms-paper', bg: '#e6dcc3', layers: [[OSM, { maxZoom: 19, attribution: OSM_ATTR }]] },
    topo: {
      name: 'Топокарта', cls: 'ms-topo ms-paper', bg: '#e8e2cf', layers: [['https://{s}.tile.opentopomap.org/{z}/{x}/{y}.png', {
        maxZoom: 17, subdomains: 'abc',
        attribution: OSM_ATTR + ', <a href="https://opentopomap.org" target="_blank" rel="noopener">OpenTopoMap</a>',
      }]],
    },
    // атлас National Geographic есть только на мелких масштабах, ближе подменяем схемой в сепии
    atlas: {
      name: 'Атлас', cls: 'ms-atlas ms-paper', bg: '#e2d7bb', layers: [
        [ESRI + 'NatGeo_World_Map/MapServer/tile/{z}/{y}/{x}', { maxZoom: 9, attribution: 'Esri, National Geographic' }],
        [OSM, { minZoom: 10, maxZoom: 19, attribution: OSM_ATTR, className: 'ms-osm' }],
      ],
    },
  };
  const ALL_CLS = Object.values(STYLES).flatMap((s) => (s.cls || '').split(' ')).filter(Boolean);

  let cur = null;
  try { cur = localStorage.getItem(KEY); } catch (e) { /* приватный режим */ }
  if (!STYLES[cur]) cur = document.documentElement.classList.contains('exp') ? 'parchment' : 'scheme';

  const maps = [];

  // Тайлы с запасом: Leaflet грузит только видимую область, и при раскрытии мини-карты,
  // сдвиге или зуме края дорисовываются квадратами. Берем еще кольцо в тайл вокруг,
  // а уже загруженные дольше держим в памяти.
  const PaddedTileLayer = L.TileLayer.extend({
    _getTiledPixelBounds(center) {
      const b = L.TileLayer.prototype._getTiledPixelBounds.call(this, center);
      const pad = L.point(256, 256);
      return L.bounds(b.min.subtract(pad), b.max.add(pad));
    },
  });

  function apply(entry) {
    const st = STYLES[cur];
    for (const l of entry.layers) entry.map.removeLayer(l);
    entry.layers = st.layers.map(([url, opt]) => {
      const l = new PaddedTileLayer(url, Object.assign({ keepBuffer: 8 }, opt));
      // замеры: сколько грузится каждый тайл (metrics.js)
      l.on('tileloadstart', (e) => { e.tile.ygStart = performance.now(); });
      l.on('tileload', (e) => window.YGMetrics && YGMetrics.tile(performance.now() - e.tile.ygStart, true, e.tile.src));
      l.on('tileerror', (e) => window.YGMetrics && YGMetrics.tile(0, false, e.tile.src));
      return l.addTo(entry.map);
    });
    const box = entry.map.getContainer();
    box.style.background = st.bg;
    box.classList.remove(...ALL_CLS);
    if (st.cls) box.classList.add(...st.cls.split(' '));
    entry.el.querySelector('.ms-cur').textContent = st.name;
    entry.el.querySelectorAll('li[data-s]').forEach((li) => li.classList.toggle('on', li.dataset.s === cur));
  }

  function set(s) {
    cur = s;
    try { localStorage.setItem(KEY, s); } catch (e) { /* приватный режим */ }
    maps.forEach(apply);
  }

  const Control = L.Control.extend({
    onAdd() {
      const el = L.DomUtil.create('div', 'ms-ctl');
      let html = '';
      for (const [k, s] of Object.entries(STYLES)) {
        if (!s.base && !html.includes('ms-sep')) html += '<li class="ms-sep"></li>';
        html += '<li data-s="' + k + '">' + s.name + '</li>';
      }
      el.innerHTML = '<button type="button" class="ms-btn" title="Стиль карты">&#9776; <span class="ms-cur"></span></button>' +
        '<ul class="ms-list">' + html + '</ul>';
      // клик по переключателю не должен ставить метку на мини-карте
      L.DomEvent.disableClickPropagation(el);
      L.DomEvent.disableScrollPropagation(el);
      el.querySelector('.ms-btn').addEventListener('click', () => el.classList.toggle('open'));
      el.querySelector('.ms-list').addEventListener('click', (e) => {
        const s = e.target.dataset.s;
        if (!s) return;
        el.classList.remove('open');
        set(s);
      });
      return el;
    },
  });

  // add подключает к карте слои текущего стиля и переключатель (по умолчанию в левом нижнем углу)
  function add(map, position) {
    const ctl = new Control({ position: position || 'bottomleft' }).addTo(map);
    const entry = { map, el: ctl.getContainer(), layers: [] };
    maps.push(entry);
    apply(entry);
  }

  return { add };
})();
