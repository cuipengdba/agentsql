// A compact editor for the same gateway-bound relation graph consumed by the
// original SQLBot canvas. IDs and permissions are revalidated by the server.
export async function renderSourceRelations({ source, tables, api, t, node, valid }) {
  const section = node('section', undefined, 'source-relations');
  section.id = 'source-relations';
  const title = node('strong', t('sourceRelations'));
  const intro = node('p', t('sourceRelationsIntro'));
  const list = node('div', undefined, 'source-relation-list');
  const form = node('form', undefined, 'source-relation-form');
  const fromTable = node('select'), fromField = node('select');
  const toTable = node('select'), toField = node('select');
  const add = node('button', t('sourceRelationAdd'));
  add.type = 'submit';
  for (const [label, select] of [
    ['sourceRelationFromTable', fromTable], ['sourceRelationFromField', fromField],
    ['sourceRelationToTable', toTable], ['sourceRelationToField', toField],
  ]) {
    const field = node('label', t(label));
    field.append(select);
    form.append(field);
  }
  form.append(add);
  section.append(title, intro, list, form);
  let graph = [];
  const tableByID = new Map(tables.map(table => [String(table.id), table]));
  const fields = new Map();
  for (const select of [fromTable, toTable]) {
    for (const table of tables) {
      const option = node('option', table.table_name);
      option.value = String(table.id);
      select.append(option);
    }
  }
  if (tables.length > 1) toTable.selectedIndex = 1;
  const id = encodeURIComponent(String(source.id));
  const getPath = '/table_relation/get/' + id;
  const savePath = '/table_relation/save/' + id;
  const fieldName = (tableID, fieldID) => {
    const table = graph.find(cell => cell.shape === 'er-rect' && String(cell.id) === tableID);
    const port = table?.ports?.find(item => String(item.id) === fieldID);
    return port?.attrs?.portNameLabel?.text || fieldID;
  };
  const endpoint = (value) => {
    const tableID = String(value?.cell || ''), fieldID = String(value?.port || '');
    return (tableByID.get(tableID)?.table_name || tableID) + '.' + fieldName(tableID, fieldID);
  };
  const reload = async () => {
    graph = await api(getPath, {}, 'POST');
    if (!valid()) return;
    list.replaceChildren();
    const edges = graph.filter(cell => cell.shape === 'edge');
    if (!edges.length) list.append(node('p', t('sourceRelationEmpty')));
    if (tables.length < 2) list.append(node('p', t('sourceRelationNeedTables')));
    for (const edge of edges) {
      const row = node('div', undefined, 'source-relation-row');
      row.append(node('span', endpoint(edge.source) + ' → ' + endpoint(edge.target)));
      const remove = node('button', t('sourceRelationRemove'));
      remove.type = 'button';
      remove.onclick = async () => {
        remove.disabled = true;
        try {
          await api(savePath, graph.filter(cell => cell !== edge), 'POST');
          if (valid()) await reload();
        } catch {
          remove.disabled = false;
          if (valid()) list.append(node('p', t('failed')));
        }
      };
      row.append(remove);
      list.append(row);
    }
  };
  const fillFields = async (tableSelect, fieldSelect) => {
    const tableID = tableSelect.value;
    fieldSelect.replaceChildren();
    if (!tableID) return;
    if (!fields.has(tableID)) {
      const rows = await api('/datasource/fieldList/' + encodeURIComponent(String(source.id)) + '/' +
        encodeURIComponent(tableID), { fieldName: '' }, 'POST');
      fields.set(tableID, rows);
    }
    if (!valid() || tableSelect.value !== tableID) return;
    for (const field of fields.get(tableID)) {
      const option = node('option', field.field_name);
      option.value = String(field.id);
      fieldSelect.append(option);
    }
  };
  fromTable.onchange = () => { void fillFields(fromTable, fromField); };
  toTable.onchange = () => { void fillFields(toTable, toField); };
  form.onsubmit = async event => {
    event.preventDefault();
    if (!valid() || !fromField.value || !toField.value || fromTable.value === toTable.value) return;
    add.disabled = true;
    try {
      const next = [...graph];
      for (const tableID of [fromTable.value, toTable.value]) {
        if (!next.some(cell => cell.shape === 'er-rect' && String(cell.id) === tableID)) {
          next.push({ id: tableID, shape: 'er-rect', position: { x: 40 + next.length * 220, y: 40 } });
        }
      }
      next.push({ shape: 'edge', source: { cell: fromTable.value, port: fromField.value },
        target: { cell: toTable.value, port: toField.value } });
      await api(savePath, next, 'POST');
      if (valid()) await reload();
    } catch {
      if (valid()) list.append(node('p', t('failed')));
    } finally { add.disabled = false; }
  };
  if (tables.length < 2) {
    form.hidden = true;
  }
  try {
    await reload();
    if (tables.length > 1) await Promise.all([fillFields(fromTable, fromField), fillFields(toTable, toField)]);
  } catch {
    if (valid()) list.replaceChildren(node('p', t('failed')));
  }
  return section;
}
