'use strict';

// Operations panel: renders the board, sends status changes and keeps the
// screen current through Server-Sent Events (/api/events).

const $ = (selector) => document.querySelector(selector);

const ESCAPES = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
const esc = (value) => String(value ?? '').replace(/[&<>"']/g, (c) => ESCAPES[c]);

const money = new Intl.NumberFormat('pt-BR', { style: 'currency', currency: 'BRL' });
const clock = new Intl.DateTimeFormat('pt-BR', { hour: '2-digit', minute: '2-digit' });

const LANES = [
  ['recebido', 'Recebidos'],
  ['preparando', 'Na cozinha'],
  ['pronto', 'Prontos'],
  ['em_entrega', 'Em rota'],
];
const NEXT = {
  recebido: ['preparando', 'Iniciar preparo'],
  preparando: ['pronto', 'Marcar como pronto'],
  pronto: ['em_entrega', 'Saiu para entrega'],
  em_entrega: ['entregue', 'Confirmar entrega'],
};
const CANCELABLE = new Set(['recebido', 'preparando', 'pronto']);
const CLOSED = new Set(['entregue', 'cancelado']);
const CLOSED_SHOWN = 4;

const state = {
  orders: new Map(), // id -> order
  pending: new Set(), // order ids with a request in flight
  loaded: false,
};

// Accepts RFC 3339 and the legacy "YYYY-MM-DD HH:MM:SS" (UTC) format.
function parseDate(value) {
  if (!value) return null;
  const s = String(value);
  const d = new Date(s.includes('T') ? s : s.replace(' ', 'T') + 'Z');
  return Number.isNaN(d.getTime()) ? null : d;
}

function formatTime(value) {
  const d = parseDate(value);
  return d ? clock.format(d) : '—';
}

function remainingMinutes(order) {
  const created = parseDate(order.createdAt);
  if (!created) return order.remainingMinutes ?? 0;
  const elapsed = Math.floor((Date.now() - created.getTime()) / 60000);
  return Math.max(0, order.estimatedMinutes - Math.max(0, elapsed));
}

// Each change appends one history event, so the event count is a version.
function version(order) {
  return Array.isArray(order.events) ? order.events.length : 0;
}

function upsert(order) {
  const current = state.orders.get(order.id);
  if (current && version(current) > version(order)) return false;
  state.orders.set(order.id, order);
  return true;
}

async function api(url, options = {}) {
  const response = await fetch(url, {
    ...options,
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
  });
  let body = null;
  try {
    body = await response.json();
  } catch {
    /* non-JSON error page */
  }
  if (!response.ok) {
    const error = new Error((body && body.error) || 'Não foi possível concluir. Tente novamente.');
    error.status = response.status;
    error.fields = (body && body.fields) || {};
    throw error;
  }
  return body;
}

let toastTimer;
function toast(message) {
  const el = $('#toast');
  el.textContent = message;
  el.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove('show'), 3200);
}

function showBanner(message) {
  $('#banner-text').textContent = message;
  $('#banner').hidden = false;
}

function hideBanner() {
  $('#banner').hidden = true;
}

function renderCard(order) {
  const busy = state.pending.has(order.id);
  const disabled = busy ? ' disabled aria-busy="true"' : '';
  const next = NEXT[order.status];
  const code = esc(order.code);
  return `<article class="order-card" data-id="${esc(order.id)}">
    <div class="order-top"><span class="order-code">#${code}</span><span class="order-time">${esc(formatTime(order.createdAt))}</span></div>
    <p class="order-eta">~${esc(remainingMinutes(order))} min · previsão</p>
    <h4 class="order-restaurant">${esc(order.restaurant)}</h4>
    <p class="customer">${esc(order.customer)}</p>
    <p class="address"><span aria-hidden="true">⌖</span> ${esc(order.address)}</p>
    <p class="order-total">${esc(money.format(order.totalCents / 100))}</p>
    ${order.courier ? `<p class="courier"><span aria-hidden="true">◉</span> Entregador: ${esc(order.courier)}</p>` : ''}
    <div class="order-actions">
      ${next ? `<button type="button" class="advance" data-action="advance" data-id="${esc(order.id)}"${disabled}>${busy ? 'Salvando…' : esc(next[1])} <span aria-hidden="true">→</span></button>` : ''}
      ${CANCELABLE.has(order.status) ? `<button type="button" class="cancel" data-action="cancel" data-id="${esc(order.id)}" aria-label="Cancelar pedido #${code}" title="Cancelar pedido"${disabled}><span aria-hidden="true">×</span></button>` : ''}
    </div>
    <a class="card-link" href="/track.html?code=${encodeURIComponent(order.code)}" target="_blank" rel="noopener">Link de rastreio <span class="visually-hidden">do pedido #${code}</span></a>
  </article>`;
}

function renderClosedCard(order) {
  const label = order.status === 'entregue' ? 'Entregue' : 'Cancelado';
  return `<article class="order-card closed" data-status="${esc(order.status)}">
    <div class="order-top"><span class="order-code">#${esc(order.code)}</span><span class="order-time">${esc(formatTime(order.updatedAt))}</span></div>
    <h4 class="order-restaurant">${esc(order.restaurant)}</h4>
    <p class="customer">${esc(order.customer)} · <span class="closed-status">${esc(label)}</span></p>
  </article>`;
}

function lane(status, label, cards, total, empty) {
  return `<section class="lane" data-status="${esc(status)}" aria-label="${esc(label)}">
    <div class="lane-head"><h3 class="lane-name"><i aria-hidden="true"></i>${esc(label)}</h3><span class="lane-count">${esc(total)}</span></div>
    ${cards || `<p class="empty-lane">${esc(empty)}</p>`}
  </section>`;
}

function sortedActive(status) {
  return [...state.orders.values()].filter((o) => o.status === status).sort((a, b) => b.id - a.id);
}

function render() {
  const board = $('#board');
  const all = [...state.orders.values()];
  const count = (status) => all.filter((o) => o.status === status).length;
  const active = all.filter((o) => !CLOSED.has(o.status));
  $('#order-count').textContent = String(active.length);
  $('#s-received').textContent = String(count('recebido'));
  $('#s-preparing').textContent = String(count('preparando'));
  $('#s-ready').textContent = String(count('pronto'));
  $('#s-route').textContent = String(count('em_entrega'));

  const closed = all
    .filter((o) => CLOSED.has(o.status))
    .sort((a, b) => (parseDate(b.updatedAt) || 0) - (parseDate(a.updatedAt) || 0) || b.id - a.id);

  // Keep keyboard focus on the same control across re-renders.
  const focused = document.activeElement;
  const focusKey = focused && board.contains(focused) && focused.dataset.action ? `${focused.dataset.action}:${focused.dataset.id}` : null;

  board.innerHTML =
    LANES.map(([status, label]) => {
      const rows = sortedActive(status);
      return lane(status, label, rows.map(renderCard).join(''), rows.length, 'Nenhum pedido nesta etapa');
    }).join('') +
    lane('encerrado', 'Encerrados', closed.slice(0, CLOSED_SHOWN).map(renderClosedCard).join(''), closed.length, 'Ainda sem pedidos encerrados');
  board.setAttribute('aria-busy', 'false');

  if (focusKey) {
    const [action, id] = focusKey.split(':');
    const again = board.querySelector(`[data-action="${action}"][data-id="${CSS.escape(id)}"]`);
    if (again) again.focus();
  }
}

let loading = null;
async function load() {
  if (loading) return loading;
  loading = (async () => {
    try {
      const orders = await api('/api/orders');
      const fresh = new Map();
      for (const order of orders) {
        const current = state.orders.get(order.id);
        fresh.set(order.id, current && version(current) > version(order) ? current : order);
      }
      state.orders = fresh;
      state.loaded = true;
      hideBanner();
      render();
    } catch (error) {
      if (!state.loaded) {
        $('#board').innerHTML = '<p class="board-message">Não foi possível carregar os pedidos.</p>';
        $('#board').setAttribute('aria-busy', 'false');
      }
      showBanner(error.status === 401 ? 'Sessão expirada. Recarregue a página e entre novamente.' : 'Não foi possível carregar os pedidos. Verifique a conexão.');
    } finally {
      loading = null;
    }
  })();
  return loading;
}

async function changeStatus(order, status) {
  if (state.pending.has(order.id)) return;
  state.pending.add(order.id);
  render();
  try {
    const updated = await api(`/api/orders/${encodeURIComponent(order.id)}/status`, {
      method: 'PATCH',
      body: JSON.stringify({ status, from: order.status }),
    });
    upsert(updated);
    toast(status === 'cancelado' ? `Pedido #${updated.code} cancelado.` : `Pedido #${updated.code} atualizado.`);
  } catch (error) {
    toast(error.message);
    if (error.status === 409) await load();
  } finally {
    state.pending.delete(order.id);
    render();
  }
}

// Cancel confirmation dialog.
function confirmCancel(order) {
  const dialog = $('#cancel-dialog');
  $('#cancel-text').textContent = `O pedido #${order.code} de ${order.customer} sai do fluxo e não pode ser reaberto.`;
  dialog.returnValue = '';
  dialog.showModal();
  return new Promise((resolve) => {
    dialog.addEventListener('close', () => resolve(dialog.returnValue === 'confirm'), { once: true });
  });
}

$('#board').addEventListener('click', async (event) => {
  const button = event.target.closest('button[data-action]');
  if (!button || button.disabled) return;
  const order = state.orders.get(Number(button.dataset.id));
  if (!order) return;
  if (button.dataset.action === 'advance' && NEXT[order.status]) {
    await changeStatus(order, NEXT[order.status][0]);
  } else if (button.dataset.action === 'cancel' && (await confirmCancel(order))) {
    await changeStatus(order, 'cancelado');
  }
});

// New order dialog.
const orderDialog = $('#order-dialog');
const orderForm = $('#order-form');
let opener = null;

function clearErrors() {
  for (const el of orderForm.querySelectorAll('.field-error')) el.textContent = '';
  for (const el of orderForm.querySelectorAll('[aria-invalid]')) el.removeAttribute('aria-invalid');
  $('#form-error').textContent = '';
}

function showFieldErrors(fields) {
  let first = null;
  for (const [name, message] of Object.entries(fields)) {
    const input = orderForm.elements.namedItem(name);
    const slot = document.getElementById(`e-${name}`);
    if (slot) slot.textContent = message;
    if (input) {
      input.setAttribute('aria-invalid', 'true');
      first = first || input;
    }
  }
  if (first) first.focus();
}

function clientValidate(data) {
  const fields = {};
  for (const name of ['restaurant', 'customer', 'address']) {
    if (String(data[name] || '').trim().length < 3) fields[name] = 'Informe ao menos 3 caracteres.';
  }
  const total = Number(data.total);
  if (!Number.isFinite(total) || total < 1 || total > 5000) fields.total = 'Informe um valor entre R$ 1,00 e R$ 5.000,00.';
  const minutes = Number(data.estimatedMinutes);
  if (!Number.isInteger(minutes) || minutes < 10 || minutes > 180) fields.estimatedMinutes = 'Use entre 10 e 180 minutos.';
  return fields;
}

$('#new-order').addEventListener('click', (event) => {
  opener = event.currentTarget;
  orderForm.reset();
  clearErrors();
  orderDialog.showModal();
  $('#f-restaurant').focus();
});

orderDialog.addEventListener('click', (event) => {
  // Backdrop click (target is the dialog itself) or the close button.
  if (event.target === orderDialog || event.target.closest('[data-close]')) orderDialog.close();
});
orderDialog.addEventListener('close', () => {
  if (opener) opener.focus();
});

orderForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  const submit = orderForm.querySelector('[type="submit"]');
  if (submit.disabled) return; // double submit guard
  clearErrors();
  const data = Object.fromEntries(new FormData(orderForm));
  const fields = clientValidate(data);
  if (Object.keys(fields).length) {
    $('#form-error').textContent = 'Confira os campos destacados.';
    showFieldErrors(fields);
    return;
  }
  submit.disabled = true;
  submit.setAttribute('aria-busy', 'true');
  const label = submit.innerHTML;
  submit.textContent = 'Criando…';
  try {
    const created = await api('/api/orders', {
      method: 'POST',
      body: JSON.stringify({ ...data, total: Number(data.total), estimatedMinutes: Number(data.estimatedMinutes) }),
    });
    upsert(created);
    render();
    orderDialog.close();
    toast(`Pedido criado · código ${created.code}`);
  } catch (error) {
    $('#form-error').textContent = error.message;
    showFieldErrors(error.fields || {});
  } finally {
    submit.disabled = false;
    submit.removeAttribute('aria-busy');
    submit.innerHTML = label;
  }
});

$('#banner-retry').addEventListener('click', () => load());

// Live updates.
const LIVE_TEXT = {
  connecting: 'Conectando…',
  live: 'Atualização ao vivo',
  reconnecting: 'Reconectando…',
  offline: 'Sem conexão ao vivo',
};

function setLive(stateName) {
  const el = $('#live');
  if (el.dataset.state === stateName) return;
  el.dataset.state = stateName;
  $('#live-text').textContent = LIVE_TEXT[stateName];
}

let source = null;
let retryDelay = 2000;
let everConnected = false;

function connect() {
  if (source) source.close();
  setLive(everConnected ? 'reconnecting' : 'connecting');
  source = new EventSource('/api/events');
  source.addEventListener('connected', () => {
    setLive('live');
    retryDelay = 2000;
    // First connection: load after subscribing so no change is missed.
    // Reconnects resume from Last-Event-ID; the server sends "resync" if needed.
    if (!everConnected) load();
    everConnected = true;
  });
  source.addEventListener('resync', () => load());
  source.addEventListener('order', (event) => {
    try {
      if (upsert(JSON.parse(event.data))) render();
    } catch {
      load();
    }
  });
  source.addEventListener('error', () => {
    if (!state.loaded) load(); // show data even if the stream cannot connect
    if (source.readyState === EventSource.CLOSED) {
      // The browser gave up (e.g. HTTP error); retry with backoff.
      setLive('offline');
      const delay = retryDelay;
      retryDelay = Math.min(retryDelay * 2, 30000);
      setTimeout(connect, delay);
    } else {
      setLive('reconnecting');
    }
  });
}

$('#date').textContent = new Intl.DateTimeFormat('pt-BR', { weekday: 'short', day: 'numeric', month: 'short' }).format(new Date());
$('#date').dateTime = new Date().toISOString().slice(0, 10);

connect();
// Refresh ETAs once a minute without hitting the server.
setInterval(() => {
  if (state.loaded && !document.hidden) render();
}, 60000);
