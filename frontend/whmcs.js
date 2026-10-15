const el = (tag, text) => { const e = document.createElement(tag); if (text !== undefined) e.textContent = text; return e; };
function field(form, title, tag = 'input') {
  const label = el('label', title), input = el(tag); input.setAttribute('aria-label', title);
  label.append(input); form.append(label); return input;
}
function options(select, values) {
  const before = select.value; select.replaceChildren();
  for (const [value, title] of values) { const option = el('option', title); option.value = value; select.append(option); }
  if (values.some(([value]) => value === before)) select.value = before;
}
function button(text, action) { const b = el('button', text); b.type = 'button'; b.onclick = action; return b; }

export async function mount(root, api) {
  let stopped = false, editing = '', integrations = [], servers = [], users = [];
  root.append(el('h1', 'WHMCS 服务映射'), el('p', '按实例、服务、客户和服务器归属独立校验。暂停只撤销平台访问，默认不操作电源或网络。'));
  const error = el('p'); error.setAttribute('role', 'status'); root.append(error);
  const run = async fn => { error.textContent = ''; try { await fn(); await load(); } catch (e) { if (!stopped) { error.className = 'error'; error.textContent = e.message; } } };
  const instanceForm = el('form'); instanceForm.append(el('h2', '登记或编辑实例'));
  const name = field(instanceForm, '实例名称'), ref = field(instanceForm, '密钥环境变量名称'); name.required = ref.required = true; ref.value = 'BAREPULSE_WHMCSTEST_SECRET';
  instanceForm.append(el('p', '密钥留在服务端。修改引用或停用会撤销已签发的门户票据；重新启用不会恢复旧票据。已有装机保护仍保留。'));
  const enabled = field(instanceForm, '允许此实例访问'); enabled.type = 'checkbox'; enabled.checked = true;
  const save = el('button', '登记实例'); save.type = 'submit';
  function clear() { editing = ''; name.value = ''; ref.value = 'BAREPULSE_WHMCSTEST_SECRET'; enabled.checked = true; save.textContent = '登记实例'; }
  instanceForm.append(save, button('清除编辑', clear)); root.append(instanceForm);
  instanceForm.onsubmit = e => { e.preventDefault(); run(async () => {
    const body = { name: name.value, secret_ref: ref.value, enabled: enabled.checked };
    if (editing) await api(`/api/v1/integrations/${editing}`, { method: 'PUT', body });
    else { const created = await api('/api/v1/integrations', { method: 'POST', body: { name: body.name, secret_ref: body.secret_ref } }); if (!body.enabled) await api(`/api/v1/integrations/${created.id}`, { method: 'PUT', body }); }
    clear();
  }); };

  const mappingForm = el('form'); mappingForm.append(el('h2', '绑定现有服务'));
  const instance = field(mappingForm, 'WHMCS 实例', 'select'), owner = field(mappingForm, '所属客户', 'select'), server = field(mappingForm, '客户服务器', 'select');
  const serviceID = field(mappingForm, 'WHMCS 服务 ID'), clientID = field(mappingForm, 'WHMCS 客户 ID');
  for (const input of [serviceID, clientID]) { input.required = true; input.inputMode = 'numeric'; input.pattern = '[1-9][0-9]{0,17}'; }
  function refreshServers() { options(server, servers.filter(s => s.owner_id === owner.value).map(s => [s.id, s.name])); }
  owner.onchange = refreshServers;
  const bind = el('button', '绑定服务'); bind.type = 'submit'; mappingForm.append(bind); root.append(mappingForm);
  mappingForm.onsubmit = e => { e.preventDefault(); run(async () => {
    if (!instance.value || !owner.value || !server.value) throw new Error('先登记实例、客户和该客户所属服务器。');
    await api('/api/v1/service-mappings', { method: 'POST', body: { integration_id: instance.value, service_id: serviceID.value, client_id: clientID.value, owner_id: owner.value, server_id: server.value } });
  }); };
  const list = el('div'); root.append(list);
  async function load() {
    const result = await Promise.all([api('/api/v1/integrations'), api('/api/v1/service-mappings'), api('/api/v1/users'), api('/api/v1/servers')]);
    if (stopped) return;
    [integrations, , users, servers] = result;
    options(instance, integrations.filter(x => x.enabled).map(x => [x.id, x.name]));
    options(owner, users.filter(u => u.role === 'customer').map(u => [u.id, u.email])); refreshServers();
    list.replaceChildren(el('h2', '已登记实例'));
    for (const x of integrations) {
      const card = el('section'); card.className = 'card';
      card.append(el('h3', x.name), el('p', `${x.enabled ? '已启用' : '已停用'} · ${x.secret_ref}`), el('p', `WHMCS 配置使用的实例 ID：${x.id}`));
      card.append(button('编辑实例', () => { editing = x.id; name.value = x.name; ref.value = x.secret_ref; enabled.checked = x.enabled; save.textContent = '保存实例并撤销旧门户票据'; instanceForm.scrollIntoView({ block: 'nearest' }); }), button(x.enabled ? '停用实例' : '启用实例', () => run(() => api(`/api/v1/integrations/${x.id}`, { method: 'PUT', body: { name: x.name, secret_ref: x.secret_ref, enabled: !x.enabled } }))));
      list.append(card);
    }
    list.append(el('h2', '服务绑定'));
    for (const m of result[1]) {
      const card = el('section'); card.className = 'card';
      card.append(el('h3', `${integrations.find(x => x.id === m.integration_id)?.name || m.integration_id} / 服务 ${m.service_id}`), el('p', `客户 ${m.client_id} · ${users.find(u => u.id === m.owner_id)?.email || m.owner_id} · ${servers.find(s => s.id === m.server_id)?.name || m.server_id} · ${m.state}`));
      if (m.state !== 'terminated') for (const [state, label] of [['suspended', '暂停访问'], ['active', '恢复访问'], ['terminated', '终止映射']]) { if (state !== m.state) card.append(button(label, () => run(() => api(`/api/v1/service-mappings/${m.id}/state`, { method: 'POST', body: { state } })))); }
      list.append(card);
    }
  }
  await load(); return () => { stopped = true; };
}
