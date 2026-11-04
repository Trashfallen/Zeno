// Звуки раунда без файлов, через Web Audio: звон за 30 с до конца, тиканье с 15 с,
// штамп в конце раунда и перезвон в конце игры. Кнопка в билете выключает, выбор помнит браузер.
window.YGSound = (function () {
  'use strict';

  const KEY = 'yaguessr.sound';
  let on = true;
  try { on = localStorage.getItem(KEY) !== 'off'; } catch (e) { /* приватный режим */ }
  let ctx = null;

  // браузер дает играть звук только после действия игрока: контекст создаем на первом клике или клавише
  function unlock() {
    if (!ctx) {
      const C = window.AudioContext || window.webkitAudioContext;
      if (!C) return;
      ctx = new C();
    }
    if (ctx.state === 'suspended') ctx.resume();
  }
  document.addEventListener('pointerdown', unlock, true);
  document.addEventListener('keydown', unlock, true);

  function ready() { return on && ctx && ctx.state === 'running'; }

  // одна нота: быстрая атака и плавное затухание
  function note(freq, start, dur, vol, type) {
    const t = ctx.currentTime + start;
    const o = ctx.createOscillator();
    const g = ctx.createGain();
    o.type = type || 'sine';
    o.frequency.setValueAtTime(freq, t);
    g.gain.setValueAtTime(0.0001, t);
    g.gain.exponentialRampToValueAtTime(vol, t + 0.008);
    g.gain.exponentialRampToValueAtTime(0.0001, t + dur);
    o.connect(g).connect(ctx.destination);
    o.start(t);
    o.stop(t + dur + 0.02);
  }

  // штамп: низкий удар и короткий шорох бумаги
  function thud() {
    const t = ctx.currentTime;
    const o = ctx.createOscillator();
    const g = ctx.createGain();
    o.frequency.setValueAtTime(140, t);
    o.frequency.exponentialRampToValueAtTime(50, t + 0.18);
    g.gain.setValueAtTime(0.5, t);
    g.gain.exponentialRampToValueAtTime(0.0001, t + 0.25);
    o.connect(g).connect(ctx.destination);
    o.start(t);
    o.stop(t + 0.3);

    const len = Math.floor(ctx.sampleRate * 0.12);
    const buf = ctx.createBuffer(1, len, ctx.sampleRate);
    const d = buf.getChannelData(0);
    for (let i = 0; i < len; i++) d[i] = (Math.random() * 2 - 1) * (1 - i / len);
    const n = ctx.createBufferSource();
    n.buffer = buf;
    const f = ctx.createBiquadFilter();
    f.type = 'lowpass';
    f.frequency.value = 1200;
    const ng = ctx.createGain();
    ng.gain.value = 0.25;
    n.connect(f).connect(ng).connect(ctx.destination);
    n.start(t);
  }

  // мягкий двойной звон: осталось 30 секунд
  function warn() {
    if (!ready()) return;
    note(659.25, 0, 0.9, 0.18, 'triangle');
    note(1318.5, 0, 0.5, 0.04);
    note(880, 0.18, 1.1, 0.18, 'triangle');
    note(1760, 0.18, 0.6, 0.04);
  }

  // деревянный «тук» как у маримбы, последние 5 секунд выше и громче
  function tick(left) {
    if (!ready()) return;
    const last = left <= 5;
    const f = last ? 1046.5 : 784;
    note(f, 0, 0.12, last ? 0.22 : 0.14);
    note(f * 2.76, 0, 0.05, last ? 0.05 : 0.03);
  }

  function end() {
    if (!ready()) return;
    thud();
    note(1567.98, 0.12, 0.9, 0.08, 'triangle');
  }

  function final() {
    if (!ready()) return;
    thud();
    [523.25, 659.25, 783.99, 1046.5].forEach((f, i) => note(f, 0.15 + i * 0.13, 1.0, 0.13, 'triangle'));
  }

  // соперник ответил: звонкое «динь-динь» вверх, не похоже на таймер
  function opponent() {
    if (!ready()) return;
    note(987.77, 0, 0.35, 0.16, 'triangle');
    note(1479.98, 0.11, 0.55, 0.16, 'triangle');
    note(2959.96, 0.11, 0.25, 0.03);
  }

  function toggle() {
    on = !on;
    try { localStorage.setItem(KEY, on ? 'on' : 'off'); } catch (e) { /* приватный режим */ }
    if (on) {
      unlock();
      tick(10); // слышно, что звук включился
    }
    return on;
  }

  return { warn, tick, end, final, opponent, toggle, isOn: () => on };
})();
