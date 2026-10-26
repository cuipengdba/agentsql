export function setupAssistants({api, node, get, canOpen}) {
  const $ = get;
  const labels = {
    'zh-CN': {nav:'小助手应用', title:'小助手应用', intro:'管理基础、高级与页面嵌入应用。数据源访问始终使用当前用户权限。', new:'新建应用', name:'名称', description:'说明', type:'类型', basic:'基础应用', advanced:'高级应用', page:'页面嵌入', domains:'允许的来源域名（多个用英文逗号分隔）', sources:'允许的数据源', configuration:'应用配置 JSON', enabled:'启用', save:'保存', remove:'删除', edit:'编辑', empty:'暂无应用', search:'搜索应用', saved:'应用配置已保存', deleted:'应用已删除', failed:'请求未完成；请检查输入或权限。', confirm:'确定删除这个应用？', noSource:'无可选数据源', pending:'嵌入调用协议正在实现与验收；当前页面只管理应用配置。'},
    en: {nav:'Assistant apps', title:'Assistant apps', intro:'Manage basic, advanced and page-embed apps. Datasource access uses the current user’s permissions.', new:'New app', name:'Name', description:'Description', type:'Type', basic:'Basic', advanced:'Advanced', page:'Page embed', domains:'Allowed origins (comma separated)', sources:'Allowed datasources', configuration:'App configuration JSON', enabled:'Enabled', save:'Save', remove:'Delete', edit:'Edit', empty:'No apps yet', search:'Search apps', saved:'App configuration saved', deleted:'App deleted', failed:'Request did not complete; check input or permissions.', confirm:'Delete this app?', noSource:'No datasource available', pending:'The embed-call protocol is still being implemented and accepted. This page manages configuration only.'}
  };
  const l = key => labels[document.documentElement.lang === 'en' ? 'en' : 'zh-CN'][key];
  const nav = node('button'); nav.id = 'assistants-nav'; nav.type = 'button'; nav.className = 'side-action'; nav.hidden = true;
  $('dashboards-nav').after(nav);
  const page = node('section'); page.id = 'assistants'; page.className = 'assistants'; page.hidden = true;
  $('dashboards').after(page);
  const heading = node('div', undefined, 'assistant-heading'), title = node('h1'), intro = node('p'), create = node('button');
  create.type = 'button'; create.className = 'primary';
  const headingCopy = node('div'); headingCopy.append(title, intro); heading.append(headingCopy, create);
  const notice = node('p', undefined, 'dashboard-note');
  const status = node('p'); status.setAttribute('role', 'status');
  const layout = node('div', undefined, 'assistant-layout'), sidebar = node('div'), search = node('input'), list = node('div');
  search.type = 'search'; search.maxLength = 120; list.className = 'assistant-list'; sidebar.append(search, list);
  const form = node('form'); form.hidden = true; form.className = 'assistant-form';
  const name = node('input'), description = node('textarea'), type = node('select'), domains = node('input'), configuration = node('textarea'), enabled = node('input'), sources = node('div', undefined, 'assistant-sources');
  name.maxLength = 120; name.required = true; description.maxLength = 2000; type.required = true; domains.maxLength = 255; domains.required = true; domains.placeholder = 'https://example.com';
  configuration.maxLength = 8192; configuration.placeholder = '{}'; enabled.type = 'checkbox';
  const field = (key, control) => { const label = node('label'); const caption = node('span'); caption.dataset.assistantLabel = key; label.append(caption, control); form.append(label); };
  field('name', name); field('description', description); field('type', type); field('domains', domains); field('sources', sources); field('configuration', configuration); field('enabled', enabled);
  const actions = node('div', undefined, 'assistant-actions'), save = node('button'), remove = node('button');
  save.type = 'submit'; save.className = 'primary'; remove.type = 'button'; actions.append(save, remove); form.append(actions);
  layout.append(sidebar, form); page.append(heading, notice, status, layout);
  let active = false, items = [], current = null, availableSources = [];
  const setStatus = (value, bad = false) => {status.textContent = value; status.classList.toggle('error', bad);};
  function renderList() {
    list.replaceChildren(); const query = search.value.trim().toLocaleLowerCase();
    for (const item of items.filter(app => app.name.toLocaleLowerCase().includes(query))) {
      const button = node('button', undefined, 'assistant-list-item'); button.type = 'button'; button.classList.toggle('active', item.id === current?.id);
      button.append(node('strong', item.name), node('small', `${l(item.type === 1 ? 'advanced' : item.type === 4 ? 'page' : 'basic')} · ${item.enabled ? l('enabled') : '—'}`));
      button.onclick = async () => { try {edit(await api('/agentsql/assistants/' + encodeURIComponent(String(item.id))));} catch {setStatus(l('failed'), true);} }; list.append(button);
    }
    if (!list.childElementCount) list.append(node('p', l('empty'), 'dashboard-note'));
  }
  function renderSources(selected = []) {
    sources.replaceChildren();
    for (const source of availableSources) {
      const label = node('label'), box = node('input'); box.type = 'checkbox'; box.value = String(source.id); box.checked = selected.map(String).includes(String(source.id));
      label.append(box, node('span', source.name)); sources.append(label);
    }
    if (!availableSources.length) sources.append(node('small', l('noSource')));
  }
  function edit(item) {
    current = item || null; form.hidden = false;
    name.value = item?.name || ''; description.value = item?.description || ''; type.value = String(item?.type ?? 0);
    domains.value = item?.domain || ''; configuration.value = item?.configuration || '{}'; enabled.checked = item?.enabled ?? true;
    renderSources(item?.datasource_ids || []); remove.hidden = !item?.id; renderList(); setStatus('');
    name.focus();
  }
  async function refresh() {items = await api('/agentsql/assistants'); if (active) renderList();}
  nav.onclick = async () => {
    if (nav.hidden || !canOpen()) return;
    document.dispatchEvent(new CustomEvent('agentsql:show-assistants'));
    active = true; page.hidden = false; nav.classList.add('active'); setStatus('');
    try {await refresh();} catch {setStatus(l('failed'), true);}
  };
  create.onclick = () => edit(null);
  search.oninput = renderList;
  form.onsubmit = async event => {
    event.preventDefault();
    const app = {name:name.value.trim(), description:description.value, type:Number(type.value), domain:domains.value.trim(), configuration:configuration.value.trim() || '{}', enabled:enabled.checked,
      datasource_ids:[...sources.querySelectorAll('input:checked')].map(input => input.value)};
    if (current?.id) Object.assign(app, {id:current.id, revision:current.revision, oid:current.oid});
    try {const saved = await api('/agentsql/assistants' + (app.id ? '/' + app.id : ''), app, app.id ? 'PUT' : 'POST'); await refresh(); edit(saved); setStatus(l('saved'));}
    catch {setStatus(l('failed'), true);}
  };
  remove.onclick = async () => {
    if (!current?.id || !confirm(l('confirm'))) return;
    try {await api('/agentsql/assistants/' + current.id + '?revision=' + current.revision, undefined, 'DELETE'); current = null; form.hidden = true; await refresh(); setStatus(l('deleted'));}
    catch {setStatus(l('failed'), true);}
  };
  function language() {
    nav.textContent = l('nav'); title.textContent = l('title'); intro.textContent = l('intro'); create.textContent = l('new'); notice.textContent = l('pending'); search.placeholder = l('search'); save.textContent = l('save'); remove.textContent = l('remove');
    for (const element of form.querySelectorAll('[data-assistant-label]')) element.textContent = l(element.dataset.assistantLabel);
    type.replaceChildren(); for (const [value, key] of [['0','basic'],['1','advanced'],['4','page']]) { const option = node('option', l(key)); option.value = value; type.append(option); }
    type.value = String(current?.type ?? 0); if (active) {renderList(); renderSources(current?.datasource_ids || []);}
  }
  language();
  return {nav, show:() => nav.click(), hide(){active = false; page.hidden = true; nav.classList.remove('active');}, visible:() => active, language, setSources(value){availableSources = value || []; if (active) renderSources(current?.datasource_ids || []);}, setAdmin(value){nav.hidden = !value;}};
}
