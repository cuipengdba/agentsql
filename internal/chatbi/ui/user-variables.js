// The server owns the definitions and validates every assignment again on save.
// This editor only prepares the original SQLBot variableValues request shape.
export function setupUserVariableEditor({ container, definitions, initial = [], t, node }) {
  const available = (definitions || []).filter(item =>
    item && Number.isSafeInteger(item.id) && item.id > 0 && Array.isArray(item.value) &&
    ['text', 'kv', 'number', 'datetime'].includes(item.var_type));
  const byID = new Map(available.map(item => [String(item.id), item]));
  const rows = initial.map(item => ({ id: String(item.variableId), values: [...(item.variableValues || [])] }));

  function render() {
    container.replaceChildren();
    const heading = node('h3', t('usersVariablesTitle'));
    const note = node('p', t('usersVariablesNote'), 'user-variable-note');
    container.append(heading, note);
    if (!available.length && !rows.length) {
      container.append(node('p', t('usersVariablesEmpty'), 'user-variable-note'));
      return;
    }
    for (const row of rows) {
      const wrapper = node('div', undefined, 'user-variable-row');
      const nameLabel = node('label', t('usersVariable'));
      const name = node('select');
      name.required = true;
      const placeholder = node('option', t('usersVariableChoose')); placeholder.value = '';
      name.append(placeholder);
      if (row.id && !byID.has(row.id)) {
        const missing = node('option', `${t('usersVariableMissing')} #${row.id}`);
        missing.value = row.id; missing.disabled = true; name.append(missing);
      }
      for (const definition of available) {
        const option = node('option', definition.name);
        option.value = String(definition.id);
        name.append(option);
      }
      name.value = row.id;
      name.onchange = () => {
        row.id = name.value;
        row.values = [];
        const definition = byID.get(row.id);
        if (definition && ['text', 'kv'].includes(definition.var_type) && definition.value.length === 1) {
          row.values = [definition.var_type === 'kv' ? definition.value[0].value : definition.value[0]];
        }
        render();
      };
      nameLabel.append(name);
      const valueLabel = node('label', t('usersVariableValue'));
      const definition = byID.get(row.id);
      if (definition && ['text', 'kv'].includes(definition.var_type)) {
        const values = node('select'); values.multiple = true;
        values.size = Math.min(4, Math.max(2, definition.value.length));
        const seen = new Set();
        for (const item of definition.value) {
          const stored = definition.var_type === 'kv' ? item?.value : item;
          if (typeof stored !== 'string' || seen.has(stored)) continue;
          seen.add(stored);
          const label = definition.var_type === 'kv' ? `${item.key}: ${stored}` : stored;
          const option = node('option', label); option.value = stored;
          option.selected = row.values.includes(stored);
          values.append(option);
        }
        values.onchange = () => { row.values = [...values.selectedOptions].map(option => option.value); };
        valueLabel.append(values);
      } else if (definition && definition.var_type === 'number') {
        const value = node('input'); value.type = 'number'; value.step = 'any';
        value.min = String(definition.value[0]); value.max = String(definition.value[1]);
        value.value = row.values.length ? String(row.values[0]) : '';
        value.oninput = () => { row.values = value.value === '' ? [] : [Number(value.value)]; };
        valueLabel.append(value);
      } else if (definition && definition.var_type === 'datetime') {
        const value = node('input'); value.type = 'date';
        value.min = String(definition.value[0]).slice(0, 10);
        value.max = String(definition.value[1]).slice(0, 10);
        value.value = row.values.length ? String(row.values[0]).slice(0, 10) : '';
        value.oninput = () => { row.values = value.value ? [value.value] : []; };
        valueLabel.append(value);
      } else {
        valueLabel.append(node('span', t(row.id ? 'usersVariableMissing' : 'usersVariableChoose')));
      }
      const remove = node('button', t('remove'));
      remove.type = 'button'; remove.onclick = () => { rows.splice(rows.indexOf(row), 1); render(); };
      wrapper.append(nameLabel, valueLabel, remove);
      container.append(wrapper);
    }
    if (available.length && rows.length < 32) {
      const add = node('button', t('usersVariableAdd'));
      add.type = 'button'; add.onclick = () => { rows.push({ id: '', values: [] }); render(); };
      container.append(add);
    }
  }

  render();
  return {
    refresh: render,
    read() {
      const seen = new Set();
      return rows.map(row => {
        const definition = byID.get(row.id);
        if (!definition || seen.has(row.id) || !row.values.length) throw new Error(t('usersVariableInvalid'));
        seen.add(row.id);
        if (definition.var_type === 'number') {
          const value = row.values[0];
          if (row.values.length !== 1 || !Number.isFinite(value) || value < definition.value[0] || value > definition.value[1]) {
            throw new Error(t('usersVariableInvalid'));
          }
        } else if (definition.var_type === 'datetime') {
          if (row.values.length !== 1 || row.values[0] < String(definition.value[0]).slice(0, 10) ||
            row.values[0] > String(definition.value[1]).slice(0, 10)) throw new Error(t('usersVariableInvalid'));
        } else {
          const allowed = new Set(definition.value.map(item => definition.var_type === 'kv' ? item.value : item));
          if (row.values.some(value => !allowed.has(value))) throw new Error(t('usersVariableInvalid'));
        }
        return { variableId: row.id, variableValues: row.values };
      });
    },
  };
}
