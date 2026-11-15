const svgNS = 'http://www.w3.org/2000/svg';
const decimal = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i;
const colors = ['#087f8c', '#b65a13', '#7551a8', '#287948'];
const own = (object, key) => Object.prototype.hasOwnProperty.call(object, key);

function element(name, attributes = {}, content = '') {
  const item = document.createElementNS(svgNS, name);
  for (const [key, value] of Object.entries(attributes)) item.setAttribute(key, String(value));
  if (content !== '') item.textContent = String(content);
  return item;
}

// Null is a missing point, not zero. Never coerce booleans, empty strings or
// integers outside the JS exact range into a plotted business value.
export function chartNumber(value) {
  if (value === null || value === undefined) return null;
  if (typeof value !== 'number' && typeof value !== 'string') return undefined;
  if (typeof value === 'string' && !decimal.test(value.trim())) return undefined;
  const number = Number(value);
  if (!Number.isFinite(number) || Math.abs(number) >= Number.MAX_SAFE_INTEGER) return undefined;
  if (typeof value === 'string' && number === 0 && /[1-9]/.test(value.toLowerCase().split('e')[0])) return undefined;
  return number;
}

export function chartFields(result) {
  const fields = result?.fields;
  const rows = result?.data;
  if (!Array.isArray(fields) || !Array.isArray(rows) || !rows.length || fields.length < 2 ||
      fields.some(field => typeof field !== 'string' || !field) || new Set(fields).size !== fields.length ||
      rows.some(row => !row || typeof row !== 'object' || Array.isArray(row))) return null;
  const numeric = fields.filter(field => {
    let present = false;
    for (const row of rows) {
      const value = chartNumber(own(row, field) ? row[field] : null);
      if (value === undefined) return false;
      if (value !== null) present = true;
    }
    return present;
  });
  return { fields: fields.slice(), numeric };
}

// The chart never stores rows, credentials or SQL. Callers provide only the
// fresh, audited result already authorized for the current visible record.
export function chartSeries(result, selection) {
  const available = chartFields(result);
  if (!available || !available.numeric.length) return null;
  const dimension = selection ? selection.dimension : available.fields.find(field => !available.numeric.includes(field)) ?? available.fields[0];
  const candidates = available.numeric.filter(field => field !== dimension);
  const business = candidates.filter(field => !/(?:^|_)id$/i.test(field));
  const measures = selection ? selection.measures : (business.length ? business : candidates).slice(0, 4);
  if (!available.fields.includes(dimension) || !Array.isArray(measures) || !measures.length || measures.length > 4 ||
      new Set(measures).size !== measures.length || measures.some(field => field === dimension || !available.numeric.includes(field))) return null;
  const items = result.data.slice(0, 20).map(row => ({
    label: String(own(row, dimension) ? row[dimension] ?? '' : ''),
    values: measures.map(field => chartNumber(own(row, field) ? row[field] : null)),
    value: chartNumber(own(row, measures[0]) ? row[measures[0]] : null)
  }));
  if (!items.some(item => item.values.some(value => value !== null))) return null;
  return { labelField: dimension, valueField: measures[0], measures: measures.slice(), items,
    totalRows: result.data.length, truncated: result.truncated === true };
}

export function pieAllowed(series) {
  return !!series && series.measures.length === 1 && series.items.every(item => item.value === null || item.value >= 0) &&
    series.items.some(item => item.value > 0);
}

export function renderChart(series, type) {
  const { items, labelField, measures } = series;
  const figure = document.createElement('figure');
  figure.className = 'result-chart';
  const title = document.createElement('figcaption');
  title.textContent = `${measures.join(' / ')} · ${labelField} · ${items.length}/${series.totalRows}`;
  figure.append(title);
  if (!['bar', 'horizontal', 'line', 'pie'].includes(type) || (type === 'pie' && !pieAllowed(series))) return figure;
  const svg = element('svg', { viewBox: '0 0 760 340', role: 'img', 'aria-label': `${type}: ${measures.join(', ')} by ${labelField}` });
  const numberTitle = (mark, item, measure, value) => {
    mark.append(element('title', {}, `${item.label} · ${measure}: ${value}`));
    svg.append(mark);
  };
  if (type === 'pie') {
    const total = items.reduce((sum, item) => sum + (item.value ?? 0), 0);
    let angle = -Math.PI / 2;
    items.forEach((item, index) => {
      if (item.value === null || item.value === 0) return;
      const sweep = item.value / total * Math.PI * 2;
      const end = angle + Math.min(sweep, Math.PI * 2 - 0.000001);
      const point = a => `${380 + 136 * Math.cos(a)} ${170 + 136 * Math.sin(a)}`;
      numberTitle(element('path', { d: `M 380 170 L ${point(angle)} A 136 136 0 ${sweep > Math.PI ? 1 : 0} 1 ${point(end)} Z`,
        fill: `hsl(${(index * 47 + 164) % 360} 58% 42%)` }), item, measures[0], item.value);
      angle += sweep;
    });
  } else {
    const values = items.flatMap(item => item.values).filter(value => value !== null);
    const min = Math.min(0, ...values), max = Math.max(0, ...values), range = max - min || 1;
    const y = value => 278 - (value - min) / range * 240;
    const x = value => 125 + (value - min) / range * 590;
    const width = 640 / items.length;
    svg.append(element('line', type === 'horizontal' ?
      { x1: x(0), x2: x(0), y1: 22, y2: 300, class: 'chart-axis' } :
      { x1: 60, x2: 720, y1: y(0), y2: y(0), class: 'chart-axis' }));
    for (const [measureIndex, measure] of measures.entries()) {
      if (type === 'line') {
        let run = [];
        const flush = () => {
          if (run.length > 1) svg.append(element('polyline', { points: run.join(' '), fill: 'none', stroke: colors[measureIndex], 'stroke-width': 2 }));
          run = [];
        };
        items.forEach((item, index) => {
          const value = item.values[measureIndex];
          if (value === null) { flush(); return; }
          run.push(`${60 + width * (index + .5)},${y(value)}`);
        });
        flush();
      }
      items.forEach((item, index) => {
        const value = item.values[measureIndex];
        if (value === null) return;
        const center = 60 + width * (index + .5);
        let mark;
        if (type === 'line') mark = element('circle', { cx: center, cy: y(value), r: 4, fill: colors[measureIndex] });
        else if (type === 'horizontal') {
          const group = 270 / items.length, height = group * .72 / measures.length;
          mark = element('rect', { x: Math.min(x(0), x(value)), y: 25 + group * index + height * measureIndex,
            width: Math.abs(x(value) - x(0)), height, fill: colors[measureIndex] });
        } else {
          const barWidth = width * .72 / measures.length;
          mark = element('rect', { x: center - width * .36 + measureIndex * barWidth,
            y: Math.min(y(0), y(value)), width: barWidth, height: Math.abs(y(value) - y(0)), fill: colors[measureIndex] });
        }
        numberTitle(mark, item, measure, value);
      });
    }
    items.forEach((item, index) => {
      if (items.length > 10 && index % Math.ceil(items.length / 10)) return;
      svg.append(element('text', type === 'horizontal' ?
        { x: 115, y: 25 + 270 / items.length * (index + .6), 'text-anchor': 'end', class: 'chart-label' } :
        { x: 60 + width * (index + .5), y: 307, 'text-anchor': 'middle', class: 'chart-label' }, item.label.slice(0, 12)));
    });
    for (const value of new Set([min, max])) svg.append(element('text', type === 'horizontal' ?
      { x: x(value), y: 328, 'text-anchor': 'middle', class: 'chart-value' } :
      { x: 54, y: y(value), 'text-anchor': 'end', class: 'chart-value' }, String(value)));
  }
  figure.append(svg);
  const legend = document.createElement('div'); legend.className = 'chart-legend';
  const entries = type === 'pie' ? items.filter(item => item.value !== null).map((item, index) => [item.label, String(item.value), `hsl(${(items.indexOf(item) * 47 + 164) % 360} 58% 42%)`]) :
    measures.map((measure, index) => [measure, '', colors[index]]);
  for (const [label, value, color] of entries) {
    const entry = document.createElement('span'), swatch = document.createElement('i');
    swatch.style.backgroundColor = color; entry.append(swatch, document.createTextNode(label + (value ? `: ${value}` : ''))); legend.append(entry);
  }
  figure.append(legend);
  return figure;
}

export function resultChartPanel(result, storedChart, table, t) {
  const available = chartFields(result), initial = chartSeries(result);
  if (!initial) {
    if (result?.truncated !== true) return table;
    const section = document.createElement('section'), note = document.createElement('p');
    note.className = 'chart-note'; note.textContent = t('chartTruncated'); section.append(table, note); return section;
  }
  const root = document.createElement('section'); root.className = 'chart-panel';
  const tabs = document.createElement('div'); tabs.className = 'chart-tabs';
  const controls = document.createElement('div'); controls.className = 'chart-controls';
  const plot = document.createElement('div'); plot.className = 'chart-plot';
  const note = document.createElement('p'); note.className = 'chart-note';
  const dimension = document.createElement('select'), measures = document.createElement('select');
  measures.multiple = true; measures.size = Math.min(4, available.numeric.length);
  const addLabel = (key, input) => { const label = document.createElement('label'); label.append(document.createTextNode(t(key)), input); controls.append(label); };
  addLabel('chartDimension', dimension); addLabel('chartMeasures', measures);
  let selectedDimension = initial.labelField, selectedMeasures = initial.measures.slice(), active = 'table';
  try {
    const chart = JSON.parse(storedChart || 'null');
    const field = entry => entry && typeof entry.value === 'string' ? entry.value : null;
    if (chart && ['column', 'bar', 'line', 'pie'].includes(chart.type)) {
      const wanted = { dimension: field(chart.type === 'pie' ? chart.axis?.series : chart.axis?.x),
        measures: chart.type === 'pie' ? [field(chart.axis?.y)] : Array.isArray(chart.axis?.y) ? chart.axis.y.map(field) : [] };
      const validated = chartSeries(result, wanted);
      if (validated && (chart.type !== 'pie' || pieAllowed(validated))) {
        selectedDimension = wanted.dimension; selectedMeasures = wanted.measures;
        active = chart.type === 'column' ? 'bar' : chart.type === 'bar' ? 'horizontal' : chart.type;
      }
    }
  } catch { /* Malformed presentation metadata never becomes query authority. */ }
  for (const field of available.fields) { const option = document.createElement('option'); option.value = field; option.textContent = field; dimension.append(option); }
  dimension.value = selectedDimension;
  function measureChoices() {
    measures.replaceChildren();
    for (const field of available.numeric.filter(field => field !== dimension.value)) {
      const option = document.createElement('option'); option.value = field; option.textContent = field;
      option.selected = selectedMeasures.includes(field); measures.append(option);
    }
  }
  const buttons = [];
  for (const [type, key] of [['table','chartTable'],['bar','chartBar'],['horizontal','chartHorizontal'],['line','chartLine'],['pie','chartPie']]) {
    const button = document.createElement('button'); button.type = 'button'; button.className = 'chart-tab'; button.textContent = t(key);
    button.onclick = () => { active = type; draw(); }; buttons.push([type, button]); tabs.append(button);
  }
  function draw() {
    const series = chartSeries(result, { dimension: dimension.value, measures: selectedMeasures });
    for (const [type, button] of buttons) {
      button.setAttribute('aria-pressed', String(active === type));
      button.disabled = type !== 'table' && (!series || (type === 'pie' && !pieAllowed(series)));
    }
    controls.hidden = active === 'table';
    if (active === 'table') { plot.replaceChildren(table); note.textContent = series?.truncated ? t('chartTruncated') : ''; return; }
    if (!series || (active === 'pie' && !pieAllowed(series))) {
      plot.replaceChildren(table); note.textContent = t('chartInvalid'); return;
    }
    plot.replaceChildren(renderChart(series, active));
    note.textContent = `${t('chartCurrentOnly')} · ${series.items.length}/${series.totalRows}${series.truncated ? ` · ${t('chartTruncated')}` : ''}`;
  }
  dimension.onchange = () => {
    selectedMeasures = selectedMeasures.filter(field => field !== dimension.value);
    if (!selectedMeasures.length) selectedMeasures = available.numeric.filter(field => field !== dimension.value).slice(0, 4);
    measureChoices(); draw();
  };
  measures.onchange = () => { selectedMeasures = Array.from(measures.selectedOptions, option => option.value); draw(); };
  measureChoices(); root.append(tabs, controls, plot, note); draw();
  return root;
}
