const PAGE_SIZE = 25;
const claimsState = { offset: 0, total: 0, type: '', status: '' };
const contradictionsState = { offset: 0, total: 0 };

async function fetchJSON(url) {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error(`${resp.status} ${resp.statusText}`);
  return resp.json();
}

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') node.className = v;
    else if (k === 'html') node.innerHTML = v;
    else node.setAttribute(k, v);
  }
  for (const c of children) {
    if (c == null) continue;
    node.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
  }
  return node;
}

async function loadHealth() {
  try {
    const h = await fetchJSON('/health');
    document.getElementById('meta').textContent = `mnemos ${h.version}`;
  } catch (e) {
    document.getElementById('meta').textContent = 'offline';
  }
}

async function loadMetrics() {
  const m = await fetchJSON('/v1/metrics');
  const cards = [
    { label: 'Events', value: m.events },
    { label: 'Claims', value: m.claims },
    { label: 'Contested', value: m.contested_claims, warn: m.contested_claims > 0 },
    { label: 'Relationships', value: m.relationships },
    { label: 'Contradictions', value: m.contradictions, warn: m.contradictions > 0 },
    { label: 'Embeddings', value: m.embeddings },
  ];
  const root = document.getElementById('metrics');
  root.innerHTML = '';
  for (const c of cards) {
    root.appendChild(el('div', { class: 'metric' + (c.warn ? ' warn' : '') },
      el('div', { class: 'label' }, c.label),
      el('div', { class: 'value' }, String(c.value))));
  }
}

async function loadClaims() {
  const params = new URLSearchParams({
    limit: String(PAGE_SIZE),
    offset: String(claimsState.offset),
  });
  if (claimsState.type) params.set('type', claimsState.type);
  if (claimsState.status) params.set('status', claimsState.status);

  const root = document.getElementById('claims');
  root.innerHTML = '';
  try {
    const data = await fetchJSON('/v1/beliefs?' + params);
    claimsState.total = data.total;
    if (!data.beliefs || data.beliefs.length === 0) {
      root.appendChild(el('div', { class: 'empty' }, 'No beliefs match these filters.'));
    } else {
      for (const c of data.beliefs) {
        root.appendChild(el('div', { class: 'row' },
          el('span', { class: 'pill' }, c.type),
          el('span', { class: 'pill' }, c.status),
          el('span', { class: 'pill' }, `conf ${(c.confidence * 100).toFixed(0)}%`),
          el('div', { class: 'text' }, c.text)));
      }
    }
    renderPagination('claims-pagination', claimsState, loadClaims);
  } catch (e) {
    root.appendChild(el('div', { class: 'error' }, `Error: ${e.message}`));
  }
}

async function loadContradictions() {
  const params = new URLSearchParams({
    limit: String(PAGE_SIZE),
    offset: String(contradictionsState.offset),
    type: 'contradicts',
  });
  const root = document.getElementById('contradictions');
  root.innerHTML = '';
  try {
    const data = await fetchJSON('/v1/associations?' + params);
    contradictionsState.total = data.total;
    if (!data.associations || data.associations.length === 0) {
      root.appendChild(el('div', { class: 'empty' }, 'No contradictions found.'));
    } else {
      for (const r of data.associations) {
        root.appendChild(el('div', { class: 'row contradicts' },
          el('div', { class: 'text' }, `${r.from_belief_id} contradicts ${r.to_belief_id}`),
          el('div', { class: 'from-to' }, new Date(r.created_at).toLocaleString())));
      }
    }
    renderPagination('contradictions-pagination', contradictionsState, loadContradictions);
  } catch (e) {
    root.appendChild(el('div', { class: 'error' }, `Error: ${e.message}`));
  }
}

function renderPagination(id, state, loader) {
  const root = document.getElementById(id);
  root.innerHTML = '';
  if (state.total <= PAGE_SIZE) return;
  const prev = el('button', { type: 'button' }, 'Prev');
  prev.disabled = state.offset === 0;
  prev.onclick = () => { state.offset = Math.max(0, state.offset - PAGE_SIZE); loader(); };
  const next = el('button', { type: 'button' }, 'Next');
  next.disabled = state.offset + PAGE_SIZE >= state.total;
  next.onclick = () => { state.offset += PAGE_SIZE; loader(); };
  const pos = `${state.offset + 1}–${Math.min(state.offset + PAGE_SIZE, state.total)} of ${state.total}`;
  root.appendChild(prev);
  root.appendChild(next);
  root.appendChild(el('span', {}, pos));
}

document.getElementById('claim-type').addEventListener('change', (e) => {
  claimsState.type = e.target.value;
  claimsState.offset = 0;
  loadClaims();
});
document.getElementById('claim-status').addEventListener('change', (e) => {
  claimsState.status = e.target.value;
  claimsState.offset = 0;
  loadClaims();
});

loadHealth();
loadMetrics();
loadClaims();
loadContradictions();
