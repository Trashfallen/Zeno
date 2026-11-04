// Редактор своих карт: на карте мира рисуются круги (кисть, круг, ластик),
// внутри них игра выбирает места для раундов. Карта на Leaflet + OpenStreetMap.
window.YGEditor = (function () {
  'use strict';

  const $ = (id) => document.getElementById(id);
  const MAX_ZONES = 3000;
  const FILL = (window.YGSkin && YGSkin.zoneFill) || { stroke: false, fillColor: '#ffcc33', fillOpacity: 0.4, interactive: false };

  let map = null;
  let layer = null;       // круги карты
  let preview = null;     // круг под курсором / растягиваемый круг
  let zones = [];         // { c: [lat, lon], r: км }
  let history = [];       // снимки zones для отмены
  let tool = 'brush';
  let drawing = false;
  let lastPoint = null;
  let circleCenter = null;
  let editingId = '';

  // ---------- Список карт ----------

  async function openList() {
    YG.show('maps');
    $('mapList').innerHTML = '<li class="muted">Загружаем...</li>';
    const maps = await YG.loadCustomMaps();
    const items = Object.values(maps);
    if (!items.length) {
      $('mapList').innerHTML = '<li class="muted">Своих карт пока нет</li>';
      return;
    }
    $('mapList').innerHTML = items.map((m) =>
      '<li><div><b>' + YG.esc(m.name) + '</b><br><span class="muted">' + YG.esc(m.authorNick || m.author) +
      ', областей: ' + m.zones.length + '</span></div>' +
      '<div class="map-actions"><button data-edit="' + m.id + '">Изменить</button>' +
      '<button data-del="' + m.id + '">Удалить</button></div></li>').join('');
  }

  $('mapList').addEventListener('click', async (e) => {
    const editId = e.target.dataset.edit;
    const delId = e.target.dataset.del;
    if (editId) openEditor(YG.customMaps['m_' + editId]);
    if (delId) {
      const m = YG.customMaps['m_' + delId];
      if (!confirm('Удалить карту «' + m.name + '»?')) return;
      await fetch('api/maps?id=' + encodeURIComponent(delId), { method: 'DELETE' });
      openList();
    }
  });

  // ---------- Редактор ----------

  function openEditor(m) {
    YG.show('editor');
    editingId = m ? m.id : '';
    $('mapName').value = m ? m.name : '';
    $('editorError').textContent = '';
    zones = m ? m.zones.map((z) => ({ c: z.c.slice(), r: z.r })) : [];
    history = [];
    initMap();
    map.invalidateSize();
    redraw();
    if (zones.length) {
      map.fitBounds(layer.getBounds(), { padding: [30, 30] });
    } else {
      map.setView([50, 30], 3);
    }
    setTool('brush');
  }

  function initMap() {
    if (map) return;
    map = YG.makeMap('editMap', 'bottomright');
    // без начального вида Leaflet не кладет круги на карту и fitBounds падает
    map.setView([50, 30], 3);
    // панель инструментов сверху, поэтому кнопки масштаба внизу
    map.zoomControl.setPosition('bottomright');
    map.doubleClickZoom.disable();
    layer = L.featureGroup().addTo(map);
    preview = L.circle([0, 0], { radius: 1, color: (window.YGSkin && YGSkin.ink) || '#ffcc33', weight: 2, fillColor: '#ffffff', fillOpacity: 0.15, interactive: false });

    map.on('mousedown', onDown);
    map.on('mousemove', onMove);
    map.on('mouseup', onUp);
    map.on('mouseout', onUp);
  }

  function circleOf(z) {
    return L.circle(z.c, Object.assign({ radius: z.r * 1000 }, FILL));
  }

  function showPreview(visible) {
    if (visible && !map.hasLayer(preview)) preview.addTo(map);
    if (!visible && map.hasLayer(preview)) preview.remove();
  }

  function redraw() {
    layer.clearLayers();
    for (const z of zones) layer.addLayer(circleOf(z));
    updateInfo();
  }

  function updateInfo() {
    $('zoneCount').textContent = 'Областей: ' + zones.length + (zones.length >= MAX_ZONES ? ' (максимум)' : '');
    $('undoBtn').disabled = !history.length;
  }

  // радиус кисти в км для текущего масштаба
  function brushKm(lat) {
    const px = Number($('brushSize').value);
    const metersPerPx = 40075016.686 * Math.cos(lat * Math.PI / 180) / Math.pow(2, map.getZoom() + 8);
    return Math.max(0.05, Math.min(3000, px * metersPerPx / 1000));
  }

  function dist(a, b) { return L.latLng(a).distanceTo(L.latLng(b)) / 1000; }
  function pt(e) { return [e.latlng.lat, e.latlng.lng]; }

  function remember() {
    history.push(zones.map((z) => ({ c: z.c.slice(), r: z.r })));
    if (history.length > 50) history.shift();
  }

  function addZone(c, r) {
    if (zones.length >= MAX_ZONES) return;
    const z = { c: c.slice(), r };
    zones.push(z);
    layer.addLayer(circleOf(z));
  }

  function eraseAt(c) {
    const r = brushKm(c[0]);
    const before = zones.length;
    zones = zones.filter((z) => dist(z.c, c) > r);
    if (zones.length !== before) redraw();
  }

  function onDown(e) {
    if (tool === 'pan') return;
    const c = pt(e);
    drawing = true;
    remember();
    if (tool === 'brush') {
      addZone(c, brushKm(c[0]));
      lastPoint = c;
    } else if (tool === 'erase') {
      eraseAt(c);
    } else if (tool === 'circle') {
      circleCenter = c;
      preview.setLatLng(c);
      preview.setRadius(1);
      showPreview(true);
    }
    updateInfo();
  }

  function onMove(e) {
    const c = pt(e);
    if (!drawing) {
      // круг под курсором показывает размер кисти и ластика
      const visible = tool === 'brush' || tool === 'erase';
      showPreview(visible);
      if (visible) {
        preview.setLatLng(c);
        preview.setRadius(brushKm(c[0]) * 1000);
      }
      return;
    }
    if (tool === 'brush') {
      preview.setLatLng(c);
      const r = brushKm(c[0]);
      // круги ставим с перекрытием и достраиваем между событиями мыши,
      // чтобы при быстром движении мазок оставался сплошным
      const step = r * 0.6;
      const d = dist(lastPoint, c);
      if (d >= step) {
        const n = Math.floor(d / step);
        const from = lastPoint;
        for (let i = 1; i <= n; i++) {
          const k = (i * step) / d;
          lastPoint = [from[0] + (c[0] - from[0]) * k, from[1] + (c[1] - from[1]) * k];
          addZone(lastPoint, r);
        }
        updateInfo();
      }
    } else if (tool === 'erase') {
      preview.setLatLng(c);
      eraseAt(c);
    } else if (tool === 'circle') {
      preview.setRadius(dist(circleCenter, c) * 1000);
    }
  }

  function onUp(e) {
    if (!drawing) return;
    drawing = false;
    if (tool === 'circle' && circleCenter) {
      const r = e.latlng ? dist(circleCenter, pt(e)) : 0;
      if (r >= 0.05) addZone(circleCenter, Math.min(r, 3000));
      else history.pop(); // случайный клик без растягивания
      circleCenter = null;
      showPreview(false);
    }
    updateInfo();
  }

  function setTool(t) {
    tool = t;
    for (const b of document.querySelectorAll('[data-tool]')) b.classList.toggle('active', b.dataset.tool === t);
    if (t === 'pan') {
      map.dragging.enable();
      showPreview(false);
    } else {
      map.dragging.disable();
    }
    $('brushBox').classList.toggle('hidden', t === 'pan' || t === 'circle');
  }

  async function save() {
    $('editorError').textContent = '';
    const body = {
      id: editingId,
      name: $('mapName').value.trim(),
      zones: zones.map((z) => ({ c: z.c, r: Math.round(z.r * 1000) / 1000, w: z.r * z.r })),
    };
    const res = await fetch('api/maps', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).catch(() => null);
    if (!res || !res.ok) {
      $('editorError').textContent = res ? (await res.text()).trim() : 'Сервер недоступен';
      return;
    }
    YG.toast('Карта сохранена');
    openList();
  }

  // ---------- Кнопки ----------

  for (const b of document.querySelectorAll('[data-tool]')) {
    b.addEventListener('click', () => setTool(b.dataset.tool));
  }
  $('undoBtn').addEventListener('click', () => {
    if (!history.length) return;
    zones = history.pop();
    redraw();
  });
  $('clearBtn').addEventListener('click', () => {
    if (!zones.length || !confirm('Стереть все области?')) return;
    remember();
    zones = [];
    redraw();
  });
  $('saveMapBtn').addEventListener('click', save);

  // поиск города через бесплатный геокодер OpenStreetMap
  $('placeSearch').addEventListener('keydown', async (e) => {
    if (e.key !== 'Enter' || !e.target.value.trim()) return;
    try {
      const q = encodeURIComponent(e.target.value.trim());
      const list = await (await fetch('https://nominatim.openstreetmap.org/search?format=json&limit=1&accept-language=ru&q=' + q)).json();
      if (!list.length) {
        YG.toast('Ничего не нашлось');
        return;
      }
      const b = list[0].boundingbox.map(Number); // [юг, север, запад, восток]
      map.fitBounds([[b[0], b[2]], [b[1], b[3]]], { maxZoom: 12 });
    } catch (err) {
      YG.toast('Поиск не ответил');
    }
  });
  $('editorBack').addEventListener('click', openList);
  $('newMapBtn').addEventListener('click', () => openEditor(null));
  $('mapsBack').addEventListener('click', () => YG.show('menu'));

  document.addEventListener('keydown', (e) => {
    if ($('editor').classList.contains('hidden') || e.target.tagName === 'INPUT') return;
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'z') {
      e.preventDefault();
      $('undoBtn').click();
    }
  });

  return { openList };
})();
