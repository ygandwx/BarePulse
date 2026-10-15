function element(tag, text, attrs = {}) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  Object.assign(node, attrs);
  return node;
}
function field(form, label, name, type = 'text', options = []) {
  const wrap = element('label', label);
  let input;
  if (type === 'select') {
    input = element('select');
    for (const [value, title] of options) input.append(element('option', title, { value }));
  } else input = element('input', undefined, { type });
  input.name = name;
  if (type === 'number') { input.min = '1'; input.step = '1'; }
  wrap.append(input); form.append(wrap); return input;
}
function table(container, headers, rows) {
  const grid = element('table'); const head = element('thead'); const tr = element('tr');
  for (const h of headers) tr.append(element('th', h)); head.append(tr); grid.append(head);
  const body = element('tbody');
  for (const cells of rows) { const row = element('tr'); for (const value of cells) { const cell = element('td'); if (value instanceof Node) cell.append(value); else cell.textContent = String(value ?? ''); row.append(cell); } body.append(row); }
  grid.append(body); container.append(grid);
}
export async function mount(root, api, user) {
  let disposed = false;
  const admin = user.role === 'admin';
  const message = element('p', '', { role: 'status', className: 'notice' });
  const content = element('div'); root.replaceChildren(element('h2', '资产、设备与端口绑定'), message, content);
  const run = async (fn) => { message.textContent = ''; try { await fn(); } catch (error) { message.textContent = `${error.code || 'ERROR'}: ${error.message}`; message.className = 'error'; } };
  async function refresh() {
    const [servers, bindings, devices, users] = await Promise.all([
      api('/api/v1/servers'), api('/api/v1/bindings'), admin ? api('/api/v1/devices') : [], admin ? api('/api/v1/users') : []
    ]);
    if (disposed) return;
    content.replaceChildren();
    const button = (label, action) => { const b = element('button', label, { type: 'button' }); b.onclick = () => run(action); return b; };
    const remove = (path) => async () => { await api(path, { method: 'DELETE' }); await refresh(); message.textContent = '已保存。历史关联资产不能物理删除，请结束绑定或设置维护状态。'; };
    const serverSection = element('section'); serverSection.append(element('h3', '服务器')); content.append(serverSection);
    const ownerNames = new Map(users.map(u => [u.id, u.email]));
    let serverForm, sName, sOwner, sModel, sNotes, sMaintenance, sSubmit, sEdit = null;
    table(serverSection, ['名称', '归属', '型号／备注', '维护', '版本', '操作'], servers.map(s => {
      const controls = element('div');
      if (admin) {
        controls.append(button('编辑', () => { sEdit = s; sName.value = s.name; sOwner.value = s.owner_id; sModel.value = s.model; sNotes.value = s.notes; sMaintenance.checked = s.maintenance; sSubmit.textContent = '保存服务器'; serverForm.scrollIntoView({ block: 'nearest' }); }), button('删除无关联资产', remove(`/api/v1/servers/${s.id}`)));
      }
      return [s.name, ownerNames.get(s.owner_id) || s.owner_id, `${s.model} ${s.notes}`, s.maintenance ? '是' : '否', s.version, controls];
    }));
    if (admin) {
      serverForm = element('form'); sName = field(serverForm, '服务器名称', 'name'); sName.required = true;
      sOwner = field(serverForm, '所属用户', 'owner_id', 'select', users.map(u => [u.id, `${u.email} (${u.role})`]));
      sModel = field(serverForm, '型号', 'model'); sNotes = field(serverForm, '硬件／机房／IP／供电路径备注', 'notes');
      sMaintenance = field(serverForm, '维护锁', 'maintenance', 'checkbox');
      sSubmit = element('button', '新增服务器', { type: 'submit' }); serverForm.append(sSubmit, button('清除编辑', () => { sEdit = null; serverForm.reset(); sSubmit.textContent = '新增服务器'; }));
      serverForm.onsubmit = (event) => { event.preventDefault(); run(async () => { await api(sEdit ? `/api/v1/servers/${sEdit.id}` : '/api/v1/servers', { method: sEdit ? 'PUT' : 'POST', body: { name: sName.value, owner_id: sOwner.value, model: sModel.value, notes: sNotes.value, maintenance: sMaintenance.checked, version: sEdit?.version || 0 } }); await refresh(); message.textContent = '服务器已保存。'; }); };
      serverSection.append(serverForm);
      const deviceSection = element('section'); deviceSection.append(element('h3', '设备'), element('p', '真实设备默认禁用；登记地址和刷新页面不会访问设备。实机动作需要另行授权及完整的受控配置。模拟设备可选择故障场景。')); content.append(deviceSection);
      let dEdit = null;
      const form = element('form'); const name = field(form, '设备名称', 'name'); name.required = true;
      const kind = field(form, '类型', 'kind', 'select', ['mock_pikvm', 'mock_pdu', 'mock_bmc', 'pikvm', 'apc', 'redfish', 'ipmi', 'snmp'].map(v => [v, v]));
      const address = field(form, '管理地址（仅登记）', 'address'); const credential = field(form, '凭据引用（非密码）', 'credential_ref');
      const scenario = field(form, '模拟场景', 'scenario', 'select', ['success', 'offline', 'inner_error', 'timeout', 'busy'].map(v => [v, v]));
      const enabled = field(form, '启用', 'enabled', 'checkbox'); enabled.checked = true;
      const submit = element('button', '新增设备', { type: 'submit' }); form.append(submit, button('清除编辑', () => { dEdit = null; form.reset(); enabled.checked = true; submit.textContent = '新增设备'; }));
      table(deviceSection, ['名称', '类型', '模拟场景', '启用', '占用／阻断／冷却', '操作'], devices.map(d => [d.name, d.kind, d.scenario, d.enabled ? '是' : '否', `${d.lease_kind || ''} ${d.blocked_reason || ''} ${d.cooldown_until || ''}`, (() => { const box = element('div'); box.append(button('编辑', () => { dEdit = d; name.value = d.name; kind.value = d.kind; address.value = d.address || ''; credential.value = ''; scenario.value = d.scenario; enabled.checked = d.enabled; submit.textContent = '保存设备'; form.scrollIntoView({ block: 'nearest' }); }), button('删除无关联设备', remove(`/api/v1/devices/${d.id}`))); return box; })()]));
      form.onsubmit = event => { event.preventDefault(); run(async () => { await api(dEdit ? `/api/v1/devices/${dEdit.id}` : '/api/v1/devices', { method: dEdit ? 'PUT' : 'POST', body: { name: name.value, kind: kind.value, address: address.value, credential_ref: credential.value, scenario: scenario.value, enabled: enabled.checked, version: dEdit?.version || 0 } }); await refresh(); message.textContent = '设备已保存。'; }); }; deviceSection.append(form);
    }
    const bindingSection = element('section'); bindingSection.append(element('h3', '端口绑定与历史')); content.append(bindingSection);
    const serverNames = new Map(servers.map(s => [s.id, s.name])); const deviceNames = new Map(devices.map(d => [d.id, d.name]));
    table(bindingSection, ['服务器', '设备', '用途', '端口', '版本', '生效／结束', '操作'], bindings.map(b => [serverNames.get(b.server_id) || b.server_id, deviceNames.get(b.device_id) || b.device_id, b.kind, b.port, b.version, `${b.created_at} → ${b.ended_at || '有效'}`, admin && b.active ? button('结束绑定', remove(`/api/v1/bindings/${b.id}`)) : '']));
    if (admin) {
      const form = element('form'); const server = field(form, '服务器', 'server_id', 'select', servers.map(s => [s.id, s.name])); const device = field(form, '设备', 'device_id', 'select', devices.map(d => [d.id, `${d.name} (${d.kind})`]));
      const kind = field(form, '用途', 'kind', 'select', [['power', '电源'], ['console', '控制台'], ['network', '网络']]);
      const port = field(form, '端口（PiKVM 1–16，BMC 为 1）', 'port', 'number'); port.value = '1'; port.required = true;
      const replacement = field(form, '替换已有绑定（保留历史）', 'replace_id', 'select', [['', '新增'], ...bindings.filter(b => b.active).map(b => [b.id, `${serverNames.get(b.server_id)} / ${b.kind} / ${deviceNames.get(b.device_id)}:${b.port}`])]);
      form.append(element('button', '保存绑定', { type: 'submit' })); form.onsubmit = event => { event.preventDefault(); run(async () => { const old = bindings.find(b => b.id === replacement.value); await api('/api/v1/bindings', { method: 'POST', body: { server_id: server.value, device_id: device.value, kind: kind.value, port: Number(port.value), replace_id: replacement.value, version: old?.version || 0 } }); await refresh(); message.textContent = '绑定已保存；旧排队任务会复核版本。'; }); }; bindingSection.append(form);
      const section = element('section'); section.append(element('h3', '用户与角色')); content.append(section);
      let edit = null; const userForm = element('form'); const email = field(userForm, '邮箱', 'email', 'email'); email.required = true;
      const role = field(userForm, '角色', 'role', 'select', [['customer', '客户'], ['operator', '只读运维'], ['admin', '管理员']]);
      const password = field(userForm, '新密码（12–72 字节；编辑留空保留）', 'password', 'password'); password.autocomplete = 'new-password';
      const save = element('button', '新增用户', { type: 'submit' }); userForm.append(save, button('清除编辑', () => { edit = null; userForm.reset(); save.textContent = '新增用户'; }));
      table(section, ['邮箱', '角色', '操作'], users.map(u => [u.email, u.role, (() => { const box = element('div'); box.append(button('编辑', () => { edit = u; email.value = u.email; role.value = u.role; password.value = ''; save.textContent = '保存用户并注销其会话'; }), button('删除无关联用户', remove(`/api/v1/users/${u.id}`))); return box; })()]));
      userForm.onsubmit = event => { event.preventDefault(); run(async () => { await api(edit ? `/api/v1/users/${edit.id}` : '/api/v1/users', { method: edit ? 'PUT' : 'POST', body: { email: email.value, role: role.value, password: password.value } }); await refresh(); message.textContent = '用户已保存。编辑账户的既有会话已撤销。'; }); }; section.append(userForm);
    }
    content.append(button('刷新资产', refresh));
  }
  await run(refresh);
  return () => { disposed = true; };
}
