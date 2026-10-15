<section id="barepulse-service" class="panel panel-default" aria-label="Physical server management">
  <div class="panel-heading"><h3 class="panel-title">BarePulse server management</h3></div>
  <div class="panel-body">
    {if $bp.error}<div class="alert alert-danger" role="alert">{$bp.error|escape:'html'}</div>{/if}
    {if $bp.authorized}
      {if $bp.notice}<div class="alert alert-info" role="status">{$bp.notice|escape:'html'}</div>{/if}
      <h4>{$bp.server_name|escape:'html'}</h4>
      <p>{$bp.server_model|escape:'html'} {if $bp.simulated}<strong>SIMULATION</strong>{/if}</p>
      <dl class="dl-horizontal">
        <dt>Server power</dt><dd>{$bp.power_state|escape:'html'}</dd>
        <dt>Outlet supply</dt><dd>{$bp.supply_state|escape:'html'} — supply does not prove server boot</dd>
        <dt>Data quality</dt><dd>{$bp.quality|escape:'html'}</dd>
        <dt>Observed at</dt><dd>{$bp.observed_at|escape:'html'}</dd>
      </dl>
      <form method="post" action="clientarea.php?action=productdetails" class="barepulse-action-form">
        <input type="hidden" name="id" value="{$bp.service_id|escape:'html'}">
        <input type="hidden" name="token" value="{$token|escape:'html'}">
        <input type="hidden" name="bp_csrf" value="{$bp.csrf|escape:'html'}">
        <input type="hidden" name="bp_request_id" value="{$bp.request_id|escape:'html'}">
        <label for="barepulse-action">Power action</label>
        <select id="barepulse-action" name="bp_action" class="form-control" {if !$bp.execution_enabled}disabled{/if}>
          {foreach $bp.actions as $item}<option value="{$item.action|escape:'html'}">{$item.label|escape:'html'}</option>{/foreach}
        </select>
        <p>Hardware reset and hard power cycle can interrupt writes. Results distinguish command acceptance from verified completion.</p>
        <button class="btn btn-primary" type="submit" {if !$bp.execution_enabled || !$bp.actions}disabled{/if}>Submit power task</button>
      </form>
      <hr>
      <form method="post" action="clientarea.php?action=productdetails" class="barepulse-action-form">
        <input type="hidden" name="id" value="{$bp.service_id|escape:'html'}">
        <input type="hidden" name="token" value="{$token|escape:'html'}">
        <input type="hidden" name="bp_csrf" value="{$bp.csrf|escape:'html'}">
        <input type="hidden" name="bp_request_id" value="{$bp.request_id|escape:'html'}">
        <input type="hidden" name="bp_action" value="console">
        <button class="btn btn-default" type="submit">Request shared console</button>
        <p>Console access is exclusive to the entire KVM. Installation protection survives disconnection and restart.</p>
      </form>
      {if $bp.portal_url}<p><a class="btn btn-success" href="{$bp.portal_url|escape:'html'}" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer">Open console / queue status (one use, 60 seconds)</a></p>{/if}
      {if $bp.task}
        <h4>Latest power task</h4>
        <dl class="dl-horizontal">
          <dt>Task</dt><dd>{$bp.task.id|escape:'html'}</dd>
          <dt>Action / status</dt><dd>{$bp.task.action|escape:'html'} / {$bp.task.status|escape:'html'}</dd>
          <dt>Accepted</dt><dd>{if $bp.task.accepted}Yes{else}No / unknown{/if}</dd>
          <dt>Completed</dt><dd>{if $bp.task.completed}Yes{else}Not verified{/if}</dd>
          <dt>Target reached</dt><dd>{if $bp.task.target_reached}Yes{else}Not verified{/if}</dd>
          <dt>Result</dt><dd>{$bp.task.code|escape:'html'} {$bp.task.message|escape:'html'}</dd>
          <dt>Updated</dt><dd>{$bp.task.updated_at|escape:'html'}</dd>
        </dl>
        {if $bp.task.status == 'unknown'}<p class="alert alert-warning">Outcome unknown. Do not resubmit the physical action. Support must reconcile the existing task.</p>{/if}
      {/if}
      <p><a id="barepulse-refresh" href="{$bp.refresh_url|escape:'html'}" data-poll="{if $bp.poll}1{else}0{/if}">Refresh state and task</a></p>
      <h4>Traffic account</h4>
      {if isset($bp.traffic_error)}<div class="alert alert-warning">{$bp.traffic_error|escape:'html'}</div>{/if}
      {if $bp.traffic}
        <p>Period: {$bp.traffic.period_start|escape:'html'} to {$bp.traffic.period_end|escape:'html'}</p>
        <table class="table"><thead><tr><th>Upload bytes</th><th>Download bytes</th><th>Policy usage bytes</th><th>Quota bytes</th><th>Remaining bytes</th></tr></thead>
          <tbody><tr><td>{$bp.traffic.upload_bytes|escape:'html'}</td><td>{$bp.traffic.download_bytes|escape:'html'}</td><td>{$bp.traffic.used_bytes|escape:'html'}</td><td>{$bp.traffic.effective_quota_bytes|escape:'html'}</td><td>{$bp.traffic.remaining_bytes|escape:'html'}</td></tr></tbody></table>
        <p>Unallocated interval bytes: upload {$bp.traffic.unallocated_upload_bytes|escape:'html'}, download {$bp.traffic.unallocated_download_bytes|escape:'html'}. Unknown intervals are not zero traffic.</p>
        <p>Confirmed intervals: {$bp.traffic.known_intervals|escape:'html'}; intervals with uncertainty: {$bp.traffic.unknown_intervals|escape:'html'}. {$bp.traffic.coverage_note|escape:'html'}</p>
        {if $bp.traffic.over_quota}<p class="alert alert-warning">Quota exceeded. Automatic charging, throttling, network cutoff and power cutoff are disabled.</p>{/if}
      {/if}
    {/if}
  </div>
</section>
{literal}
<script>
(function () {
  var root = document.getElementById('barepulse-service');
  if (!root) return;
  var sending = false;
  root.querySelectorAll('form.barepulse-action-form').forEach(function (form) {
    form.addEventListener('submit', function (event) {
      if (sending) { event.preventDefault(); return; }
      sending = true;
      root.querySelectorAll('button[type=submit]').forEach(function (button) { button.disabled = true; });
    });
  });
  var refresh = document.getElementById('barepulse-refresh');
  if (refresh && refresh.dataset.poll === '1') {
    setTimeout(function () { if (!sending && !document.hidden) window.location.replace(refresh.href); }, 4000);
  }
}());
</script>
{/literal}
