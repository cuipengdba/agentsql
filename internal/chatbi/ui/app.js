import { chartSeries, renderChart } from './chart.js';
import { setupDashboard } from './dashboard.js';
import { setupAssistants } from './assistants.js';

const apiRoot = new URL('../api/v1', import.meta.url).pathname;
const $ = id => document.getElementById(id);
const copy = {
  'zh-CN': {menu:'菜单',loginTitle:'与数据对话',loginSubtitle:'使用你的账号登录 AgentSQL',account:'账号',password:'密码',login:'登录',qa:'问数',newChat:'＋ 新建对话',history:'最近对话',logout:'退出',protected:'受控数据访问',theme:'切换主题',title:'让每个问题，都有数据依据。',subtitle:'选择数据源，用自然语言探索业务数据。',feature1:'按权限查询',feature2:'敏感信息脱敏',feature3:'全程可追溯',datasource:'数据源',question:'你的问题',placeholder:'例如：按地区汇总销售额',review:'请结合 SQL 与结果核实模型回答。',cancel:'停止',send:'发送 ↗',refreshNotice:'历史结果会按当前权限重新读取。',working:'正在生成并验证查询…',reading:'正在读取结果…',empty:'没有可用的数据源，请联系管理员配置访问权限。',failed:'请求未完成，请检查权限或稍后重试。',cancelled:'已停止请求。',sql:'查看 SQL',rows:'行',audit:'审计记录',noRows:'查询成功，没有匹配的数据。',unfinished:'请求尚未完成。',loginFailed:'登录失败，请检查账号和密码。'},
  en: {menu:'Menu',loginTitle:'Talk to your data',loginSubtitle:'Sign in to AgentSQL with your account',account:'Account',password:'Password',login:'Sign in',qa:'Q&A',newChat:'+ New conversation',history:'Recent conversations',logout:'Sign out',protected:'Controlled data access',theme:'Toggle theme',title:'Give every question a data-backed answer.',subtitle:'Choose a datasource and explore your business data in natural language.',feature1:'Permission-aware queries',feature2:'Sensitive data masking',feature3:'Traceable results',datasource:'Datasource',question:'Your question',placeholder:'For example: summarize sales by region',review:'Review the SQL and results to verify model answers.',cancel:'Stop',send:'Send ↗',refreshNotice:'Historical results are refreshed under current permissions.',working:'Generating and checking your query…',reading:'Loading results…',empty:'No datasources are available. Ask your administrator to configure access.',failed:'The request did not complete. Check permissions or try again later.',cancelled:'Request stopped.',sql:'View SQL',rows:'rows',audit:'Audit record',noRows:'The query succeeded with no matching rows.',unfinished:'This request has not completed.',loginFailed:'Sign-in failed. Check your account and password.'}
};
Object.assign(copy['zh-CN'], {
  semanticNav:'业务语义',semanticTitle:'业务语义',semanticIntro:'术语和 SQL 示例只作为已授权问数的参考，不会绕过当前数据库权限。',semanticAdd:'新增',terms:'术语',examples:'SQL 示例',search:'搜索',termWord:'术语',description:'说明',aliases:'别名（逗号分隔）',termSources:'限定数据源（可不选）',exampleQuestion:'示例问题',exampleSQL:'示例 SQL',exampleSource:'数据源',enabled:'启用',disabled:'已停用',save:'保存',formCancel:'取消',previous:'上一页',next:'下一页',edit:'编辑',remove:'删除',disable:'停用',enable:'启用',noSemantic:'暂无记录',semanticSaved:'已保存',semanticDeleted:'已删除',confirmDelete:'确定删除这条记录？',items:'条',newTerm:'新增术语',editTerm:'编辑术语',newExample:'新增 SQL 示例',editExample:'编辑 SQL 示例',sourceRequired:'请先配置有权限的数据源。'
});
Object.assign(copy['zh-CN'], {sourceNav:'数据源',sourceTitle:'数据源',sourceIntro:'查看当前有权访问的数据源、表和字段。预览仍通过受控网关执行。',sourceEmpty:'没有可用的数据源',tableEmpty:'当前没有可见表',fieldEmpty:'当前没有可见字段',previewEmpty:'预览没有返回数据',previewTitle:'预览（最多 100 行）',sourceLoading:'正在读取当前权限下的元数据…'});
Object.assign(copy.en, {
  semanticNav:'Business semantics',semanticTitle:'Business semantics',semanticIntro:'Terms and SQL examples provide context for authorized Q&A; they never bypass current database permissions.',semanticAdd:'Add',terms:'Terms',examples:'SQL examples',search:'Search',termWord:'Term',description:'Description',aliases:'Aliases (comma separated)',termSources:'Limit to datasources (optional)',exampleQuestion:'Example question',exampleSQL:'Example SQL',exampleSource:'Datasource',enabled:'Enabled',disabled:'Disabled',save:'Save',formCancel:'Cancel',previous:'Previous',next:'Next',edit:'Edit',remove:'Delete',disable:'Disable',enable:'Enable',noSemantic:'No records yet',semanticSaved:'Saved',semanticDeleted:'Deleted',confirmDelete:'Delete this record?',items:'items',newTerm:'Add term',editTerm:'Edit term',newExample:'Add SQL example',editExample:'Edit SQL example',sourceRequired:'Configure an authorized datasource first.'
});
Object.assign(copy.en, {sourceNav:'Datasources',sourceTitle:'Datasources',sourceIntro:'Explore your currently authorized datasources, tables, and fields. Previews still run through the controlled gateway.',sourceEmpty:'No datasources are available',tableEmpty:'No visible tables',fieldEmpty:'No visible fields',previewEmpty:'No rows returned for this preview',previewTitle:'Preview (up to 100 rows)',sourceLoading:'Loading metadata under current permissions…'});
Object.assign(copy['zh-CN'], {suggestions:'推荐问题',positioning:'AgentSQL 提供独立问数页面，也可作为现有 SQLBot 的数据库安全网关。可用范围以当前发布说明为准。',renameChat:'重命名',deleteChat:'删除',renamePrompt:'对话名称（最多 64 个字）',deletePrompt:'确定删除这个对话？',invalidChatName:'对话名称须为 1–64 个字。',searchChats:'搜索对话',noMatchingChats:'没有匹配的对话'});
Object.assign(copy.en, {suggestions:'Suggested questions',positioning:'AgentSQL has its own Q&A interface and can also secure database access for an existing SQLBot deployment. See the current release notes for verified scope.',renameChat:'Rename',deleteChat:'Delete',renamePrompt:'Conversation name (up to 64 characters)',deletePrompt:'Delete this conversation?',invalidChatName:'The conversation name must be 1–64 characters.',searchChats:'Search conversations',noMatchingChats:'No matching conversations'});
Object.assign(copy['zh-CN'], {chartTable:'表格',chartBar:'柱状图',chartLine:'折线图',chartPie:'饼图'});
Object.assign(copy.en, {chartTable:'Table',chartBar:'Bar',chartLine:'Line',chartPie:'Pie'});
Object.assign(copy['zh-CN'], {dashboardNav:'仪表板',dashboardTitle:'仪表板',dashboardIntro:'保存画布配置。每次打开都按当前权限重新查询并审计。',dashboardNew:'新建仪表板',dashboardName:'名称',dashboardAddText:'添加文字',dashboardAddChart:'添加当前对话结果',dashboardCardTitle:'卡片标题',dashboardText:'文字',dashboardChart:'图表',dashboardPending:'保存后按当前权限加载结果。',dashboardNoChart:'这份结果不能生成所选图表。',dashboardEmpty:'暂无仪表板',dashboardSaving:'正在保存…',dashboardSaved:'已保存，并重新按当前权限读取。',dashboardDeleted:'仪表板已删除。',dashboardConfirmDelete:'确定删除这个仪表板？'});
Object.assign(copy.en, {dashboardNav:'Dashboards',dashboardTitle:'Dashboards',dashboardIntro:'Save canvas configuration. Opening a dashboard reruns every query under current permissions with an audit.',dashboardNew:'New dashboard',dashboardName:'Name',dashboardAddText:'Add text',dashboardAddChart:'Add current conversation result',dashboardCardTitle:'Card title',dashboardText:'Text',dashboardChart:'Chart',dashboardPending:'Save to load this result under current permissions.',dashboardNoChart:'This result cannot produce the selected chart.',dashboardEmpty:'No dashboards yet',dashboardSaving:'Saving…',dashboardSaved:'Saved and refreshed under current permissions.',dashboardDeleted:'Dashboard deleted.',dashboardConfirmDelete:'Delete this dashboard?'});
Object.assign(copy['zh-CN'], {analyze:'分析结果',predict:'趋势判断',narrativeScope:'分析行数',narrativeMethod:'方法：基于当前授权样本的模型回答',narrativeCaution:'模型回答可能有误，请结合 SQL 和数据核实；数值趋势只是线性外推示意。',narrativeStale:'当前数据或权限已变化，历史模型回答不再显示。',forecastUnavailable:'当前结果不是至少三行的有序时间或数值序列，只提供文字判断。',forecastMethod:'线性趋势示意'});
Object.assign(copy.en, {analyze:'Analyze result',predict:'Trend outlook',narrativeScope:'Analyzed rows',narrativeMethod:'Method: model answer from a currently authorized sample',narrativeCaution:'The model can be wrong. Verify against the SQL and data; numeric trends are illustrative linear extrapolations.',narrativeStale:'Current data or permissions changed. The historical model answer is hidden.',forecastUnavailable:'This result is not an ordered numeric or date series with at least three rows. Only a text outlook is available.',forecastMethod:'Illustrative linear trend'});
Object.assign(copy['zh-CN'], {recommendations:'推荐问题',recommendationSource:'推荐问题的数据源',recommendationQuestions:'每行一个问题，最多 20 个',recommendationPlaceholder:'例如：按地区查看销售额',recommendationNote:'推荐问题只填入提问框；点击发送后仍按当前权限执行并审计。',recommendationInvalid:'最多 20 个问题，每个问题不超过 500 字。'});
Object.assign(copy.en, {recommendations:'Suggestions',recommendationSource:'Datasource for suggestions',recommendationQuestions:'One question per line, up to 20',recommendationPlaceholder:'For example: show revenue by region',recommendationNote:'A suggestion only fills the composer. Submitting it still checks current permissions and records an audit.',recommendationInvalid:'Use at most 20 questions, each at most 500 characters.'});
Object.assign(copy['zh-CN'], {exportCSV:'导出 CSV',exportXLSX:'导出 XLSX',exporting:'正在按当前权限准备导出…'});
Object.assign(copy.en, {exportCSV:'Export CSV',exportXLSX:'Export XLSX',exporting:'Preparing export under current permissions…'});
Object.assign(copy['zh-CN'], {usage:'执行详情',usageStarted:'开始时间',usageFinished:'完成时间',usageDuration:'耗时',usageTokens:'消耗 Token',usageStepsEmpty:'暂无阶段明细',seconds:'秒'});
Object.assign(copy.en, {usage:'Execution details',usageStarted:'Started',usageFinished:'Finished',usageDuration:'Duration',usageTokens:'Tokens used',usageStepsEmpty:'No step trace is available',seconds:'seconds'});
Object.assign(copy['zh-CN'], {accountSettings:'账号设置',passwordNote:'修改密码后，当前会话及其他已登录会话都需要重新登录。',currentPassword:'当前密码',newPassword:'新密码',changePassword:'修改密码',passwordChanged:'密码已修改，请重新登录。'});
Object.assign(copy.en, {accountSettings:'Account settings',passwordNote:'Changing your password signs out this and all other sessions.',currentPassword:'Current password',newPassword:'New password',changePassword:'Change password',passwordChanged:'Password changed. Sign in again.'});
Object.assign(copy['zh-CN'], {usersNav:'用户管理',usersTitle:'用户管理',usersIntro:'查看账号、工作空间成员关系，并启用或停用用户。变更会撤销旧会话并留下管理审计。',usersSearch:'搜索账号、姓名或邮箱',usersWorkspace:'工作空间',usersMember:'已加入当前空间',usersNotMember:'未加入当前空间',usersGrant:'加入空间',usersRevoke:'移出空间',confirmRevokeUser:'移出此用户并撤销其旧会话？',usersEnabled:'已启用',usersDisabled:'已停用',usersDisable:'停用',usersEnable:'启用',usersEmpty:'没有匹配的用户',usersCount:'位用户',usersCountOne:'位用户',confirmDisableUser:'停用此用户并撤销其全部会话？',usersSaved:'用户状态已更新',usersMemberSaved:'成员关系已更新'});
Object.assign(copy.en, {usersNav:'Users',usersTitle:'Users',usersIntro:'Review accounts and workspace membership, and enable or disable users. Changes revoke old sessions and leave a management audit.',usersSearch:'Search account, name or email',usersWorkspace:'Workspace',usersMember:'Member of this workspace',usersNotMember:'Not a member',usersGrant:'Add to workspace',usersRevoke:'Remove from workspace',confirmRevokeUser:'Remove this user and revoke old sessions?',usersEnabled:'Active',usersDisabled:'Disabled',usersDisable:'Disable',usersEnable:'Enable',usersEmpty:'No matching users',usersCount:'users',usersCountOne:'user',confirmDisableUser:'Disable this user and revoke all sessions?',usersSaved:'User status updated',usersMemberSaved:'Workspace membership updated'});
Object.assign(copy['zh-CN'], {importFile:'导入 CSV/XLSX',importName:'数据源名称',importChoose:'选择文件（最多 4 MiB）',importPreview:'预览工作表',importSave:'导入并创建数据源',importRows:'行',importNoSheets:'请选择至少一个工作表。',importReady:'预览完成。选择工作表和字段类型后创建数据源。',importWorking:'正在通过受控网关导入并创建数据源…',importDone:'数据源已创建。',importFileChanged:'文件已更换，请重新预览。'});
Object.assign(copy.en, {importFile:'Import CSV/XLSX',importName:'Datasource name',importChoose:'Choose a file (up to 4 MiB)',importPreview:'Preview sheets',importSave:'Import and create datasource',importRows:'rows',importNoSheets:'Select at least one sheet.',importReady:'Preview ready. Choose sheets and field types, then create the datasource.',importWorking:'Importing through the controlled gateway…',importDone:'Datasource created.',importFileChanged:'File changed. Preview it again.'});
Object.assign(copy['zh-CN'], {deleteSource:'删除文件数据源',deleteSourcePrompt:'删除这个文件数据源及其托管表？此操作无法撤销。',sourceDeleted:'文件数据源已删除。'});
Object.assign(copy.en, {deleteSource:'Delete file datasource',deleteSourcePrompt:'Delete this file datasource and its managed tables? This cannot be undone.',sourceDeleted:'File datasource deleted.'});
copy['zh-CN'].semanticIntro='术语和 SQL 示例仅作为已授权问数的参考；推荐问题只填入提问框，查询仍按当前权限执行。';
copy.en.semanticIntro='Terms and SQL examples inform authorized Q&A. Suggestions only fill the composer; every query still checks current permissions.';
const requestedLang=new URLSearchParams(location.search).get('lang');
let lang=(requestedLang==='en'||requestedLang==='zh-CN'?requestedLang:localStorage.getItem('agentsql-chatbi-language'))==='en'?'en':'zh-CN';
let token=sessionStorage.getItem('agentsql-chatbi-token')||'';
let current=null,controller=null,sourceList=[],sourceVisible=false,sourceEpoch=0,selectedSource=null,semanticVisible=false,semanticKind='terms',semanticPage=1,semanticTotalPages=0,semanticEditing=null,suggestionEpoch=0,recommendationEpoch=0,recommendationRevision=null,recommendationItems=[],historyItems=[],historyEpoch=0,importStage=null,isAdmin=false;
let usersVisible=false,usersEpoch=0,userPage=1,userTotalPages=1;
const t=key=>copy[lang][key];
function translate(){document.documentElement.lang=lang;document.querySelectorAll('[data-i18n]').forEach(el=>{el.textContent=t(el.dataset.i18n)});document.querySelectorAll('.history-action').forEach(el=>{el.title=t(el.dataset.i18n)});document.querySelectorAll('[data-placeholder]').forEach(el=>{el.placeholder=t(el.dataset.placeholder)});$('language').textContent=lang==='en'?'简体中文':'English';$('login-language').textContent=$('language').textContent;}
function theme(value){document.documentElement.dataset.theme=value;localStorage.setItem('agentsql-chatbi-theme',value);}
theme(localStorage.getItem('agentsql-chatbi-theme')||(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'));translate();
function status(message,error=false){$('status').textContent=message;$('status').classList.toggle('error',error);}
function node(tag,text,cls){const el=document.createElement(tag);if(text!==undefined)el.textContent=text;if(cls)el.className=cls;return el;}
async function api(path,body,method=body===undefined?'GET':'POST',signal){const response=await fetch(apiRoot+path,{method,headers:{'Content-Type':'application/json',...(token?{'X-SQLBOT-TOKEN':'Bearer '+token}:{})},body:body===undefined?undefined:JSON.stringify(body),signal});if(response.status===401){token='';sessionStorage.removeItem('agentsql-chatbi-token');hideUsers();$('users-list').replaceChildren();$('workspace').hidden=true;$('login').hidden=false;$('login-tools').hidden=false;}if(!response.ok){const error=new Error(t('failed'));error.status=response.status;throw error;}const envelope=await response.json();if(envelope.code!==0)throw new Error(t('failed'));return envelope.data;}
const dashboard=setupDashboard({api,t,node,get:$});
const assistants=setupAssistants({api,node,get:$,canOpen:()=>!controller});
async function loadSuggestions(){
  const epoch=++suggestionEpoch,source=$('datasource').value;
  $('suggestions').hidden=true;$('suggestion-list').replaceChildren();
  if(!source||!token)return;
  try{
    const questions=await api('/recommended_problem/get_datasource_recommended/'+encodeURIComponent(source));
    if(epoch!==suggestionEpoch||$('datasource').value!==source||!token)return;
    for(const item of questions){
      if(typeof item.question!=='string'||!item.question.trim())continue;
      const button=node('button',item.question.trim());button.type='button';
      button.onclick=()=>{$('question').value=item.question.trim();$('question').focus();};
      $('suggestion-list').append(button);
    }
    $('suggestions').hidden=!$('suggestion-list').childElementCount;
  }catch{ /* Suggestions are optional; authorization failures never expose stale items. */ }
}
function renderHistory(){
  const nav=$('history');nav.replaceChildren();
  const query=$('history-search').value.trim().toLocaleLowerCase();
  const items=historyItems.filter(item=>String(item.brief||'').toLocaleLowerCase().includes(query));
  if(query&&items.length===0){nav.append(node('p',t('noMatchingChats'),'history-empty'));return;}
  for(const item of items){
    const row=node('div',undefined,'history-row');
    const button=node('button',item.brief,'history-open');button.type='button';
    button.classList.toggle('active',String(item.id)===String(current));button.onclick=()=>open(item.id);
    const rename=node('button',t('renameChat'),'history-action');rename.type='button';rename.title=t('renameChat');rename.dataset.i18n='renameChat';
    rename.onclick=async()=>{
      if(controller)return;
      const brief=window.prompt(t('renamePrompt'),item.brief);if(brief===null)return;
      const name=brief.trim();if(!name||[...name].length>64){status(t('invalidChatName'),true);return;}
      try{await api('/chat/rename',{id:item.id,brief:name});await history();status('');}catch{status(t('failed'),true);}
    };
    const remove=node('button',t('deleteChat'),'history-action');remove.type='button';remove.title=t('deleteChat');remove.dataset.i18n='deleteChat';
    remove.onclick=async()=>{
      if(controller||!window.confirm(t('deletePrompt')))return;
      try{await api('/chat/'+encodeURIComponent(String(item.id)),undefined,'DELETE');
        if(String(item.id)===String(current)){$('new-chat').click();}else await history();status('');
      }catch{status(t('failed'),true);}
    };
    row.append(button,rename,remove);nav.append(row);
  }
}
async function history(){
  const epoch=++historyEpoch,items=await api('/chat/list');
  if(epoch!==historyEpoch||!token)return;
  historyItems=items.sort((a,b)=>new Date(b.latest_record_time)-new Date(a.latest_record_time));
  renderHistory();
}
async function enter(){
  const user=await api('/user/info');
  isAdmin=Boolean(user.isAdmin);
  if(!isAdmin){hideUsers();$('users-list').replaceChildren();}
  $('account-name').textContent=user.name||user.account;
  if(!requestedLang){
    const saved=localStorage.getItem('agentsql-chatbi-language');
    const server=user.language==='en'||user.language==='en-US'?'en':user.language==='zh-CN'?'zh-CN':null;
    if(saved==='en'||saved==='zh-CN'){
      lang=saved;
      if(server&&server!==saved)try{await api('/user/language',{language:saved},'PUT');}catch{ /* Keep the local display preference if account persistence fails. */ }
    }else if(server){lang=server;localStorage.setItem('agentsql-chatbi-language',lang);}
    translate();
  }
  sourceList=await api('/datasource/list');
  $('datasource').replaceChildren();$('term-sources').replaceChildren();$('example-source').replaceChildren();$('recommendation-source').replaceChildren();
  for(const source of sourceList){
    const label=source.name+' · '+source.type;
    for(const target of [$('datasource'),$('term-sources'),$('example-source'),$('recommendation-source')]){
      const option=node('option',label);option.value=String(source.id);target.append(option);
    }
  }
  $('semantics-nav').hidden=!isAdmin;
  $('users-nav').hidden=!isAdmin;
  assistants.setAdmin(isAdmin);
  assistants.setSources(sourceList);
  $('source-import').hidden=!isAdmin;
  $('login').hidden=true;$('login-tools').hidden=true;$('workspace').hidden=false;
  if(!sourceList.length)status(t('empty'),true);
  await history();
  void loadSuggestions();
}
function show(chat){
  $('welcome').hidden=true;$('conversation').replaceChildren();
  dashboard.setRecords(chat);
  for(const record of chat.records||[]){
    if(record.first_chat&&!record.question)continue;
    if(record.action==='analysis'||record.action==='predict'){
      const turn=node('article',undefined,'turn'),answer=node('div',undefined,'answer');
      answer.append(node('strong',t(record.action==='analysis'?'analyze':'predict')));
      const content=record.action==='analysis'?record.analysis:record.predict_content;
      answer.append(node('p',content||t('narrativeStale'),'narrative-history'));
      if(content&&record.data)answer.append(node('small',`${t('audit')} #${record.data.audit_id}`,'dashboard-audit'));
      turn.append(answer);$('conversation').append(turn);continue;
    }
    const turn=node('article',undefined,'turn');turn.append(node('div',record.question,'question-text'));
    const answer=node('div',undefined,'answer');
    if(record.error){answer.append(node('p',t('failed'),'error'));}
    else if(!record.finish){answer.append(node('p',t('unfinished')));}
    else if(record.data){
      const data=record.data;
      if(!data.data?.length){answer.append(node('p',t('noRows')));}
      else{
        const wrap=node('div',undefined,'table-wrap'),table=node('table'),head=node('thead'),hrow=node('tr');
        for(const field of data.fields||[])hrow.append(node('th',field));
        head.append(hrow);table.append(head);
        const body=node('tbody');
        for(const item of data.data){const row=node('tr');for(const field of data.fields||[])row.append(node('td',item[field]===null?'—':String(item[field]??'')));body.append(row);}
        table.append(body);wrap.append(table);
        const series=chartSeries(data);
        if(series){
          const tabs=node('div',undefined,'chart-tabs'),plot=node('div',undefined,'chart-plot');
          const views={table:wrap,bar:renderChart(series,'bar'),line:renderChart(series,'line')};
          const kinds=[['table','chartTable'],['bar','chartBar'],['line','chartLine']];
          if(series.items.some(item=>item.value>0)){views.pie=renderChart(series,'pie');kinds.push(['pie','chartPie']);}
          for(const [type,key] of kinds){
            const button=node('button',t(key));button.type='button';button.className='chart-tab';
            button.setAttribute('aria-pressed',String(type==='table'));
            button.onclick=()=>{
              for(const sibling of tabs.children)sibling.setAttribute('aria-pressed',String(sibling===button));
              plot.replaceChildren(views[type]);
            };
            tabs.append(button);
          }
          plot.append(wrap);answer.append(tabs,plot);
        }else answer.append(wrap);
      }
      answer.append(node('div',`${data.data?.length||0} ${t('rows')} · ${t('audit')} #${data.audit_id}`,'audit'));
      for(const format of ['csv','xlsx']){
        const exportButton=node('button',t(format==='csv'?'exportCSV':'exportXLSX'),'export-button');
        exportButton.type='button';exportButton.onclick=()=>void downloadExport(record.id,chat.id,format,exportButton);answer.append(exportButton);
      }
      for(const kind of ['analysis','predict']){
        const button=node('button',t(kind==='analysis'?'analyze':'predict'),'export-button');button.type='button';
        const result=node('div',undefined,'narrative-result');
        button.onclick=async()=>{
          button.disabled=true;result.replaceChildren(node('p',t('reading')));answer.append(result);
          try{const data=await api('/agentsql/record/'+encodeURIComponent(String(record.id))+'/'+kind);
            if(!answer.isConnected)return;
            result.replaceChildren(node('strong',t(kind==='analysis'?'analyze':'predict')),node('p',data.content),node('small',`${t('narrativeScope')}: ${data.analyzed_rows}/${data.total_rows} · ${t('audit')} #${data.audit_id} · ${t('narrativeMethod')}`));
            result.append(node('small',t('narrativeCaution')));
            if(kind==='predict'){
              try{
                const forecast=await api('/agentsql/record/'+encodeURIComponent(String(record.id))+'/forecast');
                if(!answer.isConnected)return;
                const table=node('table'),head=node('tr'),body=node('tbody');
                head.append(node('th',forecast.label_field),node('th',forecast.value_field));table.append(node('thead'));table.tHead.append(head);
                for(const point of forecast.points){const row=node('tr');row.append(node('td',point.label),node('td',String(point.value)));body.append(row);}
                table.append(body);const wrap=node('div',undefined,'table-wrap');wrap.append(table);result.append(node('small',`${t('forecastMethod')} · ${t('audit')} #${forecast.audit_id}`),wrap);
              }catch(error){if(!answer.isConnected)return;if(error.status===422)result.append(node('small',t('forecastUnavailable')));else result.replaceChildren(node('p',t('failed'),'error'));}
            }
          }catch{if(answer.isConnected)result.replaceChildren(node('p',t('failed'),'error'));}finally{button.disabled=false;}
        };
        answer.append(button);
      }
    }
    if(record.finish){
      const usage=node('details',undefined,'usage-details'),summary=node('summary',t('usage')),content=node('div',undefined,'usage-content');
      usage.append(summary,content);usage.addEventListener('toggle',()=>{if(usage.open)void loadUsage(record.id,usage,content);});answer.append(usage);
    }
    if(record.sql){const details=node('details');details.append(node('summary',t('sql')),node('pre',record.sql));answer.append(details);}
    turn.append(answer);$('conversation').append(turn);
  }
  $('conversation').scrollTop=$('conversation').scrollHeight;
}
async function loadUsage(id,details,content){
  content.replaceChildren(node('p',t('reading')));
  try{
    const data=await api('/chat/record/'+encodeURIComponent(String(id))+'/log');
    if(!details.isConnected||!details.open)return;
    const lines=[];
    if(data.start_time)lines.push(`${t('usageStarted')}: ${new Date(data.start_time).toLocaleString(lang)}`);
    if(data.finish_time)lines.push(`${t('usageFinished')}: ${new Date(data.finish_time).toLocaleString(lang)}`);
    if(Number.isFinite(data.duration))lines.push(`${t('usageDuration')}: ${data.duration} ${t('seconds')}`);
    if(Number.isSafeInteger(data.total_tokens)&&data.total_tokens>=0)lines.push(`${t('usageTokens')}: ${data.total_tokens}`);
    content.replaceChildren(...lines.map(line=>node('p',line)),node('p',t('usageStepsEmpty')));
  }catch{if(details.isConnected&&details.open)content.replaceChildren(node('p',t('failed'),'error'));}
}
async function downloadExport(id,chatID,format,button){
  if(controller||!token)return;
  button.disabled=true;status(t('exporting'));
  try{
    const path=format==='xlsx'?'/chat/record/'+encodeURIComponent(String(id))+'/excel/export/'+encodeURIComponent(String(chatID)):'/agentsql/record/'+encodeURIComponent(String(id))+'/export.csv';
    const response=await fetch(apiRoot+path,{
      headers:{'X-SQLBOT-TOKEN':'Bearer '+token}
    });
    if(response.status===401){token='';sessionStorage.removeItem('agentsql-chatbi-token');$('workspace').hidden=true;$('login').hidden=false;$('login-tools').hidden=false;}
    if(!response.ok||!response.headers.get('X-AgentSQL-Audit-ID')||!response.headers.get('Content-Type')?.includes(format==='xlsx'?'spreadsheetml.sheet':'text/csv'))throw new Error('export');
    const blob=await response.blob();
    const url=URL.createObjectURL(blob),link=document.createElement('a');
    link.href=url;link.download='agentsql-record-'+String(id)+'.'+format;document.body.append(link);link.click();link.remove();
    setTimeout(()=>URL.revokeObjectURL(url),30000);status('');
  }catch{status(t('failed'),true);}finally{button.disabled=false;}
}
function semanticStatus(value,error=false){$('semantic-status').textContent=value;$('semantic-status').classList.toggle('error',error);}
function hideSemanticForm(){$('semantic-form').hidden=true;semanticEditing=null;}
function hideUsers(){usersVisible=false;usersEpoch++;$('users').hidden=true;$('users-nav').classList.remove('active');}
async function loadUsers(){
  if(!usersVisible)return;
  const epoch=++usersEpoch,query=new URLSearchParams({keyword:$('users-search').value.trim()});
  $('users-status').textContent=t('reading');
  try{
    const result=await api('/user/pager/'+userPage+'/20?'+query);
    if(!usersVisible||epoch!==usersEpoch)return;
    userTotalPages=Math.max(1,result.total_pages||0);
    if(userPage>userTotalPages){userPage=userTotalPages;return loadUsers();}
    const list=$('users-list');list.replaceChildren();
    for(const user of result.items||[]){
      const article=node('article',undefined,'user-item'),details=node('div');
      const workspace=$('users-workspace').value,member=(user.oid_list||[]).some(id=>String(id)===workspace);
      details.append(node('strong',user.name||user.account),node('small',user.account+(user.email?' · '+user.email:'')),node('small',t(user.status===1?'usersEnabled':'usersDisabled')+' · '+t(member?'usersMember':'usersNotMember')));
      const action=node('button',t(user.status===1?'usersDisable':'usersEnable'));action.type='button';
      action.onclick=async()=>{
        if(user.status===1&&!window.confirm(t('confirmDisableUser')))return;
        action.disabled=true;
        try{await api('/user/status',{id:user.id,status:user.status===1?0:1},'PATCH');await loadUsers();if(!$('users-status').classList.contains('error'))$('users-status').textContent=t('usersSaved');}
        catch{$('users-status').textContent=t('failed');$('users-status').classList.add('error');}
        finally{action.disabled=false;}
      };
      const membership=node('button',t(member?'usersRevoke':'usersGrant'));membership.type='button';membership.disabled=!workspace;
      membership.onclick=async()=>{
        if(member&&!window.confirm(t('confirmRevokeUser')))return;
        membership.disabled=true;
        try{await api('/system/workspace/uws',{uid_list:[user.id],oid:workspace},member?'DELETE':'POST');await loadUsers();if(!$('users-status').classList.contains('error'))$('users-status').textContent=t('usersMemberSaved');}
        catch{$('users-status').textContent=t('failed');$('users-status').classList.add('error');}
        finally{membership.disabled=false;}
      };
      const actions=node('div',undefined,'user-actions');actions.append(membership,action);
      article.append(details,actions);list.append(article);
    }
    if(!result.items?.length)list.append(node('p',t('usersEmpty'),'semantic-empty'));
    $('users-count').textContent=`${userPage}/${userTotalPages} · ${result.total} ${t(result.total===1?'usersCountOne':'usersCount')}`;
    $('users-prev').disabled=userPage<=1;$('users-next').disabled=userPage>=userTotalPages;
    $('users-status').textContent='';$('users-status').classList.remove('error');
  }catch{if(usersVisible&&epoch===usersEpoch){$('users-status').textContent=t('failed');$('users-status').classList.add('error');}}
}
async function showUsers(){
  if(controller||!isAdmin)return;
  dashboard.hide();assistants.hide();closeMenu();
  sourceVisible=false;sourceEpoch++;$('sources').hidden=true;$('sources-nav').classList.remove('active');
  semanticVisible=false;$('semantics').hidden=true;$('semantics-nav').classList.remove('active');
  $('welcome').hidden=true;$('conversation').hidden=true;$('composer-wrap').hidden=true;
  usersVisible=true;$('users').hidden=false;$('users-nav').classList.add('active');
  const epoch=++usersEpoch,previous=$('users-workspace').value;
  try{
    const workspaces=await api('/system/workspace');
    if(!usersVisible||epoch!==usersEpoch)return;
    $('users-workspace').replaceChildren();
    for(const workspace of workspaces){const option=node('option',workspace.name);option.value=String(workspace.id);$('users-workspace').append(option);}
    if(workspaces.some(workspace=>String(workspace.id)===previous))$('users-workspace').value=previous;
    await loadUsers();
  }catch{$('users-status').textContent=t('failed');$('users-status').classList.add('error');}
}
function showQA(){
  dashboard.hide();
  assistants.hide();
  hideUsers();
  sourceVisible=false;sourceEpoch++;$('sources').hidden=true;$('sources-nav').classList.remove('active');
  semanticVisible=false;$('semantics').hidden=true;$('composer-wrap').hidden=false;
  $('semantics-nav').classList.remove('active');
  $('conversation').hidden=false;$('welcome').hidden=!!current;
}
async function showSemantics(){
  if(controller||$('semantics-nav').hidden)return;
  dashboard.hide();
  assistants.hide();
  hideUsers();
  sourceVisible=false;sourceEpoch++;$('sources').hidden=true;$('sources-nav').classList.remove('active');
  closeMenu();semanticVisible=true;$('welcome').hidden=true;$('conversation').hidden=true;$('composer-wrap').hidden=true;
  $('semantics').hidden=false;$('semantics-nav').classList.add('active');
  await loadSemantics();
}
async function loadRecommendationSettings(){
  const epoch=++recommendationEpoch,source=$('recommendation-source').value;
  recommendationRevision=null;recommendationItems=[];$('recommendation-questions').value='';
  if(!source){semanticStatus(t('sourceRequired'),true);return;}
  semanticStatus('');
  try{
    const result=await api('/agentsql/recommendations/'+encodeURIComponent(source));
    if(epoch!==recommendationEpoch||!semanticVisible||semanticKind!=='recommendations'||$('recommendation-source').value!==source)return;
    recommendationRevision=result.revision;recommendationItems=result.questions||[];
    $('recommendation-questions').value=recommendationItems.map(item=>item.question).join('\n');
  }catch{if(epoch===recommendationEpoch)semanticStatus(t('failed'),true);}
}
async function saveRecommendationSettings(event){
  event.preventDefault();
  const source=$('recommendation-source').value,revision=recommendationRevision,epoch=recommendationEpoch;
  if(!source||revision===null)return;
  const lines=$('recommendation-questions').value.split(/\r?\n/).map(value=>value.trim()).filter(Boolean);
  if(lines.length>20||lines.some(value=>value.length>500)){semanticStatus(t('recommendationInvalid'),true);return;}
  const button=$('recommendation-form').querySelector('button[type=submit]');button.disabled=true;
  try{
    const questions=lines.map((question,index)=>({...((recommendationItems[index]?.id)?{id:recommendationItems[index].id}:{}),question}));
    const result=await api('/agentsql/recommendations/'+encodeURIComponent(source),{revision,questions},'PUT');
    if(epoch!==recommendationEpoch||$('recommendation-source').value!==source||!semanticVisible||semanticKind!=='recommendations')return;
    recommendationRevision=result.revision;recommendationItems=result.questions||[];
    $('recommendation-questions').value=recommendationItems.map(item=>item.question).join('\n');
    semanticStatus(t('semanticSaved'));void loadSuggestions();
  }catch{semanticStatus(t('failed'),true);}finally{button.disabled=false;}
}
function sourceStatus(value,error=false){$('source-status').textContent=value;$('source-status').classList.toggle('error',error);}
function clearImportStage(){importStage=null;$('import-sheets').replaceChildren();$('import-save').hidden=true;}
function renderImportStage(sheets){
  const target=$('import-sheets');target.replaceChildren();
  for(const sheet of sheets){
    const section=node('section',undefined,'import-sheet');
    const label=node('label'),check=node('input');check.type='checkbox';check.checked=true;check.className='import-sheet-choice';
    label.append(check,document.createTextNode(' '+sheet.sheetName+' · '+sheet.rows+' '+t('importRows')));section.append(label);
    const fields=node('div',undefined,'import-fields');
    for(const field of sheet.fields||[]){
      const item=node('label',undefined,'import-field'),select=node('select');select.dataset.fieldName=field.fieldName;
      for(const type of ['string','int','float','datetime']){const option=node('option',type);option.value=type;select.append(option);}
      select.value=field.fieldType;
      item.append(node('span',field.fieldName),select);fields.append(item);
    }
    section.append(fields);
    if(sheet.data?.length){const wrap=node('div',undefined,'table-wrap'),table=node('table'),head=node('thead'),headRow=node('tr');
      for(const field of sheet.fields||[])headRow.append(node('th',field.fieldName));head.append(headRow);table.append(head);
      const body=node('tbody');for(const row of sheet.data){const tr=node('tr');for(const field of sheet.fields||[])tr.append(node('td',String(row[field.fieldName]??'')));body.append(tr);}table.append(body);wrap.append(table);section.append(wrap);
    }
    target.append(section);
  }
  $('import-save').hidden=false;
}
async function previewImport(){
  const file=$('import-file').files?.[0],button=$('import-preview');
  if(!file||file.size>4*1024*1024){sourceStatus(t('failed'),true);return;}
  clearImportStage();button.disabled=true;sourceStatus(t('sourceLoading'));
  try{
    const form=new FormData();form.append('file',file);
    const response=await fetch(apiRoot+'/datasource/parseExcel',{method:'POST',headers:{'X-SQLBOT-TOKEN':'Bearer '+token},body:form});
    if(!response.ok)throw new Error('preview');
    const envelope=await response.json();if(envelope.code!==0||!envelope.data?.filePath||!Array.isArray(envelope.data.data))throw new Error('preview');
    importStage=envelope.data;renderImportStage(importStage.data);
    if(!$('import-name').value.trim())$('import-name').value=file.name.replace(/\.(csv|xlsx)$/i,'').slice(0,50);
    sourceStatus(t('importReady'));
  }catch{sourceStatus(t('failed'),true);}finally{button.disabled=false;}
}
async function saveImport(event){
  event.preventDefault();if(!importStage){sourceStatus(t('importFileChanged'),true);return;}
  const sections=[...$('import-sheets').querySelectorAll('.import-sheet')];
  const sheets=sections.map((section,index)=>({section,sheet:importStage.data[index]})).filter(item=>item.section.querySelector('.import-sheet-choice').checked).map(({section,sheet})=>({sheetName:sheet.sheetName,fields:[...section.querySelectorAll('select')].map(select=>({fieldName:select.dataset.fieldName,fieldType:select.value}))}));
  if(!sheets.length){sourceStatus(t('importNoSheets'),true);return;}
  const button=$('import-save');button.disabled=true;sourceStatus(t('importWorking'));
  try{
    const filePath=importStage.filePath;
    await api('/datasource/importToDb',{filePath,sheets});
    await api('/agentsql/datasource/claim-import',{filePath,name:$('import-name').value.trim(),description:$('import-description').value.trim()});
    clearImportStage();$('source-import-form').reset();$('source-import').open=false;
    await enter();await showSources();sourceStatus(t('importDone'));
  }catch{sourceStatus(t('failed'),true);}finally{button.disabled=false;}
}
function renderSourceList(){
  const list=$('source-list');list.replaceChildren();
  const query=$('source-search').value.trim().toLocaleLowerCase();
  const matches=sourceList.filter(source=>String(source.name||'').toLocaleLowerCase().includes(query));
  for(const source of matches){
    const button=node('button',undefined,'source-card');button.type='button';
    button.classList.toggle('active',String(source.id)===String(selectedSource?.id));
    button.append(node('strong',source.name),node('small',source.type||''));
    button.onclick=()=>{void loadSource(source);};list.append(button);
  }
  if(!matches.length)list.append(node('p',t('sourceEmpty'),'semantic-empty'));
}
async function showSources(){
  if(controller)return;
  dashboard.hide();
  assistants.hide();
  hideUsers();
  closeMenu();semanticVisible=false;$('semantics').hidden=true;$('semantics-nav').classList.remove('active');
  sourceVisible=true;$('sources').hidden=false;$('sources-nav').classList.add('active');
  $('welcome').hidden=true;$('conversation').hidden=true;$('composer-wrap').hidden=true;
  const epoch=++sourceEpoch;sourceStatus(t('sourceLoading'));
  try{sourceList=await api('/datasource/list');if(!sourceVisible||epoch!==sourceEpoch)return;
    selectedSource=null;$('source-detail').hidden=true;renderSourceList();sourceStatus('');
  }catch{if(epoch===sourceEpoch)sourceStatus(t('failed'),true);}
}
async function showDashboards(){
  if(controller)return;
  assistants.hide();
  hideUsers();
  closeMenu();sourceVisible=false;sourceEpoch++;semanticVisible=false;
  $('sources').hidden=true;$('sources-nav').classList.remove('active');
  $('semantics').hidden=true;$('semantics-nav').classList.remove('active');
  $('welcome').hidden=true;$('conversation').hidden=true;$('composer-wrap').hidden=true;
  await dashboard.show();
}
document.addEventListener('agentsql:show-assistants',()=>{
  dashboard.hide();hideUsers();closeMenu();sourceVisible=false;sourceEpoch++;semanticVisible=false;
  $('sources').hidden=true;$('sources-nav').classList.remove('active');
  $('semantics').hidden=true;$('semantics-nav').classList.remove('active');
  $('welcome').hidden=true;$('conversation').hidden=true;$('composer-wrap').hidden=true;
});
async function loadSource(source){
  selectedSource=source;const epoch=++sourceEpoch;renderSourceList();sourceStatus(t('sourceLoading'));
  $('source-name').textContent=source.name;$('source-description').textContent=source.description||'';
  $('source-delete').hidden=!(isAdmin&&source.type==='excel');
  $('source-detail').hidden=false;$('source-tables').replaceChildren();$('source-fields').replaceChildren();$('source-preview').replaceChildren();
  try{
    const tables=await api('/datasource/tableList/'+encodeURIComponent(String(source.id)),{},'POST');
    if(!sourceVisible||epoch!==sourceEpoch)return;
    for(const table of tables){
      const button=node('button',table.table_name,'source-table');button.type='button';
      button.onclick=()=>{void loadSourceTable(source,table);};$('source-tables').append(button);
    }
    if(!tables.length)$('source-tables').append(node('p',t('tableEmpty'),'semantic-empty'));
    sourceStatus('');
  }catch{if(epoch===sourceEpoch)sourceStatus(t('failed'),true);}
}
async function deleteSelectedSource(){
  const source=selectedSource;if(!isAdmin||!sourceVisible||source?.type!=='excel'||!window.confirm(t('deleteSourcePrompt')))return;
  const button=$('source-delete');button.disabled=true;
  try{
    await api('/datasource/delete/'+encodeURIComponent(String(source.id))+'/'+encodeURIComponent(source.name),{},'POST');
    selectedSource=null;$('source-detail').hidden=true;await enter();await showSources();sourceStatus(t('sourceDeleted'));
  }catch{sourceStatus(t('failed'),true);}finally{button.disabled=false;}
}
function renderPreview(result){
  const target=$('source-preview');target.replaceChildren();target.append(node('h3',t('previewTitle')));
  if(!result.data?.length){target.append(node('p',t('previewEmpty'),'semantic-empty'));return;}
  const wrap=node('div',undefined,'table-wrap'),table=node('table'),head=node('thead'),heading=node('tr');
  for(const field of result.fields||[])heading.append(node('th',field));head.append(heading);table.append(head);
  const body=node('tbody');for(const item of result.data){const row=node('tr');
    for(const field of result.fields||[])row.append(node('td',item[field]===null?'—':String(item[field]??'')));
    body.append(row);
  }table.append(body);wrap.append(table);target.append(wrap);
}
async function loadSourceTable(source,table){
  const epoch=++sourceEpoch;sourceStatus(t('sourceLoading'));
  $('source-fields').replaceChildren();$('source-preview').replaceChildren();
  for(const button of $('source-tables').querySelectorAll('button'))button.classList.toggle('active',button.textContent===table.table_name);
  try{
    const id=encodeURIComponent(String(source.id)),tableID=encodeURIComponent(String(table.id));
    const fields=await api('/datasource/fieldList/'+id+'/'+tableID,{fieldName:''},'POST');
    if(!sourceVisible||epoch!==sourceEpoch)return;
    for(const field of fields){const row=node('div',undefined,'source-field');row.append(node('strong',field.field_name),node('small',field.field_type));$('source-fields').append(row);}
    if(!fields.length)$('source-fields').append(node('p',t('fieldEmpty'),'semantic-empty'));
    const result=await api('/datasource/previewData/'+id,{table:{id:String(table.id),table_name:table.table_name}},'POST');
    if(!sourceVisible||epoch!==sourceEpoch)return;
    renderPreview(result);sourceStatus('');
  }catch{if(epoch===sourceEpoch)sourceStatus(t('failed'),true);}
}
function semanticAction(label,callback){const button=node('button',label);button.type='button';button.onclick=callback;return button;}
function openSemanticForm(item=null){
  if(semanticKind==='examples'&&!sourceList.length){semanticStatus(t('sourceRequired'),true);return;}
  const form=$('semantic-form');form.reset();semanticEditing=item?.id||null;
  $('term-fields').hidden=semanticKind!=='terms';$('example-fields').hidden=semanticKind!=='examples';
  $('semantic-form-title').textContent=t(semanticKind==='terms'?(item?'editTerm':'newTerm'):(item?'editExample':'newExample'));
  $('semantic-enabled').checked=item?item.enabled:true;
  if(item&&semanticKind==='terms'){
    $('term-word').value=item.word;$('term-description').value=item.description;
    $('term-aliases').value=(item.other_words||[]).join(', ');
    for(const option of $('term-sources').options)option.selected=(item.datasource_ids||[]).includes(option.value);
  }
  if(item&&semanticKind==='examples'){
    $('example-question').value=item.question;$('example-sql').value=item.description;
    $('example-source').value=item.datasource;
  }
  form.hidden=false;form.scrollIntoView({block:'nearest'});
  (semanticKind==='terms'?$('term-word'):$('example-question')).focus();
}
function renderSemanticItem(item){
  const article=node('article',undefined,'semantic-item');
  const head=node('header');head.append(node('h2',semanticKind==='terms'?item.word:item.question));article.append(head);
  article.append(node(semanticKind==='terms'?'p':'pre',item.description,semanticKind==='terms'?'':'semantic-sql'));
  if(semanticKind==='terms'&&(item.other_words||[]).length)article.append(node('small',item.other_words.join(' · ')));
  if(semanticKind==='examples')article.append(node('small',item.datasource_name||item.datasource));
  article.append(node('small',' · '+t(item.enabled?'enabled':'disabled')));
  const actions=node('div',undefined,'semantic-actions');
  actions.append(semanticAction(t('edit'),()=>openSemanticForm(item)));
  actions.append(semanticAction(t(item.enabled?'disable':'enable'),async()=>{
    try{await api('/agentsql/'+semanticKind+'/'+encodeURIComponent(item.id)+'/enabled',{enabled:!item.enabled});await loadSemantics();}
    catch{semanticStatus(t('failed'),true);}
  }));
  actions.append(semanticAction(t('remove'),async()=>{
    if(!window.confirm(t('confirmDelete')))return;
    try{await api('/agentsql/'+semanticKind+'/'+encodeURIComponent(item.id),undefined,'DELETE');semanticStatus(t('semanticDeleted'));await loadSemantics();}
    catch{semanticStatus(t('failed'),true);}
  }));
  article.append(actions);return article;
}
async function loadSemantics(){
  if(!semanticVisible)return;
  $('semantic-terms').classList.toggle('active',semanticKind==='terms');
  $('semantic-examples').classList.toggle('active',semanticKind==='examples');
  $('semantic-recommendations').classList.toggle('active',semanticKind==='recommendations');
  const recommendations=semanticKind==='recommendations';
  $('semantic-add').hidden=recommendations;$('semantic-search-form').hidden=recommendations;
  $('semantic-list').hidden=recommendations;$('semantic-prev').parentElement.hidden=recommendations;
  $('recommendation-form').hidden=!recommendations;
  if(recommendations){await loadRecommendationSettings();return;}
  recommendationEpoch++;
  try{
    const query=new URLSearchParams({page:String(semanticPage),size:'20',q:$('semantic-search').value.trim()});
    const result=await api('/agentsql/'+semanticKind+'?'+query);
    semanticTotalPages=result.total_pages;
    if(semanticPage>semanticTotalPages&&semanticTotalPages>0){semanticPage=semanticTotalPages;return loadSemantics();}
    const list=$('semantic-list');list.replaceChildren();
    for(const item of result.data||[])list.append(renderSemanticItem(item));
    if(!result.data?.length)list.append(node('p',t('noSemantic'),'semantic-empty'));
    $('semantic-count').textContent=`${semanticPage}/${Math.max(1,semanticTotalPages)} · ${result.total_count} ${t('items')}`;
    $('semantic-prev').disabled=semanticPage<=1;$('semantic-next').disabled=semanticPage>=semanticTotalPages;
  }catch{semanticStatus(t('failed'),true);}
}
async function saveSemantic(event){
  event.preventDefault();
  const save=$('semantic-form').querySelector('button[type=submit]');save.disabled=true;
  try{
    let input;
    if(semanticKind==='terms'){
      input={word:$('term-word').value.trim(),description:$('term-description').value.trim(),
        other_words:$('term-aliases').value.split(',').map(value=>value.trim()).filter(Boolean),
        datasource_ids:[...$('term-sources').selectedOptions].map(option=>option.value),enabled:$('semantic-enabled').checked};
      if(!input.word)throw new Error('term');
    }else{
      input={question:$('example-question').value.trim(),description:$('example-sql').value.trim(),
        datasource:$('example-source').value,enabled:$('semantic-enabled').checked};
      if(!input.question||!input.description||!input.datasource)throw new Error('example');
    }
    if(semanticEditing)input.id=semanticEditing;
    await api('/agentsql/'+semanticKind,input,'PUT');
    hideSemanticForm();semanticStatus(t('semanticSaved'));await loadSemantics();
  }catch{semanticStatus(t('failed'),true);}finally{save.disabled=false;}
}
function closeMenu(){document.documentElement.dataset.menu='closed';$('menu').setAttribute('aria-expanded','false');}
$('menu').onclick=()=>{const visible=document.documentElement.dataset.menu!=='open';document.documentElement.dataset.menu=visible?'open':'closed';$('menu').setAttribute('aria-expanded',String(visible));};
document.addEventListener('keydown',event=>{if(event.key==='Escape')closeMenu();});
async function open(id){if(controller)return;closeMenu();showQA();status(t('reading'));try{const chat=await api('/chat/'+encodeURIComponent(id)+'/with_data');current=String(chat.id);$('datasource').value=String(chat.datasource);$('datasource').disabled=true;void loadSuggestions();show(chat);await history();status('');}catch{status(t('failed'),true);}}
$('login-form').onsubmit=async event=>{event.preventDefault();const button=event.target.querySelector('button');button.disabled=true;$('login-error').textContent='';try{const data=await api('/agentsql/login',{username:$('username').value,password:$('password').value});token=data.access_token;sessionStorage.setItem('agentsql-chatbi-token',token);$('password').value='';await enter();}catch{$('login-error').textContent=t('loginFailed');}finally{button.disabled=false;}};
$('logout').onclick=async()=>{controller?.abort();suggestionEpoch++;try{await api('/login/logout',{});}finally{token='';sessionStorage.removeItem('agentsql-chatbi-token');location.reload();}};
$('account-settings').onclick=()=>{$('account-form').reset();$('account-status').textContent='';$('account-dialog').showModal();};
$('account-close').onclick=()=>$('account-dialog').close();
$('account-form').onsubmit=async event=>{
  event.preventDefault();const submit=$('account-form').querySelector('button[type=submit]');submit.disabled=true;
  $('account-status').textContent='';
  try{
    await api('/user/pwd',{pwd:$('current-password').value,new_pwd:$('new-password').value},'PUT');
    $('account-dialog').close();token='';sessionStorage.removeItem('agentsql-chatbi-token');
    $('workspace').hidden=true;$('login').hidden=false;$('login-tools').hidden=false;$('login-error').textContent=t('passwordChanged');
  }catch{$('account-status').textContent=t('failed');}finally{submit.disabled=false;$('account-form').reset();}
};
$('new-chat').onclick=()=>{if(controller)return;closeMenu();current=null;showQA();$('conversation').replaceChildren();$('welcome').hidden=false;$('datasource').disabled=false;$('question').value='';status('');history().catch(()=>status(t('failed'),true));void loadSuggestions();$('question').focus();};
$('history-search').oninput=()=>renderHistory();
$('datasource').onchange=()=>{void loadSuggestions();};
$('semantics-nav').onclick=()=>{void showSemantics();};
$('users-nav').onclick=()=>{userPage=1;void showUsers();};
$('users-workspace').onchange=()=>{void loadUsers();};
$('users-search-form').onsubmit=event=>{event.preventDefault();userPage=1;void loadUsers();};
$('users-prev').onclick=()=>{if(userPage>1){userPage--;void loadUsers();}};
$('users-next').onclick=()=>{if(userPage<userTotalPages){userPage++;void loadUsers();}};
$('sources-nav').onclick=()=>{void showSources();};
$('dashboards-nav').onclick=()=>{void showDashboards();};
$('source-search-form').onsubmit=event=>{event.preventDefault();renderSourceList();};
$('source-delete').onclick=()=>{void deleteSelectedSource();};
$('import-file').onchange=()=>{clearImportStage();sourceStatus(t('importFileChanged'));};
$('import-preview').onclick=()=>{void previewImport();};
$('source-import-form').onsubmit=event=>{void saveImport(event);};
$('semantic-terms').onclick=()=>{semanticKind='terms';semanticPage=1;$('semantic-search').value='';semanticStatus('');hideSemanticForm();void loadSemantics();};
$('semantic-examples').onclick=()=>{semanticKind='examples';semanticPage=1;$('semantic-search').value='';semanticStatus('');hideSemanticForm();void loadSemantics();};
$('semantic-recommendations').onclick=()=>{semanticKind='recommendations';semanticStatus('');hideSemanticForm();void loadSemantics();};
$('recommendation-source').onchange=()=>{void loadRecommendationSettings();};
$('recommendation-form').onsubmit=event=>{void saveRecommendationSettings(event);};
$('semantic-add').onclick=()=>openSemanticForm();
$('semantic-cancel').onclick=()=>hideSemanticForm();
$('semantic-form').onsubmit=event=>{void saveSemantic(event);};
$('semantic-search-form').onsubmit=event=>{event.preventDefault();semanticPage=1;void loadSemantics();};
$('semantic-prev').onclick=()=>{if(semanticPage>1){semanticPage--;void loadSemantics();}};
$('semantic-next').onclick=()=>{if(semanticPage<semanticTotalPages){semanticPage++;void loadSemantics();}};
$('language').onclick=async()=>{const next=lang==='en'?'zh-CN':'en';if(token){try{await api('/user/language',{language:next},'PUT');}catch{status(t('failed'),true);return;}}lang=next;localStorage.setItem('agentsql-chatbi-language',lang);translate();if(token&&!sourceList.length)status(t('empty'),true);renderHistory();assistants.language();if(dashboard.visible()){dashboard.language();}else if(usersVisible){void loadUsers();}else if(semanticVisible){semanticStatus('');hideSemanticForm();void loadSemantics();}else if(sourceVisible){void showSources();}else if(current&&!assistants.visible())open(current);};
$('theme').onclick=()=>theme(document.documentElement.dataset.theme==='dark'?'light':'dark');
$('login-theme').onclick=$('theme').onclick;
$('login-language').onclick=$('language').onclick;
$('cancel').onclick=()=>controller?.abort();
$('question-form').onsubmit=async event=>{event.preventDefault();if(controller||!$('question').value.trim())return;const question=$('question').value.trim();controller=new AbortController();const signal=controller.signal;$('send').disabled=true;$('cancel').hidden=false;status(t('working'));try{if(!current){const chat=await api('/chat/start',{datasource:$('datasource').value,question,origin:0},'POST',signal);current=String(chat.id);$('datasource').disabled=true;}$('welcome').hidden=true;const response=await fetch(apiRoot+'/chat/question',{method:'POST',headers:{'Content-Type':'application/json','X-SQLBOT-TOKEN':'Bearer '+token},body:JSON.stringify({chat_id:current,question}),signal});if(!response.ok||!response.body)throw new Error('request');const reader=response.body.getReader();const decoder=new TextDecoder();let pending='',finished=false;while(true){const {value,done}=await reader.read();if(done)break;pending+=decoder.decode(value,{stream:true});let boundary;while((boundary=pending.indexOf('\n\n'))>=0){const block=pending.slice(0,boundary);pending=pending.slice(boundary+2);for(const line of block.split('\n')){if(!line.startsWith('data:'))continue;const item=JSON.parse(line.slice(5));if(item.type==='error')throw new Error('request');if(item.type==='finish')finished=true;}}}if(!finished)throw new Error('incomplete');$('question').value='';const chat=await api('/chat/'+encodeURIComponent(current)+'/with_data',undefined,'GET',signal);show(chat);await history();status('');}catch(error){status(error.name==='AbortError'?t('cancelled'):t('failed'),error.name!=='AbortError');}finally{controller=null;$('send').disabled=false;$('cancel').hidden=true;}};
if(token)enter().catch(()=>{status(t('failed'),true);$('workspace').hidden=true;$('login').hidden=false;$('login-tools').hidden=false;});
