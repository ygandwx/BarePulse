const el=(tag,text)=>{const e=document.createElement(tag);if(text)e.textContent=text;return e};
export async function mount(root,api){
 root.append(el('h1','WHMCS 服务映射'),el('p','集成密钥仅存服务端环境变量；请求按实例、服务、客户和服务器归属独立校验。暂停只撤销平台访问，默认不操作电源或网络。'));
 const error=el('p'),list=el('div');root.append(error);
 const run=async fn=>{error.textContent='';try{await fn();await load()}catch(e){error.className='error';error.textContent=e.message}};
 function form(title,fields,submit){const f=el('form');f.append(el('h2',title));for(const [name,label,value]of fields){const l=el('label',label),i=el('input');i.name=name;i.required=true;i.value=value||'';l.append(i);f.append(l)}const b=el('button','保存');b.type='submit';f.append(b);f.onsubmit=e=>{e.preventDefault();run(()=>submit(Object.fromEntries(new FormData(f))))};root.append(f);return f}
 form('登记实例',[['name','实例名称'],['secret_ref','密钥环境变量名称','BAREPULSE_WHMCSTEST_SECRET']],b=>api('/api/v1/integrations',{method:'POST',body:b}));
 form('绑定现有服务',[['integration_id','实例 ID'],['service_id','WHMCS 服务 ID'],['client_id','WHMCS 客户 ID'],['owner_id','BarePulse 用户 ID'],['server_id','BarePulse 服务器 ID']],b=>api('/api/v1/service-mappings',{method:'POST',body:b}));root.append(list);
 async function load(){list.replaceChildren();list.append(el('h2','已登记实例'));const integrations=await api('/api/v1/integrations');for(const x of integrations)list.append(el('pre',JSON.stringify(x,null,2)));list.append(el('h2','服务绑定'));for(const m of await api('/api/v1/service-mappings')){const card=el('section');card.className='card';card.append(el('pre',JSON.stringify(m,null,2)));for(const [state,label]of [['suspended','暂停访问'],['active','恢复访问'],['terminated','终止映射']]){const b=el('button',label);b.onclick=()=>run(()=>api(`/api/v1/service-mappings/${m.id}/state`,{method:'POST',body:{state}}));card.append(b)}list.append(card)}}await load();
}
