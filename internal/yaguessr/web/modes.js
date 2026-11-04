// Режимы игры на клиенте: экран «Загадай место» и захват стран.
// game.js отдает сюда свое состояние через window.YG.
window.YGModes = (function () {
  'use strict';

  const $ = (id) => document.getElementById(id);

  // ---------- Загадай место ----------

  let pickMap = null;
  let pickMark = null;
  let foundMark = null;
  let pickPoint = null;

  function pinIcon(color, cls) {
    return L.divIcon({ className: '', html: '<div class="yg-pin ' + (cls || '') + '" style="background:' + color + '"></div>', iconSize: [18, 18], iconAnchor: [9, 9] });
  }

  function enterPick(st) {
    YG.show('pick');
    if (!pickMap) {
      pickMap = YG.makeMap('pickMap', 'topleft');
      pickMap.on('click', (e) => {
        pickPoint = [e.latlng.lat, ((e.latlng.lng + 540) % 360) - 180];
        if (pickMark) pickMark.setLatLng(e.latlng);
        else pickMark = L.marker(e.latlng, { icon: pinIcon('#B23A2E') }).addTo(pickMap);
        $('pickBtn').disabled = false;
        $('pickInfo').textContent = '';
      });
    }
    pickMap.invalidateSize();
    // новый раунд загадывания: прошлую точку убираем
    if (st.round !== enterPick.round) {
      enterPick.round = st.round;
      if (pickMark) pickMark.remove();
      if (foundMark) foundMark.remove();
      pickMark = foundMark = pickPoint = null;
      $('pickBtn').disabled = true;
      $('pickInfo').textContent = '';
      pickMap.fitBounds(YG.regionBounds(YG.region()));
    }
    $('pickRound').textContent = st.round + 1;
    $('pickStatus').innerHTML = st.players.filter((p) => p.online && !p.out).map((p) =>
      '<li class="' + (p.picked ? 'done' : '') + '"><span class="dot" style="background:' + YG.colorOf(p.id) + '"></span>' +
      YG.esc(p.name) + (p.picking ? ' <i>ищем панораму...</i>' : p.picked ? '' : ' <i>думает</i>') + '</li>').join('');
    const me = st.players.find((p) => p.id === YG.myId());
    $('pickBtn').textContent = me && me.picked ? 'Загадать другое' : 'Загадать';
  }

  $('pickBtn').addEventListener('click', () => {
    if (!pickPoint) return;
    YG.send({ t: 'pick', lat: pickPoint[0], lon: pickPoint[1] });
    $('pickBtn').disabled = true;
    $('pickInfo').textContent = 'Ищем панораму рядом...';
  });

  function picked(m) {
    if (foundMark) foundMark.remove();
    foundMark = L.marker([m.loc.lat, m.loc.lon], { icon: pinIcon('#2e9d4a') }).addTo(pickMap);
    $('pickInfo').textContent = 'Панорама найдена в ' + YG.formatDist(m.d) + ' от точки. Ждем остальных, пока можно передумать';
    $('pickBtn').disabled = false;
  }

  function pickError(msg) {
    $('pickInfo').textContent = msg;
    $('pickBtn').disabled = false;
  }

  // ---------- Захват стран ----------
  // Страна раунда достается тому, кто ответил точнее всех. Страны считает клиент
  // по контурам Natural Earth (world-atlas), у всех игроков результат одинаковый.

  const WORLD_URL = 'https://cdn.jsdelivr.net/npm/world-atlas@2.0.2/countries-50m.json';
  const TOPO_URL = 'https://cdn.jsdelivr.net/npm/topojson-client@3.1.0/dist/topojson-client.min.js';
  let countriesPromise = null;
  const owners = {}; // номер раунда -> { country, owner }
  let territoryLayer = null;

  function loadScript(src) {
    return new Promise((resolve, reject) => {
      const s = document.createElement('script');
      s.src = src;
      s.onload = resolve;
      s.onerror = () => reject(new Error('Не загрузились контуры стран'));
      document.head.appendChild(s);
    });
  }

  // русские названия: номер страны ISO 3166 из world-atlas -> двухбуквенный код -> имя
  const ISO_URL = 'https://cdn.jsdelivr.net/npm/i18n-iso-countries@7.11.0/';

  function ruNames() {
    return Promise.all([fetch(ISO_URL + 'codes.json').then((r) => r.json()), fetch(ISO_URL + 'langs/ru.json').then((r) => r.json())])
      .then(([codes, ru]) => {
        const byNum = {};
        for (const [a2, , num] of codes) {
          const n = ru.countries[a2];
          if (n) byNum[num] = Array.isArray(n) ? n[0] : n;
        }
        return byNum;
      })
      .catch(() => ({})); // без русских названий останутся английские
  }

  function countries() {
    if (!countriesPromise) {
      countriesPromise = Promise.all([loadScript(TOPO_URL), fetch(WORLD_URL).then((r) => r.json()), ruNames()])
        .then(([, world, names]) => topojson.feature(world, world.objects.countries).features.map((f) => {
          f.bbox = bboxOf(f.geometry);
          f.properties.name = names[f.id] || f.properties.name;
          return f;
        }))
        .catch((e) => { countriesPromise = null; throw e; });
    }
    return countriesPromise;
  }

  function bboxOf(g) {
    let w = 180, s = 90, e = -180, n = -90;
    const polys = g.type === 'Polygon' ? [g.coordinates] : g.coordinates;
    for (const poly of polys) {
      for (const [x, y] of poly[0]) {
        w = Math.min(w, x); e = Math.max(e, x); s = Math.min(s, y); n = Math.max(n, y);
      }
    }
    return [w, s, e, n];
  }

  function inRing(x, y, ring) {
    let inside = false;
    for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
      const [xi, yi] = ring[i];
      const [xj, yj] = ring[j];
      if ((yi > y) !== (yj > y) && x < (xj - xi) * (y - yi) / (yj - yi) + xi) inside = !inside;
    }
    return inside;
  }

  function countryAt(list, lat, lon) {
    for (const f of list) {
      const b = f.bbox;
      if (lon < b[0] || lon > b[2] || lat < b[1] || lat > b[3]) continue;
      const polys = f.geometry.type === 'Polygon' ? [f.geometry.coordinates] : f.geometry.coordinates;
      for (const poly of polys) {
        if (inRing(lon, lat, poly[0]) && !poly.slice(1).some((hole) => inRing(lon, lat, hole))) return f;
      }
    }
    return null;
  }

  // победитель раунда: лучший ответ, при ничьей страна никому не достается
  function roundWinner(rv) {
    let best = null;
    let tie = false;
    for (const g of rv.guesses) {
      if (!best || g.score > best.score) { best = g; tie = false; } else if (g.score === best.score) tie = true;
    }
    return best && !tie && best.score > 0 ? best.id : '';
  }

  // conquer дописывает владельцев стран по раундам и рисует их на карте итогов.
  // first - номер первого раунда в rounds (в итогах раунда приходит только последний).
  async function conquer(rounds, first, map, final) {
    const list = await countries();
    if (final) for (const k of Object.keys(owners)) delete owners[k];
    rounds.forEach((rv, i) => {
      const f = countryAt(list, rv.answer.lat, rv.answer.lon);
      owners[first + i] = { country: f, owner: roundWinner(rv) };
    });
    // у страны остается последний захватчик
    const byCountry = {};
    Object.keys(owners).sort((a, b) => a - b).forEach((n) => {
      const o = owners[n];
      if (o.country && o.owner) byCountry[o.country.properties.name] = o;
    });
    const counts = {};
    for (const o of Object.values(byCountry)) counts[o.owner] = (counts[o.owner] || 0) + 1;

    if (territoryLayer) territoryLayer.remove();
    territoryLayer = L.geoJSON(Object.values(byCountry).map((o) => Object.assign({}, o.country, { properties: Object.assign({}, o.country.properties, { owner: o.owner }) })), {
      style: (f) => ({ color: YG.colorOf(f.properties.owner), weight: 2, fillColor: YG.colorOf(f.properties.owner), fillOpacity: 0.3 }),
      interactive: false,
    }).addTo(map);

    const last = owners[first + rounds.length - 1];
    return {
      counts,
      country: last.country ? last.country.properties.name : '',
      owner: last.owner,
    };
  }

  function resetConquer() {
    for (const k of Object.keys(owners)) delete owners[k];
    if (territoryLayer) { territoryLayer.remove(); territoryLayer = null; }
  }

  return { enterPick, picked, pickError, conquer, resetConquer, countries };
})();
