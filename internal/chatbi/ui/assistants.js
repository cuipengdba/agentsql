export function setupAssistants({api, node, get, canOpen}) {
  const $ = get;
  const labels = {
    'zh-CN': {nav:'小助手应用', title:'小助手应用', intro:'管理基础、高级与页面嵌入应用。每次数据访问均复核账号和数据源权限。', new:'新建应用', name:'名称', description:'说明', type:'类型', basic:'基础应用', advanced:'高级应用', page:'页面嵌入', domains:'允许的来源域名（多个用英文逗号或分号分隔）', sources:'允许的数据源', configuration:'应用配置 JSON', useCustomModel:'指定模型', customModel:'模型', enabled:'启用', save:'保存', remove:'删除', edit:'编辑', empty:'暂无应用', search:'搜索应用', saved:'应用配置已保存', deleted:'应用已删除', failed:'请求未完成；请检查输入或权限。', guestSaveFailed:'公开访客配置未通过；请核对账号、当前空间、各数据源的独立绑定和来源域名。', confirm:'确定删除这个应用？', noSource:'无可选数据源', pending:'嵌入调用协议正在实现与验收；当前页面只管理应用配置。', publicGuest:'公开访客账号（仅基础应用）', guestSearch:'按账号或姓名搜索', noGuest:'不公开', guestWarning:'选择专用普通账号后，列出的数据源会对所有能访问该助手的人公开。该账号必须在当前空间有独立数据源绑定；不要选择私有数据。'},
    en: {nav:'Assistant apps', title:'Assistant apps', intro:'Manage basic, advanced and page-embed apps. Each data read rechecks account and datasource permissions.', new:'New app', name:'Name', description:'Description', type:'Type', basic:'Basic', advanced:'Advanced', page:'Page embed', domains:'Allowed origins (comma or semicolon separated)', sources:'Allowed datasources', configuration:'App configuration JSON', useCustomModel:'Use specific model', customModel:'Model', enabled:'Enabled', save:'Save', remove:'Delete', edit:'Edit', empty:'No apps yet', search:'Search apps', saved:'App configuration saved', deleted:'Deleted', failed:'Request did not complete; check input or permissions.', guestSaveFailed:'Public guest setup was rejected. Check the account, workspace, separate bindings for every datasource, and allowed origin.', confirm:'Delete this app?', noSource:'No datasource available', pending:'The embed-call protocol is still being implemented and accepted. This page manages configuration only.', publicGuest:'Public guest account (basic apps only)', guestSearch:'Search account or name', noGuest:'Not public', guestWarning:'Selecting a dedicated regular account makes the listed datasources public to anyone who can reach this assistant. The account needs separate datasource bindings in this workspace. Do not choose private data.'}
  };
  Object.assign(labels['zh-CN'], {display:'展示设置', displayIntro:'图片仅限 PNG/JPG，单张最多 10 MiB、2048×2048 像素。仅启用并引用图片的应用可读取。', theme:'主题颜色', headerColor:'标题文字颜色', welcome:'欢迎语', welcomeDesc:'欢迎说明', logo:'应用 Logo', floatIcon:'浮窗图标', removeImage:'移除图片', xSide:'水平位置', ySide:'垂直位置', xOffset:'水平偏移（像素）', yOffset:'垂直偏移（像素）', drag:'允许拖动浮窗图标', displaySave:'保存展示设置', displaySaved:'展示设置已保存', invalidImage:'请选择不超过 10 MiB 的 PNG/JPG 图片。', left:'左', right:'右', top:'上', bottom:'下'});
  Object.assign(labels.en, {display:'Display settings', displayIntro:'PNG/JPG only, up to 10 MiB and 2048×2048 pixels each. Only an enabled app that still references an image can read it.', theme:'Theme color', headerColor:'Header text color', welcome:'Welcome text', welcomeDesc:'Welcome description', logo:'App logo', floatIcon:'Floating icon', removeImage:'Remove image', xSide:'Horizontal side', ySide:'Vertical side', xOffset:'Horizontal offset (pixels)', yOffset:'Vertical offset (pixels)', drag:'Allow dragging the floating icon', displaySave:'Save display settings', displaySaved:'Display settings saved', invalidImage:'Choose a PNG/JPG image no larger than 10 MiB.', left:'Left', right:'Right', top:'Top', bottom:'Bottom'});
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
  const name = node('input'), description = node('textarea'), type = node('select'), domains = node('input'), configuration = node('textarea'), enabled = node('input'), useCustomModel = node('input'), customModel = node('select'), sources = node('div', undefined, 'assistant-sources');
  const guestSearch = node('input'), publicGuest = node('select'), guestWarning = node('p', undefined, 'dashboard-note');
  name.maxLength = 120; name.required = true; description.maxLength = 2000; type.required = true; domains.maxLength = 255; domains.required = true; domains.placeholder = 'https://example.com';
  configuration.maxLength = 8192; configuration.placeholder = '{}'; enabled.type = 'checkbox'; useCustomModel.type = 'checkbox';
  const field = (key, control) => { const label = node('label'); const caption = node('span'); caption.dataset.assistantLabel = key; label.append(caption, control); form.append(label); return label; };
  field('name', name); field('description', description); field('type', type); field('domains', domains); field('sources', sources);
  const guestField = field('publicGuest', publicGuest); guestSearch.type = 'search'; guestSearch.maxLength = 100;
  guestField.before(guestSearch); guestWarning.dataset.assistantLabel = 'guestWarning'; form.append(guestWarning);
  field('configuration', configuration); field('useCustomModel', useCustomModel); field('customModel', customModel); field('enabled', enabled);
  const display = node('details', undefined, 'assistant-display'), displaySummary = node('summary'), displayFields = node('div', undefined, 'assistant-display-fields');
  displaySummary.dataset.assistantLabel = 'display'; display.append(displaySummary, displayFields);
  const displayIntro = node('p', undefined, 'dashboard-note'); displayIntro.dataset.assistantLabel = 'displayIntro'; displayFields.append(displayIntro);
  const displayField = (key, control) => {const label = node('label'), caption = node('span'); caption.dataset.assistantLabel = key; label.append(caption, control); displayFields.append(label);};
  const theme = node('input'), headerColor = node('input'), welcome = node('input'), welcomeDesc = node('textarea');
  theme.type = 'color'; headerColor.type = 'color'; welcome.maxLength = 500; welcomeDesc.maxLength = 2000;
  displayField('theme', theme); displayField('headerColor', headerColor); displayField('welcome', welcome); displayField('welcomeDesc', welcomeDesc);
  const imageControl = key => {
    const field = node('div', undefined, 'assistant-image-field'), caption = node('span'), group = node('div', undefined, 'assistant-image-control');
    const preview = node('img'), file = node('input'), clear = node('button');
    caption.id = 'assistant-' + key + '-label'; caption.dataset.assistantLabel = key;
    preview.alt = ''; preview.hidden = true; file.type = 'file'; file.accept = '.png,.jpg,.jpeg,image/png,image/jpeg'; file.setAttribute('aria-labelledby', caption.id);
    clear.type = 'button'; clear.dataset.assistantLabel = 'removeImage'; group.append(preview, file, clear); field.append(caption, group); displayFields.append(field);
    return {preview, file, clear};
  };
  const logoImage = imageControl('logo'), floatingImage = imageControl('floatIcon');
  const xSide = node('select'), xOffset = node('input'), ySide = node('select'), yOffset = node('input'), drag = node('input');
  for (const input of [xOffset, yOffset]) {input.type = 'number'; input.min = '0'; input.max = '4096';}
  drag.type = 'checkbox'; displayField('xSide', xSide); displayField('xOffset', xOffset); displayField('ySide', ySide); displayField('yOffset', yOffset); displayField('drag', drag);
  const displaySave = node('button'); displaySave.type = 'button'; displaySave.className = 'primary'; displaySave.dataset.assistantLabel = 'displaySave'; displayFields.append(displaySave);
  form.append(display);
  const actions = node('div', undefined, 'assistant-actions'), save = node('button'), remove = node('button');
  save.type = 'submit'; save.className = 'primary'; remove.type = 'button'; actions.append(save, remove); form.append(actions);
  layout.append(sidebar, form); page.append(heading, notice, status, layout);
  let active = false, items = [], current = null, availableSources = [], availableModels = [], clearLogo = false, clearFloating = false;
  let guestWorkspace = '', guestCandidates = [], guestRequest = 0, guestSearchTimer;
  const setStatus = (value, bad = false) => {status.textContent = value; status.classList.toggle('error', bad);};
  function renderGuests(selected = '') {
    publicGuest.replaceChildren();
    const none = node('option', l('noGuest')); none.value = ''; publicGuest.append(none);
    for (const user of guestCandidates) {
      const option = node('option', `${user.name} (${user.account})`); option.value = String(user.id); publicGuest.append(option);
    }
    if (selected && !guestCandidates.some(user => String(user.id) === selected)) {
      const option = node('option', `#${selected}`); option.value = selected; publicGuest.append(option);
    }
    publicGuest.value = selected;
  }
  async function loadGuests(keyword = '') {
    if (!guestWorkspace) return;
    const request = ++guestRequest;
    const query = new URLSearchParams({status:'1', origins:'0', oidlist:guestWorkspace, keyword});
    const page = await api('/user/pager/1/100?' + query);
    if (request !== guestRequest) return;
    guestCandidates = (page.items || []).filter(user => !user.isAdmin && String(user.oid) === guestWorkspace && user.origin === 0 && user.status === 1);
    renderGuests(publicGuest.value);
  }
  function showGuestField() {
    const visible = type.value === '0'; guestSearch.hidden = !visible; guestField.hidden = !visible; guestWarning.hidden = !visible;
  }
  type.onchange = showGuestField;
  guestSearch.oninput = () => {
    clearTimeout(guestSearchTimer);
    guestSearchTimer = setTimeout(() => loadGuests(guestSearch.value.trim()).catch(() => setStatus(l('failed'), true)), 250);
  };
  guestSearch.onkeydown = event => {if (event.key === 'Enter') event.preventDefault();};
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
  function renderModels(value) {
    customModel.replaceChildren();
    for (const model of availableModels) {const option = node('option', model.name); option.value = String(model.id); customModel.append(option);}
    if (value) customModel.value = String(value);
    customModel.disabled = !useCustomModel.checked;
  }
  useCustomModel.onchange = () => {customModel.disabled = !useCustomModel.checked;};
  const displayConfig = item => {try {return JSON.parse(item?.configuration || '{}') || {};} catch {return {};}};
  function renderDisplay(item) {
    display.hidden = !item?.id; display.open = false; clearLogo = false; clearFloating = false;
    logoImage.file.value = ''; floatingImage.file.value = '';
    if (!item?.id) return;
    const config = displayConfig(item);
    theme.value = /^#[0-9a-fA-F]{6}$/.test(config.theme) ? config.theme : '#18b59b';
    headerColor.value = /^#[0-9a-fA-F]{6}$/.test(config.header_font_color) ? config.header_font_color : '#222831';
    welcome.value = typeof config.welcome === 'string' ? config.welcome : '';
    welcomeDesc.value = typeof config.welcome_desc === 'string' ? config.welcome_desc : '';
    xSide.value = config.x_type === 'left' ? 'left' : 'right'; ySide.value = config.y_type === 'top' ? 'top' : 'bottom';
    xOffset.value = String(Number.isInteger(config.x_val) ? config.x_val : 0); yOffset.value = String(Number.isInteger(config.y_val) ? config.y_val : 0);
    drag.checked = config.float_icon_drag !== false;
    for (const [control, id] of [[logoImage, config.logo], [floatingImage, config.float_icon]]) {
      control.preview.hidden = typeof id !== 'string' || !id;
      if (!control.preview.hidden) control.preview.src = '/api/v1/system/assistant/picture/' + encodeURIComponent(id);
    }
  }
  logoImage.clear.onclick = () => {clearLogo = true; logoImage.file.value = ''; logoImage.preview.hidden = true;};
  floatingImage.clear.onclick = () => {clearFloating = true; floatingImage.file.value = ''; floatingImage.preview.hidden = true;};
  logoImage.file.onchange = () => {if (logoImage.file.files.length) clearLogo = false;};
  floatingImage.file.onchange = () => {if (floatingImage.file.files.length) clearFloating = false;};
  displaySave.onclick = async () => {
    if (!current?.id || [xOffset, yOffset].some(input => input.value === '' || !input.validity.valid || !Number.isInteger(Number(input.value)))) {setStatus(l('failed'), true); return;}
    const formData = new FormData(), config = displayConfig(current);
    for (const [control, flag] of [[logoImage, 'logo'], [floatingImage, 'float_icon']]) {
      const file = control.file.files[0];
      if (!file) continue;
      if (file.size > 10 * 1024 * 1024 || file.name.length > 128 || !/\.(png|jpe?g)$/i.test(file.name)) {setStatus(l('invalidImage'), true); return;}
      formData.append('files', file, file.name + ',' + flag);
    }
    formData.append('data', JSON.stringify({id:current.id, theme:theme.value, header_font_color:headerColor.value,
      welcome:welcome.value, welcome_desc:welcomeDesc.value, float_icon_drag:drag.checked,
      x_type:xSide.value, x_val:Number(xOffset.value), y_type:ySide.value, y_val:Number(yOffset.value),
      logo:clearLogo ? '' : (config.logo || ''), float_icon:clearFloating ? '' : (config.float_icon || '')}));
    try {await api('/system/assistant/ui', formData, 'PATCH'); const fresh = await api('/agentsql/assistants/' + current.id); await refresh(); edit(fresh); display.open = true; setStatus(l('displaySaved'));}
    catch {setStatus(l('failed'), true);}
  };
  function edit(item) {
    current = item || null; form.hidden = false;
    name.value = item?.name || ''; description.value = item?.description || ''; type.value = String(item?.type ?? 0);
    domains.value = item?.domain || ''; configuration.value = item?.configuration || '{}'; enabled.checked = item?.enabled ?? true; useCustomModel.checked = item?.enable_custom_model || false;
    guestSearch.value = ''; renderGuests(String(item?.public_guest_user || '')); showGuestField();
    renderSources(item?.datasource_ids || []); renderModels(item?.custom_model); renderDisplay(item); remove.hidden = !item?.id; renderList(); setStatus('');
    if (active) loadGuests().catch(() => setStatus(l('failed'), true));
    name.focus();
  }
  async function refresh() {items = await api('/agentsql/assistants'); if (active) renderList();}
  nav.onclick = async () => {
    if (nav.hidden || !canOpen()) return;
    document.dispatchEvent(new CustomEvent('agentsql:show-assistants'));
    active = true; page.hidden = false; nav.classList.add('active'); setStatus('');
    try {const [models, user] = await Promise.all([api('/system/aimodel/list/by_ws'), api('/user/info')]); availableModels = models; guestWorkspace = String(user.oid); renderModels(current?.custom_model); await Promise.all([refresh(), loadGuests()]);} catch {setStatus(l('failed'), true);}
  };
  create.onclick = () => edit(null);
  search.oninput = renderList;
  form.onsubmit = async event => {
    event.preventDefault();
    const app = {name:name.value.trim(), description:description.value, type:Number(type.value), domain:domains.value.trim(), configuration:configuration.value.trim() || '{}', enabled:enabled.checked, enable_custom_model:useCustomModel.checked, custom_model:useCustomModel.checked ? customModel.value : null,
      datasource_ids:[...sources.querySelectorAll('input:checked')].map(input => input.value)};
    if (app.type === 0 && publicGuest.value) app.public_guest_user = publicGuest.value;
    if (current?.id) Object.assign(app, {id:current.id, revision:current.revision, oid:current.oid});
    try {const saved = await api('/agentsql/assistants' + (app.id ? '/' + app.id : ''), app, app.id ? 'PUT' : 'POST'); await refresh(); edit(saved); setStatus(l('saved'));}
    catch (error) {setStatus(app.public_guest_user && error.status === 422 ? l('guestSaveFailed') : l('failed'), true);}
  };
  remove.onclick = async () => {
    if (!current?.id || !confirm(l('confirm'))) return;
    try {await api('/agentsql/assistants/' + current.id + '?revision=' + current.revision, undefined, 'DELETE'); current = null; form.hidden = true; await refresh(); setStatus(l('deleted'));}
    catch {setStatus(l('failed'), true);}
  };
  function language() {
    nav.textContent = l('nav'); title.textContent = l('title'); intro.textContent = l('intro'); create.textContent = l('new'); notice.textContent = l('pending'); search.placeholder = l('search'); guestSearch.placeholder = l('guestSearch'); guestSearch.setAttribute('aria-label', l('guestSearch')); save.textContent = l('save'); remove.textContent = l('remove');
    for (const element of form.querySelectorAll('[data-assistant-label]')) element.textContent = l(element.dataset.assistantLabel);
    logoImage.clear.setAttribute('aria-label', l('removeImage') + ' ' + l('logo'));
    floatingImage.clear.setAttribute('aria-label', l('removeImage') + ' ' + l('floatIcon'));
    type.replaceChildren(); for (const [value, key] of [['0','basic'],['1','advanced'],['4','page']]) { const option = node('option', l(key)); option.value = value; type.append(option); }
    const priorX = xSide.value || 'right', priorY = ySide.value || 'bottom';
    xSide.replaceChildren(); ySide.replaceChildren();
    for (const [select, choices] of [[xSide, ['left','right']], [ySide, ['top','bottom']]]) {
      for (const key of choices) {const option = node('option', l(key)); option.value = key; select.append(option);}
    }
    xSide.value = priorX; ySide.value = priorY;
    type.value = String(current?.type ?? 0); showGuestField(); renderGuests(publicGuest.value); if (active) {renderList(); renderSources(current?.datasource_ids || []); renderModels(current?.custom_model);}
  }
  language();
  return {nav, show:() => nav.click(), hide(){active = false; page.hidden = true; nav.classList.remove('active');}, visible:() => active, language, setSources(value){availableSources = value || []; if (active) renderSources(current?.datasource_ids || []);}, setAdmin(value){nav.hidden = !value;}};
}
