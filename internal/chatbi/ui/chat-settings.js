export function setupChatSettings({api, node, get, canOpen, onChange}) {
  const $ = get;
  const labels = {
    'zh-CN': {nav:'问数设置',title:'问数设置',intro:'设置问数助手的显示方式和多轮上下文。查询行数始终受安全上限约束。',name:'助手名称',hideThinking:'隐藏模型思考过程',expandThinking:'默认展开思考过程',contextRecords:'上下文轮数（0–3）',showSQL:'显示 SQL',showLog:'显示执行日志',limit:'查询行数上限始终开启，不能关闭。',save:'保存',reload:'重新读取',saved:'设置已保存并审计。',failed:'无法读取或保存设置；请检查权限和输入。'},
    en: {nav:'Q&A settings',title:'Q&A settings',intro:'Configure the assistant display and conversation context. Query rows remain bounded by the safety limit.',name:'Assistant name',hideThinking:'Hide model reasoning',expandThinking:'Expand reasoning by default',contextRecords:'Context turns (0–3)',showSQL:'Show SQL',showLog:'Show execution log',limit:'The query row limit remains enforced and cannot be disabled.',save:'Save',reload:'Reload',saved:'Settings saved with an audit record.',failed:'Could not read or save settings; check access and input.'}
  };
  const l = key => labels[document.documentElement.lang === 'en' ? 'en' : 'zh-CN'][key];
  const nav = node('button'); nav.id = 'chat-settings-nav'; nav.type = 'button'; nav.className = 'side-action'; nav.hidden = true;
  $('models-nav').after(nav);
  const page = node('section'); page.id = 'chat-settings'; page.className = 'users'; page.hidden = true;
  $('models').after(page);
  const heading = node('div', undefined, 'users-heading'), title = node('h1'), intro = node('p');
  heading.append(node('span','AGENTSQL','eyebrow'),title,intro);
  const form = node('form'); form.className = 'semantic-form';
  const name = node('input'), context = node('input');
  name.required = true; name.maxLength = 80; context.type = 'number'; context.min = '0'; context.max = '3'; context.step = '1'; context.required = true;
  const hideThinking = node('input'), expandThinking = node('input'), showSQL = node('input'), showLog = node('input');
  for (const control of [hideThinking,expandThinking,showSQL,showLog]) control.type = 'checkbox';
  const field = (key,control) => {const label = node('label');const caption = node('span');caption.dataset.chatSettingsLabel = key;label.append(caption,control);form.append(label);};
  field('name',name);field('hideThinking',hideThinking);field('expandThinking',expandThinking);field('contextRecords',context);field('showSQL',showSQL);field('showLog',showLog);
  const limit = node('p'), status = node('p'); status.setAttribute('role','status');
  const actions = node('div',undefined,'semantic-form-actions'), reload = node('button'), save = node('button');
  reload.type = 'button';save.type = 'submit';save.className = 'primary';actions.append(reload,save);
  form.append(limit,status,actions);page.append(heading,form);
  let active = false, epoch = 0;
  const setStatus = (message,bad=false) => {status.textContent=message;status.classList.toggle('error',bad);};
  async function refresh(){
    const current = ++epoch;setStatus('');
    try{
      const value = await api('/agentsql/chat-parameters');
      if(!active||current!==epoch)return false;
      name.value=value.name||'AgentSQL';hideThinking.checked=!!value.hide_thinking;expandThinking.checked=!!value.expand_thinking;
      context.value=String(value.context_records);showSQL.checked=!!value.show_sql;showLog.checked=!!value.show_log;
      onChange(value);
      return true;
    }catch{if(active&&current===epoch)setStatus(l('failed'),true);return false;}
  }
  nav.onclick=()=>{if(nav.hidden||!canOpen())return;document.dispatchEvent(new CustomEvent('agentsql:show-chat-settings'));active=true;page.hidden=false;nav.classList.add('active');void refresh();};
  reload.onclick=()=>{if(active)void refresh();};
  form.onsubmit=async event=>{
    event.preventDefault();if(!active)return;
    const count=Number(context.value);
    if(!Number.isInteger(count)||count<0||count>3){setStatus(l('failed'),true);return;}
    save.disabled=true;
    try{
      await api('/agentsql/chat-parameters',{name:name.value.trim(),hide_thinking:hideThinking.checked,expand_thinking:expandThinking.checked,
        limit_rows:true,context_records:count,show_sql:showSQL.checked,show_log:showLog.checked},'PUT');
      if(active&&await refresh())setStatus(l('saved'));
    }catch{if(active)setStatus(l('failed'),true);}finally{save.disabled=false;}
  };
  function language(){nav.textContent=l('nav');title.textContent=l('title');intro.textContent=l('intro');limit.textContent=l('limit');reload.textContent=l('reload');save.textContent=l('save');setStatus('');for(const label of form.querySelectorAll('[data-chat-settings-label]'))label.textContent=l(label.dataset.chatSettingsLabel);}
  language();
  return {nav,hide(){active=false;epoch++;page.hidden=true;nav.classList.remove('active');},visible:()=>active,language,setAdmin(value){nav.hidden=!value;if(!value&&active)this.hide();}};
}
