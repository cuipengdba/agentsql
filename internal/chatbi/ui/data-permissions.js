// This editor sends the same rule-group shape as SQLBot's permission page.
// The server revalidates every ID and expression against live gateway state.
export function setupDataPermissions({ api, t, node, get, canOpen }) {
  let open = false;
  let epoch = 0;
  let rules = [];
  let sources = [];
  let draft = null;
  let tables = [];
  let fields = [];
  let selectedUsers = new Map();
  const $ = get;

  function status(message, error = false) {
    $('permission-status').textContent = message;
    $('permission-status').classList.toggle('error', error);
  }
  function label(text, control) {
    const wrapper = node('label');
    wrapper.append(node('span', text), control);
    return wrapper;
  }
  function option(value, text) {
    const item = node('option', text);
    item.value = String(value);
    return item;
  }
  function currentWorkspace() { return $('account-workspace').value; }

  function renderList() {
    const target = $('permission-list');
    target.replaceChildren();
    for (const rule of rules) {
      const card = node('article', undefined, 'user-item permission-card');
      const details = node('div');
      details.append(node('strong', rule.name), node('small',
        `${t(rule.enable ? 'enabled' : 'disabled')} · ${rule.permissions?.length || 0} ${t('permissionItems')} · ${rule.users?.length || 0} ${t('permissionUsers')}`));
      if (rule.description) details.append(node('small', rule.description));
      const edit = node('button', t('edit'));
      edit.type = 'button'; edit.onclick = () => void editRule(rule.id);
      const remove = node('button', t('remove'));
      remove.type = 'button'; remove.onclick = () => void deleteRule(rule.id);
      card.append(details, edit, remove); target.append(card);
    }
    if (!rules.length) target.append(node('p', t('permissionEmpty'), 'semantic-empty'));
  }
  function renderUsers() {
    const target = $('permission-selected-users'); target.replaceChildren();
    for (const [id, name] of selectedUsers) {
      const chip = node('span', name || `#${id}`, 'permission-user-chip');
      const remove = node('button', '×'); remove.type = 'button';
      remove.setAttribute('aria-label', `${t('remove')} ${name || id}`);
      remove.onclick = () => { selectedUsers.delete(id); renderUsers(); };
      chip.append(remove); target.append(chip);
    }
    if (!selectedUsers.size) target.append(node('small', t('permissionNoUsers')));
  }
  function renderItems() {
    const target = $('permission-items'); target.replaceChildren();
    for (const [index, item] of (draft?.permissions || []).entries()) {
      const row = node('div', undefined, 'permission-item');
      row.append(node('span', `${t(item.type === 'row' ? 'permissionRow' : 'permissionColumn')} · #${item.ds_id} / #${item.table_id}`));
      const remove = node('button', t('remove')); remove.type = 'button';
      remove.onclick = () => { draft.permissions.splice(index, 1); renderItems(); };
      row.append(remove); target.append(row);
    }
    if (!draft?.permissions?.length) target.append(node('small', t('permissionNoItems')));
  }
  function renderDraft() {
    $('permission-editor').hidden = !draft;
    if (!draft) return;
    $('permission-name').value = draft.name || '';
    $('permission-description').value = draft.description || '';
    $('permission-enabled').checked = !!draft.enable;
    renderUsers(); renderItems();
  }
  function newRule() {
    epoch++;
    draft = { name: '', description: '', enable: false, permissions: [], users: [] };
    $('permission-source').value = '';
    $('permission-table').replaceChildren(option('', t('permissionChooseTable')));
    $('permission-fields').replaceChildren(); tables = []; fields = [];
    selectedUsers = new Map(); renderDraft(); status('');
    $('permission-name').focus();
  }
  async function editRule(id) {
    const current = ++epoch;
    draft = null; renderDraft(); status(t('reading'));
    try {
      const rule = await api('/ds_permission/get/' + encodeURIComponent(String(id)), {}, 'POST');
      if (!open || current !== epoch) return;
      draft = { id: rule.id, name: rule.name, description: rule.description || '',
        enable: !!rule.enable, permissions: rule.permissions || [], users: rule.users || [] };
      selectedUsers = new Map(draft.users.map(value => [String(value), `#${value}`]));
      renderDraft(); status('');
    } catch { if (open && current === epoch) status(t('failed'), true); }
  }
  async function deleteRule(id) {
    if (!window.confirm(t('permissionDeleteConfirm'))) return;
    status(t('reading'));
    try {
      await api('/ds_permission/delete/' + encodeURIComponent(String(id)), {}, 'POST');
      if (!open) return;
      draft = null; renderDraft(); if (await load()) status(t('permissionDeleted'));
    } catch { if (open) status(t('failed'), true); }
  }
  async function load() {
    const current = ++epoch;
    $('permission-new').disabled = true;
    rules = []; sources = []; tables = []; fields = []; draft = null; selectedUsers.clear();
    $('permission-form').reset();
    $('permission-list').replaceChildren(); $('permission-user-results').replaceChildren();
    $('permission-table').replaceChildren(option('', t('permissionChooseTable')));
    $('permission-fields').replaceChildren();
    renderDraft(); status(t('reading'));
    try {
      const [items, available] = await Promise.all([
        api('/ds_permission/list', {}, 'POST'), api('/datasource/list')]);
      if (!open || current !== epoch) return;
      rules = Array.isArray(items) ? items : [];
      sources = (Array.isArray(available) ? available : []).filter(item => ['pg', 'mysql'].includes(item.type));
      const picker = $('permission-source'); picker.replaceChildren(option('', t('permissionChooseSource')));
      for (const source of sources) picker.append(option(source.id, source.name));
      renderList(); $('permission-new').disabled = false; status(''); return true;
    } catch { if (open && current === epoch) { $('permission-new').disabled = false; status(t('failed'), true); } return false; }
  }
  async function searchUsers() {
    if (!open || !draft) return;
    const current = epoch;
    const query = new URLSearchParams({ keyword: $('permission-user-search').value.trim(), oidlist: currentWorkspace() });
    const target = $('permission-user-results'); target.replaceChildren();
    try {
      const page = await api('/user/pager/1/100?' + query);
      if (!open || current !== epoch || !draft) return;
      for (const user of page.items || []) {
        if (user.status !== 1) continue;
        const id = String(user.id), display = `${user.name || user.account} (${user.account})`;
        const add = node('button', display); add.type = 'button';
        add.onclick = () => { selectedUsers.set(id, display); renderUsers(); };
        target.append(add);
      }
      if (!(page.items || []).length) target.append(node('small', t('permissionNoMatches')));
    } catch { if (open && current === epoch) status(t('failed'), true); }
  }
  async function loadTables() {
    const current = ++epoch, id = $('permission-source').value;
    tables = []; fields = []; $('permission-table').replaceChildren(option('', t('permissionChooseTable')));
    $('permission-fields').replaceChildren();
    if (!id) return;
    try {
      const result = await api('/datasource/tableList/' + encodeURIComponent(id), {}, 'POST');
      if (!open || current !== epoch) return;
      tables = result.filter(item => item.checked);
      for (const table of tables) $('permission-table').append(option(table.id, table.table_name));
    } catch { if (open && current === epoch) status(t('failed'), true); }
  }
  async function loadFields() {
    const current = ++epoch, datasource = $('permission-source').value, table = $('permission-table').value;
    fields = []; $('permission-fields').replaceChildren();
    if (!datasource || !table) return;
    try {
      const result = await api('/datasource/fieldList/' + encodeURIComponent(datasource) + '/' + encodeURIComponent(table), { fieldName: '' }, 'POST');
      if (!open || current !== epoch) return;
      fields = result.filter(item => item.checked);
      renderFieldEditor();
    } catch { if (open && current === epoch) status(t('failed'), true); }
  }
  function renderFieldEditor(preserve = false) {
    const target = $('permission-fields');
    const prior = preserve ? { field: $('permission-field')?.value, term: $('permission-term')?.value,
      value: $('permission-value')?.value, hidden: new Set([...target.querySelectorAll('input[type="checkbox"]')]
        .filter(control => !control.checked).map(control => control.value)) } : null;
    target.replaceChildren();
    if (!fields.length) { target.append(node('small', t('fieldEmpty'))); return; }
    if ($('permission-kind').value === 'row') {
      const select = node('select'); select.id = 'permission-field';
      for (const field of fields) select.append(option(field.id, field.field_name));
      if (prior?.field) select.value = prior.field;
      target.append(label(t('permissionField'), select));
      const term = node('select'); term.id = 'permission-term';
      for (const [value, text] of [['eq', '='], ['not_eq', '≠'], ['lt', '<'], ['le', '≤'], ['gt', '>'], ['ge', '≥'], ['null', t('permissionIsNull')], ['not_null', t('permissionNotNull')]]) {
        term.append(option(value, text));
      }
      if (prior?.term) term.value = prior.term;
      target.append(label(t('permissionOperator'), term));
      const value = node('input'); value.id = 'permission-value'; value.maxLength = 256;
      value.value = prior?.value || '';
      target.append(label(t('permissionValue'), value));
      term.onchange = () => { value.disabled = term.value === 'null' || term.value === 'not_null'; };
      term.onchange();
    } else {
      target.append(node('p', t('permissionColumnHint')));
      for (const field of fields) {
        const checkbox = node('input'); checkbox.type = 'checkbox'; checkbox.checked = !prior?.hidden.has(String(field.id));
        checkbox.value = String(field.id);
        target.append(label(field.field_name, checkbox));
      }
    }
  }
  function addItem() {
    if (!draft || draft.permissions.length >= 16) { status(t('permissionInvalid'), true); return; }
    const datasource = $('permission-source').value, table = $('permission-table').value;
    if (!datasource || !table || !tables.some(item => String(item.id) === table) || !fields.length) {
      status(t('permissionInvalid'), true); return;
    }
    const item = { type: $('permission-kind').value, ds_id: datasource, table_id: table };
    if (item.type === 'row') {
      const field = $('permission-field')?.value, term = $('permission-term')?.value, value = $('permission-value')?.value || '';
      if (!fields.some(entry => String(entry.id) === field) || !term || (!['null', 'not_null'].includes(term) && !value)) {
        status(t('permissionInvalid'), true); return;
      }
      item.expression_tree = JSON.stringify({ logic: 'and', items: [{ type: 'item', field_id: field,
        filter_type: 'logic', term, value, value_type: 'normal' }] });
    } else {
      const controls = [...$('permission-fields').querySelectorAll('input[type="checkbox"]')];
      if (controls.length !== fields.length || controls.every(control => control.checked)) {
        status(t('permissionChooseHidden'), true); return;
      }
      item.permissions = JSON.stringify(controls.map(control => ({ field_id: control.value, enable: control.checked })));
    }
    draft.permissions.push(item); renderItems(); status('');
  }
  async function save(event) {
    event.preventDefault();
    if (!draft) return;
    const name = $('permission-name').value.trim();
    if (!name || name.length > 128 || draft.permissions.length > 16 ||
        ($('permission-enabled').checked && (!draft.permissions.length || !selectedUsers.size))) {
      status(t('permissionInvalid'), true); return;
    }
    const submit = $('permission-save'); submit.disabled = true;
    try {
      const body = { ...draft, name, description: $('permission-description').value,
        enable: $('permission-enabled').checked, users: [...selectedUsers.keys()] };
      await api('/ds_permission/save', body, 'POST');
      if (!open) return;
      if (await load()) status(t('permissionSaved'));
    } catch { if (open) status(t('failed'), true); }
    finally { submit.disabled = false; }
  }
  function hide() {
    open = false; epoch++; rules = []; sources = []; draft = null; tables = []; fields = [];
    selectedUsers.clear();
    $('permissions').hidden = true; $('permissions-nav').classList.remove('active');
    $('permission-list').replaceChildren(); $('permission-user-results').replaceChildren();
    $('permission-items').replaceChildren(); $('permission-fields').replaceChildren();
    $('permission-editor').hidden = true;
    $('permission-form').reset(); $('permission-user-search-form').reset();
  }
  async function show() {
    if (!canOpen()) return;
    open = true; $('permissions').hidden = false; $('permissions-nav').classList.add('active');
    await load();
  }
  $('permission-new').onclick = newRule;
  $('permission-form').onsubmit = event => void save(event);
  $('permission-cancel').onclick = () => { draft = null; renderDraft(); status(''); };
  $('permission-add-item').onclick = addItem;
  $('permission-source').onchange = () => void loadTables();
  $('permission-table').onchange = () => void loadFields();
  $('permission-kind').onchange = () => renderFieldEditor();
  $('permission-user-search-form').onsubmit = event => { event.preventDefault(); void searchUsers(); };
  return { hide, show, visible: () => open, language: () => { if (open) { renderList(); renderUsers(); renderItems(); renderFieldEditor(true); } } };
}
