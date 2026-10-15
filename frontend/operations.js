function el(tag, text, attrs = {}) { const node = document.createElement(tag); if (text !== undefined) node.textContent = text; Object.assign(node, attrs); return node; }
const labels = { power_on: '开机／接通供电', request_shutdown: '请求正常关机', hard_reset: '硬复位（完成需核验）', power_cycle: '断电再送电（供电循环）', enable_port: '管理员启用端口', disable_port: '管理员禁用端口' };
// Keep unchanged DOM rows mounted: polling must not discard a reconciliation
// draft, its focus/checkbox, or an expanded audit event.
function updateRows(container, cache, entries, render) {
  const wanted = new Set(entries.map(item => item.id));
  for (const [id, previous] of cache) if (!wanted.has(id)) { previous.node.remove(); cache.delete(id); }
  entries.forEach((item, index) => {
    const signature = JSON.stringify(item); let row = cache.get(item.id);
    if (!row || row.signature !== signature) {
      const node = render(item);
      if (row) row.node.replaceWith(node);
      row = { node, signature }; cache.set(item.id, row);
    }
    if (container.children[index] !== row.node) container.insertBefore(row.node, container.children[index] || null);
  });
}
export async function mount(root, api, user) {
  let stopped = false; let pendingKey = null; let keyAction = null; let canSubmit = false;
  const taskRows = new Map(); const auditRows = new Map();
  const notice = el('p', '', { role: 'status' }); const form = el('form'); const server = el('select'); const action = el('select');
  const status = el('pre', '选择服务器查看缓存状态；模拟与实机来源分别标注。'); const tasks = el('div'); const audit = el('div'); const submit = el('button', '提交持久电源任务', { type: 'submit', disabled: user.role === 'operator' });
  const sLabel = el('label', '服务器'); sLabel.append(server); const aLabel = el('label', '允许动作'); aLabel.append(action); form.append(sLabel, aLabel, submit);
  root.replaceChildren(el('h2', '电源任务与审计'), el('p', '202 表示已排队。供电 ON 与服务器启动分别显示；超时和未核验结果不会自动重发。'), notice, form, status, el('h3', '任务（最近 200 条）'), tasks, el('h3', '审计（最近 300 条）'), audit);
  async function run(fn) { try { await fn(); } catch (error) { notice.className = 'error'; notice.textContent = `${error.code || 'ERROR'}: ${error.message}`; } }
  async function loadStatus() {
    if (!server.value) return;
    const data = await api(`/api/v1/servers/${server.value}/power`); if (stopped) return;
    const selectedAction = action.value;
    status.textContent = JSON.stringify(data, null, 2); action.replaceChildren(...data.actions.map(v => el('option', labels[v] || v, { value: v })));
    if (data.actions.includes(selectedAction)) action.value = selectedAction;
    canSubmit = user.role !== 'operator' && data.execution_enabled && data.actions.length > 0;
    submit.disabled = !canSubmit;
  }
  const button = (text, fn) => { const b = el('button', text, { type: 'button' }); b.onclick = () => run(fn); return b; };
  async function refresh() {
    const [entries, events] = await Promise.all([api('/api/v1/tasks'), api('/api/v1/audit')]); if (stopped) return;
    updateRows(tasks, taskRows, entries, task => {
      const box = el('article'); box.append(el('strong', `${task.action} — ${task.status}`), el('p', `${task.id} | ${task.created_at}`), el('pre', JSON.stringify(task.result, null, 2)));
      if (task.status === 'queued' && user.role !== 'operator') box.append(button('取消未执行任务', async () => { await api(`/api/v1/tasks/${task.id}`, { method: 'DELETE' }); await refresh(); }));
      if (task.status === 'unknown' && user.role === 'admin' && task.result.simulated) {
        const reconcile = el('form'); const evidence = el('input', undefined, { placeholder: '核验结果、证据及原因', required: true });
        const outcome = el('select'); outcome.append(el('option', '已核验失败', { value: 'failed' }), el('option', '已核验成功', { value: 'succeeded' }));
        const ack = el('input', undefined, { type: 'checkbox', required: true }); const ackLabel = el('label', '确认旧执行者已停止（仅离线模拟核对）'); ackLabel.append(ack);
        reconcile.append(evidence, outcome, ackLabel, el('button', '记录核验并解除模拟设备阻断', { type: 'submit' }));
        reconcile.onsubmit = event => { event.preventDefault(); run(async () => { await api(`/api/v1/tasks/${task.id}/resolve`, { method: 'POST', body: { outcome: outcome.value, reason: evidence.value, executor_stopped: ack.checked } }); await refresh(); await loadStatus(); }); }; box.append(reconcile);
      }
      return box;
    });
    tasks.setAttribute('aria-label', entries.length ? '电源任务列表' : '暂无任务');
    updateRows(audit, auditRows, events, item => { const row = el('details'); row.append(el('summary', `${item.created_at} · ${item.action} · ${item.object_id}`), el('pre', JSON.stringify(item.details, null, 2))); return row; });
  }
  server.onchange = () => { pendingKey = null; run(loadStatus); }; action.onchange = () => { pendingKey = null; };
  form.onsubmit = event => {
    event.preventDefault(); run(async () => {
      const signature = `${server.value}:${action.value}`;
      if (!pendingKey || keyAction !== signature) { pendingKey = crypto.randomUUID(); keyAction = signature; }
      submit.disabled = true;
      try {
        const task = await api(`/api/v1/servers/${server.value}/power-actions`, { method: 'POST', headers: { 'Idempotency-Key': pendingKey }, body: { action: action.value } });
        notice.className = 'notice'; notice.textContent = `任务 ${task.id}：${task.status}。任务已持久化，可刷新查看。`; pendingKey = null; await refresh(); await loadStatus();
      } finally { if (!stopped) submit.disabled = !canSubmit; }
    });
  };
  await run(async () => { const servers = await api('/api/v1/servers'); server.replaceChildren(...servers.map(s => el('option', s.name, { value: s.id }))); await loadStatus(); await refresh(); });
  root.append(button('刷新结果', async () => { await refresh(); await loadStatus(); }));
  const timer = setInterval(() => { if (!stopped && root.isConnected) run(async () => { await refresh(); await loadStatus(); }); }, 3000);
  return () => { stopped = true; clearInterval(timer); };
}
