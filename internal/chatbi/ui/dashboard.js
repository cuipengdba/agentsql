import { chartSeries, renderChart } from './chart.js';

export function setupDashboard({ api, t, node, get }) {
  let active = false, selected = null, items = [], records = [], results = {};
  const $ = get;
  const key = () => crypto.randomUUID().replaceAll('-', '');
  const status = (value, error = false) => {
    $('dashboard-status').textContent = value;
    $('dashboard-status').classList.toggle('error', error);
  };
  const hidden = () => {
    active = false;
    $('dashboards').hidden = true;
    $('dashboards-nav').classList.remove('active');
  };
  function renderRecordChoices() {
    const select = $('dashboard-record');
    select.replaceChildren();
    for (const record of records) {
      const option = node('option', record.question || String(record.id));
      option.value = String(record.id);
      select.append(option);
    }
    $('dashboard-add-chart').disabled = records.length === 0;
  }
  function renderTable(data) {
    const wrap = node('div', undefined, 'table-wrap'), table = node('table');
    const head = node('thead'), heading = node('tr');
    for (const field of data.fields || []) heading.append(node('th', field));
    head.append(heading); table.append(head);
    const body = node('tbody');
    for (const item of data.data || []) {
      const row = node('tr');
      for (const field of data.fields || []) row.append(node('td', item[field] == null ? '—' : String(item[field])));
      body.append(row);
    }
    table.append(body); wrap.append(table);
    return wrap;
  }
  function renderCards() {
    const list = $('dashboard-cards');
    list.replaceChildren();
    if (!selected) return;
    for (const card of selected.cards || []) {
      const shell = node('article', undefined, 'dashboard-card');
      const top = node('div', undefined, 'dashboard-card-top');
      const title = node('input'); title.value = card.title || ''; title.maxLength = 120;
      title.setAttribute('aria-label', t('dashboardCardTitle'));
      title.oninput = () => { card.title = title.value; };
      const remove = node('button', t('remove')); remove.type = 'button';
      remove.onclick = () => { selected.cards = selected.cards.filter(item => item.id !== card.id); delete results[card.id]; renderCards(); };
      top.append(title, remove); shell.append(top);
      if (card.kind === 'text') {
        const text = node('textarea'); text.value = card.text || ''; text.maxLength = 4000;
        text.setAttribute('aria-label', t('dashboardText'));
        text.oninput = () => { card.text = text.value; };
        shell.append(text);
      } else {
        const data = results[card.id];
        if (!data) shell.append(node('p', t('dashboardPending'), 'dashboard-note'));
        else {
          const series = chartSeries(data);
          if (card.kind === 'table') shell.append(renderTable(data));
          else if (series) shell.append(renderChart(series, card.kind));
          else shell.append(node('p', t('dashboardNoChart'), 'dashboard-note'));
          shell.append(node('small', `${t('audit')} #${data.audit_id}`, 'dashboard-audit'));
        }
      }
      list.append(shell);
    }
  }
  function renderList() {
    const list = $('dashboard-list'); list.replaceChildren();
    for (const item of items) {
      const button = node('button', item.name, 'dashboard-list-item'); button.type = 'button';
      button.classList.toggle('active', selected?.id === item.id);
      button.onclick = () => { void load(item.id); };
      list.append(button);
    }
    if (!items.length) list.append(node('p', t('dashboardEmpty'), 'dashboard-note'));
  }
  async function refreshList() {
    items = await api('/agentsql/dashboards');
    if (active) renderList();
  }
  async function load(id) {
    status(t('reading'));
    selected = null; results = {}; $('dashboard-editor').hidden = true;
    try {
      const view = await api('/agentsql/dashboards/' + encodeURIComponent(id));
      if (!active) return;
      selected = { id: view.id, name: view.name, cards: view.cards || [], revision: view.revision };
      results = view.results || {};
      $('dashboard-name').value = selected.name;
      $('dashboard-editor').hidden = false;
      renderList(); renderCards(); status('');
    } catch { if (active) status(t('failed'), true); }
  }
  async function show() {
    if (active) return;
    active = true; $('dashboards').hidden = false; $('dashboards-nav').classList.add('active');
    renderRecordChoices(); status(t('reading'));
    try { await refreshList(); status(''); }
    catch { if (active) status(t('failed'), true); }
  }
  $('dashboard-new').onclick = () => {
    selected = { name: '', cards: [], revision: 0 }; results = {};
    $('dashboard-name').value = ''; $('dashboard-editor').hidden = false;
    renderList(); renderCards(); status(''); $('dashboard-name').focus();
  };
  $('dashboard-add-text').onclick = () => {
    if (!selected || selected.cards.length >= 32) return;
    selected.cards.push({ id: key(), kind: 'text', title: t('dashboardText'), text: '' });
    renderCards();
  };
  $('dashboard-add-chart').onclick = () => {
    if (!selected || selected.cards.length >= 32 || !$('dashboard-record').value) return;
    selected.cards.push({ id: key(), kind: $('dashboard-kind').value, title: t('dashboardChart'), record_id: $('dashboard-record').value });
    renderCards();
  };
  $('dashboard-form').onsubmit = async event => {
    event.preventDefault(); if (!selected) return;
    const item = { ...selected, name: $('dashboard-name').value.trim() };
    if (!item.name) return;
    status(t('dashboardSaving'));
    try {
      const saved = await api('/agentsql/dashboards' + (item.id ? '/' + item.id : ''), item, item.id ? 'PUT' : 'POST');
      await refreshList(); await load(saved.id); status(t('dashboardSaved'));
    } catch { status(t('failed'), true); }
  };
  $('dashboard-delete').onclick = async () => {
    if (!selected?.id || !confirm(t('dashboardConfirmDelete'))) return;
    try {
      await api('/agentsql/dashboards/' + selected.id + '?revision=' + selected.revision, undefined, 'DELETE');
      selected = null; results = {}; $('dashboard-editor').hidden = true;
      await refreshList(); status(t('dashboardDeleted'));
    } catch { status(t('failed'), true); }
  };
  return {
    show, hide: hidden, visible: () => active,
    setRecords(chat) {
      records = (chat?.records || []).filter(record => record.finish && !record.error && record.sql);
      if (active) renderRecordChoices();
    },
    language() { if (active) { renderList(); renderCards(); renderRecordChoices(); } }
  };
}
