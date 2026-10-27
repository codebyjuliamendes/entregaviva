'use strict';

// Public tracking page. Looks up an order by its code and follows it through
// the per-order public stream (/api/orders/track/{code}/events), which only
// carries privacy-safe fields.

const $ = (selector) => document.querySelector(selector);

const ESCAPES = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
const esc = (value) => String(value ?? '').replace(/[&<>"']/g, (c) => ESCAPES[c]);

const LABELS = {
  recebido: 'Pedido recebido',
  preparando: 'Na cozinha',
  pronto: 'Pedido pronto',
  em_entrega: 'Saiu para entrega',
  entregue: 'Pedido entregue',
  cancelado: 'Pedido cancelado',
};
const STEPS = ['recebido', 'preparando', 'pronto', 'em_entrega', 'entregue'];
const STEP_LABELS = ['Recebido', 'Preparando', 'Pronto', 'Em rota', 'Entregue'];
const TERMINAL = new Set(['entregue', 'cancelado']);
const LIVE_TEXT = {
  connecting: 'Conectando…',
  live: 'Atualização ao vivo',
  reconnecting: 'Reconectando…',
  offline: 'Sem conexão ao vivo',
};
const dateTime = new Intl.DateTimeFormat('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' });
const clock = new Intl.DateTimeFormat('pt-BR', { hour: '2-digit', minute: '2-digit' });

let tracked = null; // last order payload
let source = null;
let requestSeq = 0;
let liveState = 'connecting';

function parseDate(value) {
  if (!value) return null;
  const s = String(value);
  const d = new Date(s.includes('T') ? s : s.replace(' ', 'T') + 'Z');
  return Number.isNaN(d.getTime()) ? null : d;
}

function remaining(order) {
  const created = parseDate(order.createdAt);
  if (!created) return order.remainingMinutes ?? 0;
  return Math.max(0, order.estimatedMinutes - Math.max(0, Math.floor((Date.now() - created.getTime()) / 60000)));
}

function etaText(order) {
  if (order.status === 'entregue') return 'Entrega concluída';
  if (order.status === 'cancelado') return 'Pedido cancelado pelo restaurante';
  const minutes = remaining(order);
  const eta = parseDate(order.estimatedDeliveryAt);
  if (!minutes) return 'A entrega pode acontecer a qualquer momento';
  return `Previsão: cerca de ${minutes} min${eta ? ` (até ${clock.format(eta)})` : ''}`;
}

function render(order) {
  tracked = order;
  const current = STEPS.indexOf(order.status);
  const steps = STEPS.map((status, i) => {
    let cls = 'todo';
    if (i < current) cls = 'done';
    else if (i === current) cls = 'current';
    return `<li class="step ${cls}"${i === current ? ' aria-current="step"' : ''}>${esc(STEP_LABELS[i])}</li>`;
  }).join('');
  const events = order.events
    .map((event, i) => {
      const d = parseDate(event.createdAt);
      const last = i === order.events.length - 1;
      return `<li class="track-event${last ? ' latest' : ''}"><i aria-hidden="true"></i><div>${esc(LABELS[event.status] || event.status)}<small>${d ? `<time datetime="${esc(d.toISOString())}">${esc(dateTime.format(d))}</time>` : ''}</small></div></li>`;
    })
    .join('');
  const live = TERMINAL.has(order.status)
    ? ''
    : `<span class="live" role="status" data-state="${esc(liveState)}"><i aria-hidden="true"></i><span class="live-text">${esc(LIVE_TEXT[liveState])}</span></span>`;
  $('#result').innerHTML = `<section class="track-result" data-status="${esc(order.status)}">
    <div class="result-head">
      <div><p class="eyebrow">Pedido #${esc(order.code)}</p><h2>${esc(order.restaurant)}</h2></div>
      ${live}
    </div>
    <p class="status-line">${esc(LABELS[order.status] || order.status)}</p>
    <p class="eta">${esc(etaText(order))}</p>
    ${order.status === 'cancelado' ? '' : `<ol class="steps" aria-label="Etapas da entrega">${steps}</ol>`}
    <h3 class="timeline-title">Histórico</h3>
    <ol class="timeline">${events}</ol>
  </section>`;
}

function setLive(next) {
  liveState = next;
  const el = document.querySelector('#result .live');
  if (!el) return;
  el.dataset.state = next;
  el.querySelector('.live-text').textContent = LIVE_TEXT[next];
}

function stopStream() {
  if (source) source.close();
  source = null;
}

function follow(code) {
  stopStream();
  if (tracked && TERMINAL.has(tracked.status)) return;
  setLive('connecting');
  const es = new EventSource(`/api/orders/track/${encodeURIComponent(code)}/events`);
  source = es;
  es.addEventListener('connected', () => setLive('live'));
  es.addEventListener('resync', () => lookup(code, { quiet: true }));
  es.addEventListener('order', (event) => {
    try {
      const order = JSON.parse(event.data);
      if (order.code !== code) return;
      liveState = 'live';
      render(order);
      if (TERMINAL.has(order.status)) stopStream();
    } catch {
      lookup(code, { quiet: true });
    }
  });
  es.addEventListener('error', () => {
    if (source !== es) return;
    if (es.readyState === EventSource.CLOSED) {
      setLive('offline');
      stopStream();
      setTimeout(() => {
        if (!source && tracked && tracked.code === code && !TERMINAL.has(tracked.status)) lookup(code, { quiet: true });
      }, 5000);
    } else {
      setLive('reconnecting');
    }
  });
}

async function lookup(code, { quiet = false } = {}) {
  const seq = ++requestSeq;
  const button = $('#track-form [type="submit"]');
  const error = $('#error');
  if (!quiet) {
    error.textContent = '';
    button.disabled = true;
    button.textContent = 'Buscando…';
    $('#result').setAttribute('aria-busy', 'true');
  }
  try {
    const response = await fetch(`/api/orders/track/${encodeURIComponent(code)}`, { headers: { Accept: 'application/json' } });
    let body = null;
    try {
      body = await response.json();
    } catch {
      /* ignore */
    }
    if (seq !== requestSeq) return; // a newer lookup superseded this one
    if (!response.ok) throw new Error((body && body.error) || 'Não foi possível encontrar o pedido.');
    render(body);
    follow(body.code);
    const url = new URL(window.location.href);
    url.searchParams.set('code', body.code);
    window.history.replaceState(null, '', url);
  } catch (e) {
    if (seq !== requestSeq) return;
    if (!quiet) {
      stopStream();
      tracked = null;
      $('#result').innerHTML = '';
      error.textContent = e.message || 'Não foi possível consultar agora. Verifique a conexão.';
      $('#code').setAttribute('aria-invalid', 'true');
      $('#code').focus();
    } else {
      setLive('offline');
    }
  } finally {
    if (!quiet && seq === requestSeq) {
      button.disabled = false;
      button.textContent = 'Acompanhar';
      $('#result').removeAttribute('aria-busy');
    }
  }
}

$('#track-form').addEventListener('submit', (event) => {
  event.preventDefault();
  const input = $('#code');
  const code = input.value.trim().toUpperCase();
  input.removeAttribute('aria-invalid');
  if (!code) {
    $('#error').textContent = 'Informe o código de acompanhamento.';
    input.setAttribute('aria-invalid', 'true');
    input.focus();
    return;
  }
  if ($('#track-form [type="submit"]').disabled) return;
  lookup(code);
});

// Refresh the countdown without contacting the server.
setInterval(() => {
  if (tracked && !TERMINAL.has(tracked.status) && !document.hidden) render(tracked);
}, 30000);

const initial = new URLSearchParams(window.location.search).get('code');
if (initial) {
  $('#code').value = initial.trim().toUpperCase().slice(0, 16);
  lookup($('#code').value);
}
