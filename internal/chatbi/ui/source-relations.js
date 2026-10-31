// The server rebuilds every saved cell from the current gateway schema. This
// editor only assembles table/field identifiers returned by the same API.
export function setupSourceRelations({api,t,node,get,onStatus}) {
  let generation=0, section=null, source=null, tables=[], fields=[], graph=[], edges=[];
  const id=value=>String(value);
  const endpoint=cell=>[id(cell?.cell),id(cell?.port)];
  const current=epoch=>epoch===generation && section?.isConnected && !get('sources').hidden;

  function reset() {
    generation++;
    section?.remove();section=null;source=null;tables=[];fields=[];graph=[];edges=[];
  }

  function render() {
    if(!section)return;
    section.replaceChildren(node('h3',t('relationTitle')),
      node('p',t('relationNote'),'relation-note'));
    const open=node('button',t('relationOpen'));open.type='button';
    open.onclick=()=>{void load(open);};section.append(open);
    if(!fields.length)return;
    const form=node('div',undefined,'relation-form');
    const makeSelect=label=>{
      const wrap=node('label',label),select=node('select');
      for(const field of fields){
        const option=node('option',field.tableName+'.'+field.name);
        option.value=JSON.stringify([field.tableID,field.id]);select.append(option);
      }
      wrap.append(select);form.append(wrap);return select;
    };
    const left=makeSelect(t('relationFrom'));
    const right=makeSelect(t('relationTo'));
    const add=node('button',t('relationAdd'));add.type='button';
    add.onclick=()=>{
      const [sourceTable,sourceField]=JSON.parse(left.value);
      const [targetTable,targetField]=JSON.parse(right.value);
      if(sourceTable===targetTable || edges.some(edge=>{
        const a=endpoint(edge.source),b=endpoint(edge.target);
        return (a[0]===sourceTable&&a[1]===sourceField&&b[0]===targetTable&&b[1]===targetField)||
          (b[0]===sourceTable&&b[1]===sourceField&&a[0]===targetTable&&a[1]===targetField);
      })){onStatus(t('relationInvalid'),true);return;}
      edges.push({shape:'edge',source:{cell:sourceTable,port:sourceField},
        target:{cell:targetTable,port:targetField}});
      render();onStatus('');
    };
    form.append(add);section.append(form);
    const list=node('div',undefined,'relation-list');
    const names=new Map(fields.map(field=>[field.tableID+':'+field.id,field.tableName+'.'+field.name]));
    edges.forEach((edge,index)=>{
      const a=endpoint(edge.source),b=endpoint(edge.target);
      const row=node('div',undefined,'relation-row');
      row.append(node('span',(names.get(a.join(':'))||'?')+' → '+(names.get(b.join(':'))||'?')));
      const remove=node('button',t('relationRemove'));remove.type='button';
      remove.onclick=()=>{edges.splice(index,1);render();};row.append(remove);list.append(row);
    });
    if(!edges.length)list.append(node('p',t('relationEmpty')));
    section.append(list);
    const save=node('button',t('relationSave'),'primary');save.type='button';
    save.onclick=()=>{void persist(save);};section.append(save);
  }

  async function load(button) {
    const epoch=generation,sourceID=id(source.id);
    button.disabled=true;onStatus(t('sourceLoading'));
    try{
      const loaded=await api('/table_relation/get/'+encodeURIComponent(sourceID),{},'POST');
      const available=await Promise.all(tables.map(async table=>{
        const rows=await api('/datasource/fieldList/'+encodeURIComponent(sourceID)+'/'+
          encodeURIComponent(id(table.id)),{fieldName:''},'POST');
        return rows.map(field=>({tableID:id(table.id),tableName:table.table_name,
          id:id(field.id),name:field.field_name}));
      }));
      if(!current(epoch))return;
      graph=loaded;fields=available.flat();edges=graph.filter(cell=>cell.shape==='edge');
      render();onStatus('');
    }catch{if(current(epoch))onStatus(t('failed'),true);}
    finally{button.disabled=false;}
  }

  async function persist(button) {
    const epoch=generation,sourceID=id(source.id);
    const nodes=graph.filter(cell=>cell.shape==='er-rect');
    const present=new Set(nodes.map(cell=>id(cell.id)));
    for(const edge of edges){
      for(const tableID of [id(edge.source.cell),id(edge.target.cell)]){
        if(present.has(tableID))continue;
        const index=nodes.length;
        nodes.push({id:tableID,shape:'er-rect',position:{x:30+(index%4)*220,y:30+Math.floor(index/4)*260}});
        present.add(tableID);
      }
    }
    button.disabled=true;onStatus(t('relationSaving'));
    try{
      await api('/table_relation/save/'+encodeURIComponent(sourceID),[...nodes,...edges],'POST');
      if(!current(epoch))return;
      graph=await api('/table_relation/get/'+encodeURIComponent(sourceID),{},'POST');
      if(!current(epoch))return;
      edges=graph.filter(cell=>cell.shape==='edge');render();onStatus(t('relationSaved'));
    }catch{if(current(epoch))onStatus(t('failed'),true);}
    finally{button.disabled=false;}
  }

  function show(selected,tablesNow) {
    reset();
    if(!tablesNow.length)return;
    source=selected;tables=tablesNow;
    section=node('section',undefined,'source-relations');
    get('source-select-form').after(section);
    if(tablesNow.length>64){section.append(node('p',t('relationTooMany')));return;}
    render();
  }
  return {reset,show,refresh:render};
}
