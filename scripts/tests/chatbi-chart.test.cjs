const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(process.env.CHART_MODULE_PATH || path.resolve(__dirname, '../../internal/chatbi/ui/chart.js'), 'utf8');
const modulePromise = import('data:text/javascript;base64,' + Buffer.from(source).toString('base64'));
const result = () => ({ fields: ['id', 'region', 'sales', 'cost'], fields_info: [], data: [
  { id: 1, region: 'east', sales: '10', cost: null }, { id: 2, region: 'west', sales: 0, cost: '5.5' }
] });

test('default dimension is categorical and numeric identifiers are not default business measures', async () => {
  const { chartSeries } = await modulePromise;
  const chart = chartSeries(result());
  assert.equal(chart.labelField, 'region');
  assert.deepEqual(chart.measures, ['sales', 'cost']);
  assert.deepEqual(chart.items.map(item => item.values), [[10, null], [0, 5.5]]);
});

test('numeric dimensions and explicit measure order are honored without changing the raw result', async () => {
  const { chartSeries } = await modulePromise;
  const raw = result(), before = JSON.stringify(raw);
  const chart = chartSeries(raw, { dimension: 'id', measures: ['cost', 'sales'] });
  assert.equal(chart.labelField, 'id');
  assert.deepEqual(chart.measures, ['cost', 'sales']);
  assert.deepEqual(chart.items.map(item => item.values), [[null, 10], [5.5, 0]]);
  assert.equal(JSON.stringify(raw), before);
});

test('missing values stay missing and all-zero cartesian results remain available', async () => {
  const { chartSeries } = await modulePromise;
  const data = { fields: ['category', 'amount'], data: [{ category: 'a', amount: 0 }, { category: 'b', amount: null }] };
  assert.deepEqual(chartSeries(data).items.map(item => item.value), [0, null]);
});

test('booleans, blanks, masked text, nondecimal values and unsafe magnitudes are not coerced', async () => {
  const { chartNumber, chartSeries } = await modulePromise;
  for (const value of [true, false, '', ' ', '0x10', 'NaN', 'Infinity', NaN, Infinity, {}, [],
    '9007199254740993', '9007199254740991.1', '1e-400', 1e100]) assert.equal(chartNumber(value), undefined, String(value));
  for (const value of [null, undefined]) assert.equal(chartNumber(value), null);
  assert.equal(chartNumber('1.25e2'), 125);
  const data = { fields: ['x', 'y'], fields_info: [{ name: 'y', is_numeric: true }], data: [{ x: 'a', y: '***' }] };
  assert.equal(chartSeries(data), null);
});

test('one negative measure or multiple measures prevent pie charts rather than dropping values', async () => {
  const { chartSeries, pieAllowed } = await modulePromise;
  const raw = result();
  assert.equal(pieAllowed(chartSeries(raw)), false);
  raw.data[1].sales = -2;
  assert.equal(pieAllowed(chartSeries(raw, { dimension: 'region', measures: ['sales'] })), false);
  raw.data[1].sales = 0;
  assert.equal(pieAllowed(chartSeries(raw, { dimension: 'region', measures: ['sales'] })), true);
  raw.data[0].sales = 0;
  assert.equal(pieAllowed(chartSeries(raw, { dimension: 'region', measures: ['sales'] })), false);
});

test('unknown, duplicate, empty and oversized field selections fail closed to the raw table', async () => {
  const { chartSeries } = await modulePromise;
  for (const selection of [
    { dimension: 'missing', measures: ['sales'] }, { dimension: 'region', measures: ['missing'] },
    { dimension: 'sales', measures: ['sales'] }, { dimension: 'region', measures: [] },
    { dimension: null, measures: ['sales'] }, { dimension: 'region', measures: null },
    { dimension: 'region', measures: ['sales', 'sales'] },
    { dimension: 'region', measures: ['sales', 'cost', 'id', 'extra', 'fifth'] }
  ]) assert.equal(chartSeries(result(), selection), null);
  assert.equal(chartSeries({ fields: ['x', 'x'], data: [{ x: 1 }] }), null);
});

test('only own fields can provide values, including prototype-like field names', async () => {
  const { chartSeries } = await modulePromise;
  assert.equal(chartSeries({ fields: ['x', 'y'], data: [Object.create({ x: 'hidden', y: 25 })] }), null);
  const row = JSON.parse('{"__proto__":"<img src=x onerror=alert(1)>","amount":"20"}');
  const chart = chartSeries({ fields: ['__proto__', 'amount'], data: [row] });
  assert.equal(chart.labelField, '__proto__');
  assert.equal(chart.items[0].label, '<img src=x onerror=alert(1)>');
  assert.equal(chart.items[0].value, 20);
});

test('chart rows are bounded and retain explicit returned and truncated counts', async () => {
  const { chartSeries } = await modulePromise;
  const data = { fields: ['x', 'y'], truncated: true, data: Array.from({ length: 30 }, (_, i) => ({ x: 'group'+i, y: i })) };
  const chart = chartSeries(data);
  assert.equal(chart.items.length, 20);
  assert.equal(chart.totalRows, 30);
  assert.equal(chart.truncated, true);
  assert.equal(data.data.length, 30);
});
