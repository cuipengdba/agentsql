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
  let variables = [];
  let rowTree = { logic: 'and', items: [] };
  let editingItemIndex = null;
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
  function freshTree() { return { logic: 'and', items: [] }; }
  function parsed(value) {
    try { return typeof value === 'string' ? JSON.parse(value) : value; }
    catch { return null; }
  }
  function fieldName(id) { return fields.find(field => String(field.id) === String(id))?.field_name || `#${id}`; }
  function variableName(id) { return variables.find(variable => String(variable.id) === String(id))?.name || `#${id}`; }
  function treeLeaves(tree) {
    return (tree?.items || []).reduce((count, item) => count + (item.type === 'tree' ? treeLeaves(item.sub_tree) : 1), 0);
  }
  function describeTree(tree) {
    if (!tree || !Array.isArray(tree.items)) return t('permissionInvalid');
    const join = ` ${String(tree.logic).toUpperCase()} `;
    return '(' + tree.items.map(item => {
      if (item.type === 'tree') return describeTree(item.sub_tree);
      const value = item.value_type === 'variable' ? `${t('permissionVariable')} ${variableName(item.variable_id)}` : item.value;
      return `${fieldName(item.field_id)} ${item.term} ${['null', 'not_null'].includes(item.term) ? '' : value}`.trim();
    }).join(join) + ')';
  }
  function describeItem(item) {
    const source = sources.find(entry => String(entry.id) === String(item.ds_id))?.name || `#${item.ds_id}`;
    const table = tables.find(entry => String(entry.id) === String(item.table_id))?.table_name || item.table_name || `#${item.table_id}`;
    let detail = '';
    if (item.type === 'row') detail = describeTree(parsed(item.expression_tree));
    else {
      const permissions = parsed(item.permissions);
      const hidden = (Array.isArray(permissions) ? permissions : []).filter(entry => entry.enable === false).map(entry => fieldName(entry.field_id));
      detail = `${t('permissionHidden')}: ${hidden.join(', ') || t('permissionNone')}`;
    }
    return `${t(item.type === 'row' ? 'permissionRow' : 'permissionColumn')} · ${source} / ${table} · ${detail}`.slice(0, 500);
  }

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
      row.append(node('span', describeItem(item)));
      const edit = node('button', t('edit')); edit.type = 'button';
      edit.onclick = () => void editItem(index);
      const remove = node('button', t('remove')); remove.type = 'button';
      remove.onclick = () => { draft.permissions.splice(index, 1); renderItems(); };
      row.append(edit, remove); target.append(row);
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
  function builderAction() {
    $('permission-add-item').textContent = t(editingItemIndex === null ? 'permissionAddItem' : 'permissionReplaceItem');
  }
  function readCondition() {
    const field = $('permission-field')?.value, term = $('permission-term')?.value;
    const valueType = $('permission-value-type')?.value || 'normal';
    const value = $('permission-value')?.value || '';
    if (!fields.some(entry => String(entry.id) === field) || !term ||
        (valueType === 'variable' && (['null', 'not_null'].includes(term) || !$('permission-variable')?.value)) ||
        (valueType === 'normal' && (['in', 'not in'].includes(term) || !['null', 'not_null'].includes(term) && !value))) {
      return null;
    }
    const item = { type: 'item', field_id: field, filter_type: 'logic', term,
      value: valueType === 'normal' ? value : '', value_type: valueType };
    if (valueType === 'variable') item.variable_id = $('permission-variable').value;
    return item;
  }
  function validTree(tree, depth = 0) {
    if (!tree || depth > 8 || !['and', 'or'].includes(tree.logic) ||
        !Array.isArray(tree.items) || !tree.items.length || tree.items.length > 16) return false;
    return tree.items.every(item => item.type === 'tree' ? validTree(item.sub_tree, depth + 1) :
      item.type === 'item' && item.filter_type === 'logic' &&
      fields.some(field => String(field.id) === String(item.field_id)));
  }
  function renderRowTree() {
    const target = $('permission-expression'); target.replaceChildren();
    target.hidden = $('permission-kind').value !== 'row' || !fields.length;
    if (target.hidden) return;
    target.append(node('p', t('permissionTreeHint')));
    function draw(tree, depth, parent, position) {
      const group = node('div', undefined, 'permission-tree-group');
      const heading = node('div', undefined, 'permission-tree-heading');
      const logic = node('select'); logic.setAttribute('aria-label', t('permissionGroupLogic'));
      logic.append(option('and', t('permissionAnd')), option('or', t('permissionOr')));
      logic.value = tree.logic;
      logic.onchange = () => { tree.logic = logic.value; renderRowTree(); };
      heading.append(label(t('permissionGroupLogic'), logic));
      const addCondition = node('button', t('permissionAddCondition')); addCondition.type = 'button';
      addCondition.onclick = () => {
        const item = readCondition();
        if (!item || treeLeaves(rowTree) >= 16 || tree.items.length >= 16) {
          status(t('permissionInvalid'), true); return;
        }
        tree.items.push(item); renderRowTree(); status('');
      };
      const addGroup = node('button', t('permissionAddGroup')); addGroup.type = 'button';
      addGroup.disabled = depth >= 8 || tree.items.length >= 16 || treeLeaves(rowTree) >= 16;
      addGroup.onclick = () => { tree.items.push({ type: 'tree', sub_tree: freshTree() }); renderRowTree(); };
      heading.append(addCondition, addGroup);
      if (parent) {
        const remove = node('button', t('remove')); remove.type = 'button';
        remove.onclick = () => { parent.items.splice(position, 1); renderRowTree(); };
        heading.append(remove);
      }
      group.append(heading);
      for (const [index, item] of tree.items.entries()) {
        if (item.type === 'tree') { group.append(draw(item.sub_tree, depth + 1, tree, index)); continue; }
        const line = node('div', undefined, 'permission-tree-condition');
        line.append(node('span', describeTree({ logic: 'and', items: [item] })));
        const reuse = node('button', t('edit')); reuse.type = 'button';
        reuse.onclick = () => {
          $('permission-field').value = String(item.field_id);
          $('permission-term').value = item.term;
          $('permission-value-type').value = item.value_type || 'normal';
          $('permission-value').value = item.value || '';
          if (item.variable_id && $('permission-variable')) $('permission-variable').value = String(item.variable_id);
          $('permission-term').dispatchEvent(new Event('change'));
          tree.items.splice(index, 1); renderRowTree();
        };
        const remove = node('button', t('remove')); remove.type = 'button';
        remove.onclick = () => { tree.items.splice(index, 1); renderRowTree(); };
        line.append(reuse, remove); group.append(line);
      }
      return group;
    }
    target.append(draw(rowTree, 0, null, 0));
  }
  async function editItem(index) {
    if (!draft || !draft.permissions[index]) return;
    const rule = draft, item = rule.permissions[index];
    if (!sources.some(source => String(source.id) === String(item.ds_id))) {
      status(t('permissionUnavailableSource'), true); return;
    }
    status(t('reading'));
    $('permission-source').value = String(item.ds_id);
    if (!await loadTables() || !open || draft !== rule) return;
    if (!tables.some(table => String(table.id) === String(item.table_id))) {
      status(t('permissionUnavailableTable'), true); return;
    }
    $('permission-table').value = String(item.table_id);
    if (!await loadFields() || !open || draft !== rule) return;
    $('permission-kind').value = item.type;
    editingItemIndex = index;
    if (item.type === 'row') {
      const tree = parsed(item.expression_tree);
      if (!tree || !validTree(tree)) { status(t('permissionInvalid'), true); editingItemIndex = null; return; }
      rowTree = structuredClone(tree); renderFieldEditor();
    } else {
      renderFieldEditor();
      const permissions = parsed(item.permissions);
      const selected = new Map((Array.isArray(permissions) ? permissions : []).map(entry => [String(entry.field_id), entry.enable]));
      for (const control of $('permission-fields').querySelectorAll('input[type="checkbox"]')) {
        if (selected.has(control.value)) control.checked = selected.get(control.value);
      }
    }
    builderAction(); renderItems(); status('');
  }
  function newRule() {
    epoch++;
    draft = { name: '', description: '', enable: false, permissions: [], users: [] };
    rowTree = freshTree(); editingItemIndex = null; builderAction();
    $('permission-kind').value = 'row';
    $('permission-source').value = '';
    $('permission-table').replaceChildren(option('', t('permissionChooseTable')));
    $('permission-fields').replaceChildren(); $('permission-expression').replaceChildren(); tables = []; fields = [];
    selectedUsers = new Map(); renderDraft(); status('');
    $('permission-name').focus();
  }
  async function editRule(id) {
    const current = ++epoch;
    draft = null; editingItemIndex = null; rowTree = freshTree(); builderAction();
    renderDraft(); status(t('reading'));
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
    rules = []; sources = []; tables = []; fields = []; variables = []; draft = null;
    rowTree = freshTree(); editingItemIndex = null; selectedUsers.clear(); builderAction();
    $('permission-form').reset();
    $('permission-list').replaceChildren(); $('permission-user-results').replaceChildren();
    $('permission-table').replaceChildren(option('', t('permissionChooseTable')));
    $('permission-fields').replaceChildren(); $('permission-expression').replaceChildren();
    renderDraft(); status(t('reading'));
    try {
      const [items, available, definitions] = await Promise.all([
        api('/ds_permission/list', {}, 'POST'), api('/datasource/list'),
        api('/sys_variable/listAll', {}).catch(() => [])]);
      if (!open || current !== epoch) return;
      rules = Array.isArray(items) ? items : [];
      sources = (Array.isArray(available) ? available : []).filter(item => ['pg', 'mysql'].includes(item.type));
      variables = Array.isArray(definitions) ? definitions : [];
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
    $('permission-fields').replaceChildren(); $('permission-expression').replaceChildren();
    if (!id) return false;
    try {
      const result = await api('/datasource/tableList/' + encodeURIComponent(id), {}, 'POST');
      if (!open || current !== epoch) return false;
      tables = result.filter(item => item.checked);
      for (const table of tables) $('permission-table').append(option(table.id, table.table_name));
      return true;
    } catch { if (open && current === epoch) status(t('failed'), true); return false; }
  }
  async function loadFields() {
    const current = ++epoch, datasource = $('permission-source').value, table = $('permission-table').value;
    fields = []; $('permission-fields').replaceChildren();
    if (!datasource || !table) return false;
    try {
      const result = await api('/datasource/fieldList/' + encodeURIComponent(datasource) + '/' + encodeURIComponent(table), { fieldName: '' }, 'POST');
      if (!open || current !== epoch) return false;
      fields = result.filter(item => item.checked);
      renderFieldEditor(); renderItems(); return true;
    } catch { if (open && current === epoch) status(t('failed'), true); return false; }
  }
  function renderFieldEditor(preserve = false) {
    const target = $('permission-fields');
    const prior = preserve ? { field: $('permission-field')?.value, term: $('permission-term')?.value,
      value: $('permission-value')?.value, valueType: $('permission-value-type')?.value,
      variable: $('permission-variable')?.value, hidden: new Set([...target.querySelectorAll('input[type="checkbox"]')]
        .filter(control => !control.checked).map(control => control.value)) } : null;
    target.replaceChildren();
    if (!fields.length) { target.append(node('small', t('fieldEmpty'))); renderRowTree(); return; }
    if ($('permission-kind').value === 'row') {
      const select = node('select'); select.id = 'permission-field';
      for (const field of fields) select.append(option(field.id, field.field_name));
      if (prior?.field) select.value = prior.field;
      target.append(label(t('permissionField'), select));
      const term = node('select'); term.id = 'permission-term';
      for (const [value, text] of [['eq', '='], ['not_eq', '≠'], ['lt', '<'], ['le', '≤'], ['gt', '>'], ['ge', '≥'], ['in', t('permissionIn')], ['not in', t('permissionNotIn')], ['null', t('permissionIsNull')], ['not_null', t('permissionNotNull')]]) {
        term.append(option(value, text));
      }
      if (prior?.term) term.value = prior.term;
      target.append(label(t('permissionOperator'), term));
      const valueType = node('select'); valueType.id = 'permission-value-type';
      valueType.append(option('normal', t('permissionLiteral')), option('variable', t('permissionVariable')));
      valueType.value = prior?.valueType || 'normal';
      target.append(label(t('permissionValueType'), valueType));
      const value = node('input'); value.id = 'permission-value'; value.maxLength = 256;
      value.value = prior?.value || '';
      target.append(label(t('permissionValue'), value));
      const variable = node('select'); variable.id = 'permission-variable';
      variable.append(option('', t('permissionChooseVariable')));
      for (const definition of variables) variable.append(option(definition.id, definition.name));
      if (prior?.variable) variable.value = prior.variable;
      target.append(label(t('permissionVariable'), variable));
      const syncValue = () => {
        const noValue = ['null', 'not_null'].includes(term.value);
        const variableOnly = ['in', 'not in'].includes(term.value);
        if (noValue) valueType.value = 'normal';
        if (variableOnly) valueType.value = 'variable';
        valueType.disabled = noValue || variableOnly;
        value.disabled = noValue || valueType.value === 'variable';
        value.parentElement.hidden = value.disabled;
        variable.parentElement.hidden = noValue || valueType.value !== 'variable';
      };
      term.onchange = syncValue; valueType.onchange = syncValue; syncValue();
    } else {
      target.append(node('p', t('permissionColumnHint')));
      for (const field of fields) {
        const checkbox = node('input'); checkbox.type = 'checkbox'; checkbox.checked = !prior?.hidden.has(String(field.id));
        checkbox.value = String(field.id);
        target.append(label(field.field_name, checkbox));
      }
    }
    renderRowTree();
  }
  function addItem() {
    if (!draft || editingItemIndex === null && draft.permissions.length >= 16) {
      status(t('permissionInvalid'), true); return;
    }
    const datasource = $('permission-source').value, table = $('permission-table').value;
    if (!datasource || !table || !tables.some(item => String(item.id) === table) || !fields.length) {
      status(t('permissionInvalid'), true); return;
    }
    const item = { type: $('permission-kind').value, ds_id: datasource, table_id: table };
    if (item.type === 'row') {
      if (!rowTree.items.length) {
        const condition = readCondition();
        if (!condition) { status(t('permissionInvalid'), true); return; }
        rowTree.items.push(condition);
      }
      if (!validTree(rowTree) || treeLeaves(rowTree) > 16) { status(t('permissionInvalid'), true); return; }
      item.expression_tree = JSON.stringify(rowTree);
      if (item.expression_tree.length > 8192) { status(t('permissionInvalid'), true); return; }
    } else {
      const controls = [...$('permission-fields').querySelectorAll('input[type="checkbox"]')];
      if (controls.length !== fields.length || controls.every(control => control.checked)) {
        status(t('permissionChooseHidden'), true); return;
      }
      item.permissions = JSON.stringify(controls.map(control => ({ field_id: control.value, enable: control.checked })));
    }
    if (editingItemIndex !== null) {
      if (draft.permissions[editingItemIndex]?.id) item.id = draft.permissions[editingItemIndex].id;
      draft.permissions[editingItemIndex] = item;
    } else draft.permissions.push(item);
    editingItemIndex = null; rowTree = freshTree(); builderAction();
    renderItems(); renderRowTree(); status('');
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
    variables = []; rowTree = freshTree(); editingItemIndex = null; selectedUsers.clear();
    $('permissions').hidden = true; $('permissions-nav').classList.remove('active');
    $('permission-list').replaceChildren(); $('permission-user-results').replaceChildren();
    $('permission-items').replaceChildren(); $('permission-fields').replaceChildren();
    $('permission-expression').replaceChildren();
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
  $('permission-source').onchange = () => {
    rowTree = freshTree(); editingItemIndex = null; builderAction(); void loadTables();
  };
  $('permission-table').onchange = () => {
    rowTree = freshTree(); editingItemIndex = null; builderAction(); void loadFields();
  };
  $('permission-kind').onchange = () => {
    rowTree = freshTree(); editingItemIndex = null; builderAction(); renderFieldEditor();
  };
  $('permission-user-search-form').onsubmit = event => { event.preventDefault(); void searchUsers(); };
  return { hide, show, visible: () => open, language: () => {
    if (open) { renderList(); renderUsers(); renderItems(); renderFieldEditor(true); builderAction(); }
  } };
}
