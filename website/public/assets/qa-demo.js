// This public preview is deliberately browser-only. It does not call an API or model.
(() => {
  'use strict';
  const lang = document.body.dataset.qaLang === 'en' ? 'en' : 'zh';
  const copy = {
    zh: {
      revenue: { question: '按地区统计订单金额', answer: '以下为虚构示例：华东地区订单金额最高。此回答没有经过模型推理或数据库查询。', policy: '模拟允许', sql: 'SELECT region, SUM(amount) AS total_amount\nFROM demo_orders\nGROUP BY region\nORDER BY total_amount DESC\nLIMIT 3;', columns: ['地区', '订单金额'], rows: [['华东', '¥128,400'], ['华南', '¥91,200'], ['华北', '¥76,500']], note: '示例编号 DEMO-001 · 数字及策略状态均为预置模拟内容。' },
      customers: { question: '查看消费最高的客户', answer: '以下为虚构示例：示例客户 A 的累计消费最高。真实环境需要授权、执行和审计。', policy: '模拟允许', sql: 'SELECT customer_name, SUM(amount) AS total_amount\nFROM demo_orders\nGROUP BY customer_name\nORDER BY total_amount DESC\nLIMIT 3;', columns: ['客户', '累计消费'], rows: [['示例客户 A', '¥48,300'], ['示例客户 B', '¥39,600'], ['示例客户 C', '¥27,100']], note: '示例编号 DEMO-002 · 客户名称和金额均为虚构内容。' },
      denied: { question: '请求敏感字段', answer: '此场景展示权限拒绝的界面样式。线上未执行任何权限判断或 SQL。', policy: '模拟拒绝', sql: '-- 模拟拒绝：不展示或执行敏感字段查询', columns: [], rows: [], note: '示例编号 DEMO-003 · 拒绝状态也是预置模拟内容，并非真实安全决策。' }
    },
    en: {
      revenue: { question: 'Order amount by region', answer: 'Fictional example: East has the highest order amount. No model inference or database query produced this answer.', policy: 'Simulated allow', sql: 'SELECT region, SUM(amount) AS total_amount\nFROM demo_orders\nGROUP BY region\nORDER BY total_amount DESC\nLIMIT 3;', columns: ['Region', 'Order amount'], rows: [['East', '$18,400'], ['South', '$13,200'], ['North', '$10,500']], note: 'Example DEMO-001 · Numbers and policy status are preset simulations.' },
      customers: { question: 'Top customers by spending', answer: 'Fictional example: Sample Customer A has the highest total spend. A real deployment requires authorization, execution and audit.', policy: 'Simulated allow', sql: 'SELECT customer_name, SUM(amount) AS total_amount\nFROM demo_orders\nGROUP BY customer_name\nORDER BY total_amount DESC\nLIMIT 3;', columns: ['Customer', 'Total spend'], rows: [['Sample Customer A', '$7,300'], ['Sample Customer B', '$5,600'], ['Sample Customer C', '$4,100']], note: 'Example DEMO-002 · Customer names and amounts are fictional.' },
      denied: { question: 'Request sensitive fields', answer: 'This scenario previews an access-denied interface. The public page makes no authorization decision and executes no SQL.', policy: 'Simulated deny', sql: '-- Simulated denial: no sensitive-field query is shown or executed', columns: [], rows: [], note: 'Example DEMO-003 · The denial is preset content, not a real security decision.' }
    }
  }[lang];

  const question = document.getElementById('qa-question');
  const answer = document.getElementById('qa-answer');
  const policy = document.getElementById('qa-policy');
  const sql = document.getElementById('qa-sql');
  const head = document.getElementById('qa-head');
  const body = document.getElementById('qa-body');
  const table = document.getElementById('qa-table-wrap');
  const note = document.getElementById('qa-result-note');
  const choices = [...document.querySelectorAll('[data-qa-example]')];

  function render(key) {
    const item = copy[key];
    if (!item) return;
    question.textContent = item.question;
    answer.textContent = item.answer;
    policy.textContent = item.policy;
    policy.classList.toggle('denied', key === 'denied');
    sql.textContent = item.sql;
    note.textContent = item.note;
    head.replaceChildren();
    body.replaceChildren();
    table.hidden = item.columns.length === 0;
    if (item.columns.length) {
      const row = document.createElement('tr');
      item.columns.forEach(value => { const cell = document.createElement('th'); cell.scope = 'col'; cell.textContent = value; row.append(cell); });
      head.append(row);
      item.rows.forEach(values => {
        const dataRow = document.createElement('tr');
        values.forEach(value => { const cell = document.createElement('td'); cell.textContent = value; dataRow.append(cell); });
        body.append(dataRow);
      });
    }
    choices.forEach(button => button.setAttribute('aria-pressed', String(button.dataset.qaExample === key)));
  }

  choices.forEach(button => button.addEventListener('click', () => render(button.dataset.qaExample)));
  render('revenue');
})();
