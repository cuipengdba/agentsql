const svgNS = 'http://www.w3.org/2000/svg';

function element(name, attributes = {}, content = '') {
  const item = document.createElementNS(svgNS, name);
  for (const [key, value] of Object.entries(attributes)) item.setAttribute(key, String(value));
  if (content) item.textContent = content;
  return item;
}

// Charts use only the result returned by a fresh, audited gateway read. The
// canvas never stores result rows or turns the chart into query authority.
export function chartSeries(result) {
  const fields = Array.isArray(result?.fields) ? result.fields : [];
  const rows = Array.isArray(result?.data) ? result.data : [];
  if (fields.length < 2 || !rows.length) return null;
  const numeric = new Set((result.fields_info || []).filter(field => field.is_numeric).map(field => field.name));
  const valueField = fields.find(field => numeric.has(field)) || fields.find(field => rows.every(row => row[field] === null || Number.isFinite(Number(row[field]))));
  const labelField = fields.find(field => field !== valueField);
  if (!valueField || !labelField) return null;
  const items = rows.slice(0, 20).filter(row => row[valueField] !== null && row[valueField] !== undefined && row[valueField] !== '').map(row => ({
    label: String(row[labelField] ?? ''), value: Number(row[valueField])
  })).filter(item => Number.isFinite(item.value));
  if (!items.length || items.every(item => item.value === 0)) return null;
  return { labelField, valueField, items, totalRows: rows.length };
}

export function renderChart(series, type) {
  const { items, labelField, valueField } = series;
  const figure = document.createElement('figure');
  figure.className = 'result-chart';
  const title = document.createElement('figcaption');
  title.textContent = `${valueField} · ${labelField}`;
  if (series.totalRows > items.length) title.textContent += ` · ${items.length}/${series.totalRows}`;
  figure.append(title);
  const svg = element('svg', { viewBox: '0 0 640 290', role: 'img', 'aria-label': `${type}: ${valueField} by ${labelField}` });
  if (type === 'pie') {
    const shown = items.filter(item => item.value > 0).slice(0, 12);
    if (shown.length < items.length) title.textContent += ` · ${shown.length}/${items.length}`;
    const sum = shown.reduce((total, item) => total + item.value, 0);
    if (sum <= 0) return figure;
    let angle = -Math.PI / 2;
    shown.forEach((item, index) => {
      if (item.value === 0) return;
      const sweep = item.value / sum * Math.PI * 2;
      const end = angle + Math.min(sweep, Math.PI * 2 - 0.000001);
      const point = a => `${220 + 104 * Math.cos(a)} ${143 + 104 * Math.sin(a)}`;
      const path = element('path', {
        d: `M 220 143 L ${point(angle)} A 104 104 0 ${sweep > Math.PI ? 1 : 0} 1 ${point(end)} Z`,
        fill: `hsl(${(index * 47 + 164) % 360} 58% 48%)`
      });
      const caption = element('title', {}, `${item.label}: ${item.value}`);
      path.append(caption); svg.append(path); angle += sweep;
      svg.append(element('text', { x: 352, y: 28 + index * 18, class: 'chart-label' }, `${item.label.slice(0, 24)} · ${item.value}`));
    });
  } else {
    const min = Math.min(0, ...items.map(item => item.value));
    const max = Math.max(0, ...items.map(item => item.value));
    const range = max - min;
    const ordinate = value => 232 - (value - min) / range * 190;
    const baseline = ordinate(0);
    const width = 530 / items.length;
    svg.append(element('line', { x1: 62, y1: baseline, x2: 612, y2: baseline, class: 'chart-axis' }));
    if (type === 'line') {
      const points = items.map((item, index) => `${62 + width * (index + .5)},${ordinate(item.value)}`).join(' ');
      svg.append(element('polyline', { points, fill: 'none', class: 'chart-line' }));
    }
    items.forEach((item, index) => {
      const x = 62 + width * (index + .5);
      const y = ordinate(item.value);
      if (type === 'bar') svg.append(element('rect', { x: x - width * .32, y: Math.min(y, baseline), width: width * .64, height: Math.abs(baseline - y), class: 'chart-bar' }));
      else svg.append(element('circle', { cx: x, cy: y, r: 4, class: 'chart-point' }));
      const tick = element('text', { x, y: 251, 'text-anchor': 'middle', class: 'chart-label' }, items.length <= 10 || index % Math.ceil(items.length / 10) === 0 ? item.label.slice(0, 10) : '');
      svg.append(tick);
      const value = element('text', { x, y: Math.max(28, Math.min(275, y + (item.value < 0 ? 14 : -7))), 'text-anchor': 'middle', class: 'chart-value' }, String(item.value));
      svg.append(value);
    });
  }
  figure.append(svg);
  return figure;
}
