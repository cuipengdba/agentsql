import { chartSeries, renderChart } from './chart.js';

export function setupDashboard({ api, t, node, get }) {
  let active = false, selected = null, items = [], records = [], results = {}, activeTab = '', preview = false;
  const $ = get;
  const key = () => crypto.randomUUID().replaceAll('-', '');
  const listSearch = node('input'); listSearch.type = 'search'; listSearch.maxLength = 120;
  const sidebar = node('div', undefined, 'dashboard-sidebar');
  $('dashboard-list').before(sidebar); sidebar.append(listSearch, $('dashboard-list'));
  const toolbar = node('div', undefined, 'dashboard-toolbar');
  const tabs = node('div', undefined, 'dashboard-tabs');
  const addTab = node('button'); addTab.type = 'button';
  const previewButton = node('button'); previewButton.type = 'button';
  const exportButton = node('button'); exportButton.type = 'button';
  const importButton = node('button'); importButton.type = 'button';
  const importFile = node('input'); importFile.type = 'file'; importFile.accept = '.json,application/json'; importFile.hidden = true;
  toolbar.append(tabs, addTab, previewButton, exportButton, importButton, importFile);
  $('dashboard-cards').before(toolbar);
  const labels = { 'zh-CN': { search:'搜索仪表板', all:'默认', addTab:'添加 Tab', tabName:'标签名称', removeTab:'删除标签', wide:'宽度', half:'半宽', full:'整行', up:'上移', down:'下移', preview:'预览', edit:'编辑', export:'导出配置', import:'导入配置', importError:'配置文件无效或引用的记录不属于当前用户。', importReady:'已加载配置；保存前请核对记录引用。', saveFirst:'请先保存仪表板。' }, en: { search:'Search dashboards', all:'Default', addTab:'Add tab', tabName:'Tab name', removeTab:'Remove tab', wide:'Width', half:'Half', full:'Full row', up:'Move up', down:'Move down', preview:'Preview', edit:'Edit', export:'Export config', import:'Import config', importError:'Invalid config or referenced records are not owned by this user.', importReady:'Config loaded. Review record references before saving.', saveFirst:'Save the dashboard first.' } };
  const label = name => labels[document.documentElement.lang === 'en' ? 'en' : 'zh-CN'][name];
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
    const visibleCards = (selected.cards || []).filter(card => (card.tab_id || '') === activeTab);
    for (const card of visibleCards) {
      const shell = node('article', undefined, 'dashboard-card');
      if (card.span === 2) shell.classList.add('dashboard-card-wide');
      const top = node('div', undefined, 'dashboard-card-top');
      const title = node('input'); title.value = card.title || ''; title.maxLength = 120;
      title.setAttribute('aria-label', t('dashboardCardTitle'));
      title.oninput = () => { card.title = title.value; };
      title.disabled = preview;
      const remove = node('button', t('remove')); remove.type = 'button';
      remove.onclick = () => { selected.cards = selected.cards.filter(item => item.id !== card.id); delete results[card.id]; renderCards(); };
      remove.hidden = preview;
      const up = node('button', label('up')); up.type = 'button'; up.hidden = preview;
      const down = node('button', label('down')); down.type = 'button'; down.hidden = preview;
      const move = delta => { const index = selected.cards.indexOf(card), other = index + delta; if (other < 0 || other >= selected.cards.length) return; [selected.cards[index], selected.cards[other]] = [selected.cards[other], selected.cards[index]]; renderCards(); };
      up.onclick = () => move(-1); down.onclick = () => move(1);
      top.append(title, up, down, remove); shell.append(top);
      if (!preview) {
        const controls = node('div', undefined, 'dashboard-card-controls');
        const span = node('select'); span.setAttribute('aria-label', label('wide'));
        for (const [value, text] of [['1', label('half')], ['2', label('full')]]) { const option = node('option', text); option.value = value; span.append(option); }
        span.value = String(card.span || 1); span.onchange = () => { card.span = Number(span.value); renderCards(); };
        const tab = node('select'); tab.setAttribute('aria-label', label('tabName'));
        for (const item of [{id:'', name:label('all')}, ...(selected.tabs || [])]) { const option = node('option', item.name); option.value = item.id; tab.append(option); }
        tab.value = card.tab_id || ''; tab.onchange = () => { card.tab_id = tab.value; renderCards(); };
        controls.append(span, tab); shell.append(controls);
      }
      if (card.kind === 'text') {
        const text = node('textarea'); text.value = card.text || ''; text.maxLength = 4000;
        text.setAttribute('aria-label', t('dashboardText'));
        text.oninput = () => { card.text = text.value; };
        text.disabled = preview;
        shell.append(text);
      } else {
        const data = results[card.id];
        if (!data) shell.append(node('p', t('dashboardPending'), 'dashboard-note'));
        else {
          const series = chartSeries(data);
          if (card.kind === 'table') shell.append(renderTable(data));
          else if (series && (card.kind !== 'pie' || series.items.some(item => item.value > 0))) shell.append(renderChart(series, card.kind));
          else shell.append(node('p', t('dashboardNoChart'), 'dashboard-note'));
          shell.append(node('small', `${t('audit')} #${data.audit_id}`, 'dashboard-audit'));
        }
      }
      list.append(shell);
    }
  }
  function renderList() {
    const list = $('dashboard-list'); list.replaceChildren();
    const query = listSearch.value.trim().toLocaleLowerCase();
    for (const item of items.filter(value => value.name.toLocaleLowerCase().includes(query))) {
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
    selected = null; results = {}; preview = false; $('dashboard-editor').hidden = true;
    try {
      const view = await api('/agentsql/dashboards/' + encodeURIComponent(id));
      if (!active) return;
      selected = { id: view.id, name: view.name, tabs: view.tabs || [], cards: view.cards || [], revision: view.revision };
      if (activeTab && !selected.tabs.some(tab => tab.id === activeTab)) activeTab = '';
      if (!activeTab && !selected.cards.some(card => !card.tab_id) && selected.tabs.length) activeTab = selected.tabs[0].id;
      results = view.results || {};
      $('dashboard-name').value = selected.name;
      $('dashboard-editor').hidden = false;
      renderList(); renderTabs(); renderCards(); status('');
    } catch { if (active) status(t('failed'), true); }
  }
  async function show() {
    if (active) return;
    active = true; $('dashboards').hidden = false; $('dashboards-nav').classList.add('active');
    renderRecordChoices(); listSearch.placeholder = label('search'); addTab.textContent = label('addTab'); exportButton.textContent = label('export'); importButton.textContent = label('import'); status(t('reading'));
    try { await refreshList(); status(''); }
    catch { if (active) status(t('failed'), true); }
  }
  $('dashboard-new').onclick = () => {
    selected = { name: '', tabs: [], cards: [], revision: 0 }; results = {}; activeTab = ''; preview = false;
    $('dashboard-name').value = ''; $('dashboard-editor').hidden = false;
    renderList(); renderTabs(); renderCards(); status(''); $('dashboard-name').focus();
  };
  $('dashboard-add-text').onclick = () => {
    if (!selected || selected.cards.length >= 32) return;
    selected.cards.push({ id: key(), kind: 'text', title: t('dashboardText'), text: '', tab_id: activeTab, span: 1 });
    renderCards();
  };
  $('dashboard-add-chart').onclick = () => {
    if (!selected || selected.cards.length >= 32 || !$('dashboard-record').value) return;
    selected.cards.push({ id: key(), kind: $('dashboard-kind').value, title: t('dashboardChart'), record_id: $('dashboard-record').value, tab_id: activeTab, span: 1 });
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
  function renderTabs() {
    tabs.replaceChildren();
    if (!selected) return;
    for (const item of [{id:'', name:label('all')}, ...(selected.tabs || [])]) {
      const row = node('div', undefined, 'dashboard-tab');
      const button = node('button', item.name); button.type = 'button'; button.classList.toggle('active', activeTab === item.id);
      button.onclick = () => { activeTab = item.id; renderTabs(); renderCards(); };
      row.append(button);
      if (item.id && !preview) {
        const name = node('input'); name.value = item.name; name.maxLength = 80; name.setAttribute('aria-label', label('tabName'));
        name.oninput = () => { item.name = name.value; button.textContent = item.name; };
        const remove = node('button', '×'); remove.type = 'button'; remove.title = label('removeTab');
        remove.onclick = () => { selected.cards.forEach(card => { if (card.tab_id === item.id) card.tab_id = ''; }); selected.tabs = selected.tabs.filter(tab => tab.id !== item.id); activeTab = ''; renderTabs(); renderCards(); };
        row.append(name, remove);
      }
      tabs.append(row);
    }
    addTab.hidden = preview || selected.tabs.length >= 8;
    $('dashboard-add-text').hidden = preview;
    $('dashboard-add-chart').hidden = preview;
    $('dashboard-record').hidden = preview;
    $('dashboard-kind').hidden = preview;
    $('dashboard-name').disabled = preview;
    $('dashboard-form').querySelector('[type="submit"]').hidden = preview;
    previewButton.textContent = label(preview ? 'edit' : 'preview');
  }
  addTab.onclick = () => {
    if (!selected || selected.tabs.length >= 8) return;
    const tab = { id:key(), name:`${label('tabName')} ${selected.tabs.length + 1}` };
    selected.tabs.push(tab); activeTab = tab.id; renderTabs(); renderCards();
  };
  previewButton.onclick = async () => {
    if (!selected?.id) { status(label('saveFirst'), true); return; }
    if (!preview) { await load(selected.id); if (!selected) return; }
    preview = !preview; renderTabs(); renderCards();
  };
  exportButton.onclick = () => {
    if (!selected) return;
    const config = {format:'agentsql-dashboard-v1', name:selected.name, tabs:selected.tabs, cards:selected.cards};
    const url = URL.createObjectURL(new Blob([JSON.stringify(config, null, 2)], {type:'application/json'}));
    const link = node('a'); link.href = url; link.download = 'agentsql-dashboard.json'; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  importButton.onclick = () => { importFile.value = ''; importFile.click(); };
  importFile.onchange = async () => {
    const file = importFile.files?.[0]; if (!file || file.size > 65536) { status(label('importError'), true); return; }
    try {
      const config = JSON.parse(await file.text());
      if (config.format !== 'agentsql-dashboard-v1' || typeof config.name !== 'string' || !Array.isArray(config.tabs) || !Array.isArray(config.cards) || config.tabs.length > 8 || config.cards.length > 32 || config.tabs.some(tab => !tab || typeof tab.id !== 'string' || typeof tab.name !== 'string') || config.cards.some(card => !card || typeof card.id !== 'string' || typeof card.kind !== 'string')) throw Error('invalid');
      selected = {name:config.name, tabs:config.tabs, cards:config.cards, revision:0}; results = {}; activeTab = ''; preview = false;
      $('dashboard-name').value = config.name; $('dashboard-editor').hidden = false;
      renderList(); renderTabs(); renderCards(); status(label('importReady'));
    } catch { status(label('importError'), true); }
  };
  listSearch.oninput = renderList;
  return {
    show, hide: hidden, visible: () => active,
    setRecords(chat) {
      records = (chat?.records || []).filter(record => record.finish && !record.error && !record.action && record.sql);
      if (active) renderRecordChoices();
    },
    language() { if (active) { listSearch.placeholder = label('search'); addTab.textContent = label('addTab'); exportButton.textContent = label('export'); importButton.textContent = label('import'); renderList(); renderTabs(); renderCards(); renderRecordChoices(); } }
  };
}
