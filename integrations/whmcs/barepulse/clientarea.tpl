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
        <h5>Per-source traffic interval rates</h5>
        <p>Latest sample: {$bp.traffic.chart.latest_sample|escape:'html'}. Upload is blue; download is orange. Each horizontal segment is the measured average for one source interval; overlapping sources are not added together. Grey bands mark unknown intervals. Dashed segments mark recovered gaps or cross-period, unallocated totals, not reconstructed minute-level traffic.</p>
        {if $bp.traffic.chart.available}
          <svg viewBox="0 0 960 250" width="100%" role="img" aria-labelledby="barepulse-traffic-title barepulse-traffic-desc">
            <title id="barepulse-traffic-title">Upload and download interval average rates</title>
            <desc id="barepulse-traffic-desc">Only returned measured intervals are plotted. Empty space is missing coverage, not zero. The table below gives exact bytes and quality.</desc>
            <rect x="50" y="30" width="860" height="170" fill="#fff" stroke="#666"/>
            {foreach $bp.traffic.chart.intervals as $point}
              {if $point.kind == 'unknown'}
                <rect x="{$point.x1|escape:'html'}" y="30" width="{$point.width|escape:'html'}" height="170" fill="#64748b" fill-opacity="0.25"><title>{$point.source|escape:'html'}: {$point.quality|escape:'html'} — bytes unknown</title></rect>
              {/if}
            {/foreach}
            {foreach $bp.traffic.chart.intervals as $point}
              {if $point.kind != 'unknown'}
                <line x1="{$point.x1|escape:'html'}" x2="{$point.x2|escape:'html'}" y1="{$point.upload_y|escape:'html'}" y2="{$point.upload_y|escape:'html'}" stroke="#1763a6" stroke-width="3" {if $point.kind == 'gap'}stroke-dasharray="7 4"{/if}><title>{$point.source|escape:'html'}: {$point.start|escape:'html'} to {$point.end|escape:'html'}; upload {$point.upload_bytes|escape:'html'} bytes; {$point.rate_label|escape:'html'}; {$point.quality|escape:'html'}</title></line>
                <line x1="{$point.x1|escape:'html'}" x2="{$point.x2|escape:'html'}" y1="{$point.download_y|escape:'html'}" y2="{$point.download_y|escape:'html'}" stroke="#b45309" stroke-width="2" {if $point.kind == 'gap'}stroke-dasharray="7 4"{/if}><title>{$point.source|escape:'html'}: {$point.start|escape:'html'} to {$point.end|escape:'html'}; download {$point.download_bytes|escape:'html'} bytes; {$point.rate_label|escape:'html'}; {$point.quality|escape:'html'}</title></line>
              {/if}
            {/foreach}
            {foreach $bp.traffic.chart.samples as $sample}
              <circle cx="{$sample.x|escape:'html'}" cy="214" r="3" fill="{if $sample.error}#b91c1c{else}#475569{/if}"><title>Sample {$sample.observed_at|escape:'html'}; {$sample.source|escape:'html'}; {$sample.quality|escape:'html'} {$sample.error|escape:'html'}</title></circle>
            {/foreach}
            <text x="50" y="18" font-size="12">Top scale: {$bp.traffic.chart.max_bps|escape:'html'} bps</text>
            <text x="22" y="202" font-size="12">0</text>
            <text x="50" y="240" font-size="12">{$bp.traffic.chart.start|escape:'html'}</text>
            <text x="910" y="240" text-anchor="end" font-size="12">{$bp.traffic.chart.end|escape:'html'}</text>
          </svg>
          <p>Dots below the chart are samples; red dots indicate collection errors. Rates are approximate for display; byte counts below stay exact. At most {$bp.traffic.chart.limit|escape:'html'} recent intervals and 100 samples are plotted. Omitted intervals: {$bp.traffic.chart.omitted|escape:'html'}; invalid records: {$bp.traffic.chart.invalid|escape:'html'}.</p>
          <div class="table-responsive"><table class="table"><caption>Most recent 20 source intervals. Recovered gaps may be billed as whole interval totals; observations and unallocated intervals are not billed.</caption><thead><tr><th>Source / time</th><th>Upload bytes</th><th>Download bytes</th><th>Quality / accounting</th></tr></thead><tbody>
            {foreach $bp.traffic.chart.rows as $point}<tr><td>{$point.source|escape:'html'}<br>{$point.start|escape:'html'} → {$point.end|escape:'html'}</td><td>{$point.upload_bytes|escape:'html'}</td><td>{$point.download_bytes|escape:'html'}</td><td>{$point.quality|escape:'html'} / {if $point.billable}billed{else}not billed{/if}</td></tr>{/foreach}
          </tbody></table></div>
        {else}
          <p role="status">No valid traffic intervals or samples are available. Usage coverage is unknown; no zero-traffic chart is inferred. Invalid records: {$bp.traffic.chart.invalid|escape:'html'}.</p>
        {/if}
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
