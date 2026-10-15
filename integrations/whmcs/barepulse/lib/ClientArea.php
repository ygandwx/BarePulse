<?php
declare(strict_types=1);

namespace BarePulse;

final class ClientArea
{
    private ServiceAPI $api;
    private $portalValidator;

    public function __construct(ServiceAPI $api, callable $portalValidator)
    {
        $this->api = $api;
        $this->portalValidator = $portalValidator;
    }

    public function render(string $serviceId, string $clientId, string $method, array $post, array &$session): array
    {
        $key = $clientId . ':' . $serviceId;
        if (!isset($session['barepulse'][$key])) {
            $session['barepulse'][$key] = ['csrf' => bin2hex(random_bytes(32)), 'form_key' => bin2hex(random_bytes(16)), 'submissions' => []];
        }
        $state =& $session['barepulse'][$key];
        $view = ['error' => '', 'notice' => '', 'portal_url' => '', 'server_name' => '', 'server_model' => '',
            'power_state' => 'unknown', 'supply_state' => 'unknown', 'quality' => 'not_observed', 'observed_at' => '',
            'simulated' => false, 'execution_enabled' => false, 'actions' => [], 'task' => null, 'traffic' => null,
            'poll' => false, 'service_id' => $serviceId, 'refresh_url' => 'clientarea.php?action=productdetails&id=' . $serviceId];
        if ($method === 'POST' && isset($post['bp_action'])) {
            if (!is_string($post['bp_csrf'] ?? null) || !hash_equals($state['csrf'], $post['bp_csrf'])) {
                throw new ApiException('CSRF_INVALID', 'This form expired. Reload the service page before submitting.');
            }
            $action = $post['bp_action'];
            if (!is_string($action) || !in_array($action, ['power_on', 'request_shutdown', 'hard_reset', 'power_cycle', 'console'], true)) {
                throw new ApiException('ACTION_UNSUPPORTED', 'This action is not available to customers.');
            }
            $requestId = $post['bp_request_id'] ?? '';
            if (!is_string($requestId) || !preg_match('/^[a-f0-9]{32}$/D', $requestId)
                || (!hash_equals($state['form_key'], $requestId) && !isset($state['submissions'][$requestId]))) {
                throw new ApiException('REQUEST_INVALID', 'Unknown or expired request. Reload the service page.');
            }
            $previous = $state['submissions'][$requestId] ?? null;
            if ($previous && $previous['action'] !== $action) {
                throw new ApiException('IDEMPOTENCY_CONFLICT', 'This request was already used for a different action.');
            }
            try {
                if ($action === 'console') {
                    // A console ticket is one-use and is never retained in session.
                    if ($previous) { throw new ApiException('TICKET_ALREADY_ISSUED', 'This console request was already submitted. Refresh and request a new ticket.'); }
                    $state['submissions'][$requestId] = ['action' => $action];
                    $response = $this->api->request('POST', $serviceId, 'console', ['client_id' => $clientId]);
                    $view['portal_url'] = ($this->portalValidator)((string) ($response['portal_url'] ?? ''));
                    $view['notice'] = 'Console request accepted. Open the one-use link within 60 seconds; it shows queue or protection status.';
                } elseif (!empty($previous['task_id'])) {
                    $state['last_task'] = $previous['task_id'];
                    $view['notice'] = 'This request was already accepted; showing the original task.';
                } else {
                    // Reserve before I/O. If the reply is lost, retain this exact
                    // action/key so a user retry cannot create another task.
                    $state['submissions'][$requestId] = ['action' => $action];
                    $task = $this->api->request('POST', $serviceId, 'power-actions', [
                        'client_id' => $clientId, 'action' => $action, 'idempotency_key' => 'whmcs-' . $requestId,
                    ]);
                    if (!is_string($task['id'] ?? null) || !preg_match('/^[a-f0-9]{48}$/D', $task['id'])) {
                        throw new ApiException('TASK_INVALID', 'The platform reply did not identify a task. Preserve this request key and contact support.');
                    }
                    $state['submissions'][$requestId]['task_id'] = $task['id'];
                    $state['last_task'] = $task['id'];
                    $view['notice'] = 'Power task accepted. Acceptance does not mean the device action completed.';
                }
                $state['form_key'] = bin2hex(random_bytes(16));
            } catch (ApiException $e) {
                $view['error'] = $e->errorCode . ': ' . $e->getMessage();
                if ($action === 'console') { $state['form_key'] = bin2hex(random_bytes(16)); }
            }
            if (count($state['submissions']) > 32) { array_shift($state['submissions']); }
        }
        try {
            $summary = $this->api->request('GET', $serviceId, 'summary', ['client_id' => $clientId]);
            $view['server_name'] = (string) ($summary['server']['name'] ?? '');
            $view['server_model'] = (string) ($summary['server']['model'] ?? '');
            $power = $this->api->request('GET', $serviceId, 'power', ['client_id' => $clientId]);
            foreach (['power_state', 'supply_state', 'quality', 'observed_at'] as $field) { $view[$field] = (string) ($power[$field] ?? $view[$field]); }
            $view['simulated'] = ($power['simulated'] ?? false) === true;
            $view['execution_enabled'] = ($power['execution_enabled'] ?? false) === true;
            $labels = ['power_on' => 'Power on', 'request_shutdown' => 'Request shutdown', 'hard_reset' => 'Hardware reset', 'power_cycle' => 'Hard power cycle'];
            foreach ($power['actions'] ?? [] as $action) { if (isset($labels[$action])) { $view['actions'][] = ['action' => $action, 'label' => $labels[$action]]; } }
            $taskId = $state['last_task'] ?? ($power['last_task']['id'] ?? null);
            if (is_string($taskId) && preg_match('/^[a-f0-9]{48}$/D', $taskId)) {
                $task = $this->api->request('GET', $serviceId, 'task', ['client_id' => $clientId, 'task_id' => $taskId]);
                $view['task'] = ['id' => (string) ($task['id'] ?? ''), 'status' => (string) ($task['status'] ?? 'unknown'),
                    'action' => (string) ($task['action'] ?? ''), 'updated_at' => (string) ($task['updated_at'] ?? ''),
                    'code' => (string) ($task['result']['code'] ?? ''), 'message' => (string) ($task['result']['message'] ?? ''),
                    'accepted' => ($task['result']['accepted'] ?? false) === true,
                    'completed' => ($task['result']['completed'] ?? false) === true,
                    'target_reached' => ($task['result']['target_reached'] ?? false) === true];
                $view['poll'] = in_array($view['task']['status'], ['queued', 'running', 'verifying'], true);
            }
            try {
                $traffic = $this->api->request('GET', $serviceId, 'traffic', ['client_id' => $clientId]);
                $view['traffic'] = [];
                foreach (['upload_bytes', 'download_bytes', 'used_bytes', 'effective_quota_bytes', 'remaining_bytes', 'period_start', 'period_end', 'unallocated_upload_bytes', 'unallocated_download_bytes', 'known_intervals', 'unknown_intervals', 'coverage_note'] as $field) {
                    $view['traffic'][$field] = (string) ($traffic[$field] ?? 'unknown');
                }
                $view['traffic']['over_quota'] = ($traffic['over_quota'] ?? false) === true;
                $view['traffic']['chart'] = self::trafficChart($traffic);
            } catch (ApiException $e) { $view['traffic_error'] = $e->errorCode . ': ' . $e->getMessage(); }
        } catch (ApiException $e) { $view['error'] = trim($view['error'] . ' ' . $e->errorCode . ': ' . $e->getMessage()); }
        $view['csrf'] = $state['csrf'];
        $view['request_id'] = $state['form_key'];
        return $view;
    }

    /** Coordinates are presentation-only floats; byte labels remain exact strings. */
    private static function trafficChart(array $traffic): array
    {
        $chart = ['available' => false, 'intervals' => [], 'samples' => [], 'rows' => [],
            'start' => '', 'end' => '', 'latest_sample' => 'No samples reported', 'max_bps' => 'unknown',
            'omitted' => 0, 'invalid' => 0, 'limit' => 200];
        $input = is_array($traffic['intervals'] ?? null) ? $traffic['intervals'] : [];
        $chart['omitted'] = max(0, count($input) - 2000);
        $intervals = [];
        foreach (array_slice($input, 0, 2000) as $item) {
            if (!is_array($item)) { ++$chart['invalid']; continue; }
            $start = self::trafficTimestamp($item['starts_at'] ?? null);
            $end = self::trafficTimestamp($item['ends_at'] ?? null);
            if ($start === null || $end === null || $end <= $start) { ++$chart['invalid']; continue; }
            $quality = self::trafficCode($item['quality'] ?? null);
            $up = $item['upload_bytes'] ?? null;
            $down = $item['download_bytes'] ?? null;
            $known = in_array($quality, ['normal', 'recovered_gap', 'boundary_unallocated'], true)
                && self::trafficBytes($up) && self::trafficBytes($down);
            if (!$known && in_array($quality, ['normal', 'recovered_gap', 'boundary_unallocated'], true)) {
                $quality = 'invalid_counter'; ++$chart['invalid'];
            }
            $intervals[] = ['from' => $start, 'to' => $end, 'start' => $item['starts_at'], 'end' => $item['ends_at'],
                'source' => self::trafficCode($item['source_id'] ?? null), 'quality' => $quality,
                'kind' => $known ? ($quality === 'normal' ? 'normal' : 'gap') : 'unknown',
                'upload_bytes' => $known ? $up : 'unknown', 'download_bytes' => $known ? $down : 'unknown',
                'upload_bps' => $known ? ((float) $up * 8 / ($end - $start)) : null,
                'download_bps' => $known ? ((float) $down * 8 / ($end - $start)) : null,
                'billable' => $known && $quality !== 'boundary_unallocated' && ($item['billable'] ?? false) === true];
        }
        usort($intervals, static fn($a, $b) => $b['to'] <=> $a['to']);
        $chart['omitted'] += max(0, count($intervals) - $chart['limit']);
        $intervals = array_slice($intervals, 0, $chart['limit']);
        $samples = [];
        foreach (array_slice(is_array($traffic['samples'] ?? null) ? $traffic['samples'] : [], 0, 100) as $item) {
            if (!is_array($item)) { ++$chart['invalid']; continue; }
            $time = self::trafficTimestamp($item['observed_at'] ?? null);
            if ($time === null) { ++$chart['invalid']; continue; }
            $samples[] = ['at' => $time, 'observed_at' => $item['observed_at'],
                'source' => self::trafficCode($item['source_id'] ?? null),
                'quality' => self::trafficCode($item['quality'] ?? null),
                'error' => isset($item['error_code']) && $item['error_code'] !== '' ? self::trafficCode($item['error_code']) : ''];
        }
        usort($samples, static fn($a, $b) => $b['at'] <=> $a['at']);
        if ($samples) { $chart['latest_sample'] = $samples[0]['observed_at']; }
        if (!$intervals && !$samples) { return $chart; }
        $times = array_merge(array_column($intervals, 'from'), array_column($intervals, 'to'), array_column($samples, 'at'));
        $first = min($times); $last = max($times);
        $span = max(1.0, $last - $first);
        $max = 0.0;
        foreach ($intervals as $item) {
            $max = max($max, $item['upload_bps'] ?? 0.0, $item['download_bps'] ?? 0.0);
        }
        $scale = max(1.0, $max);
        $chart['available'] = true;
        $chart['start'] = gmdate('Y-m-d H:i:s', (int) $first) . ' UTC';
        $chart['end'] = gmdate('Y-m-d H:i:s', (int) $last) . ' UTC';
        $chart['max_bps'] = $max > 0 ? sprintf('%.3g', $scale) : ($intervals && array_filter($intervals, static fn($i) => $i['kind'] !== 'unknown') ? '1 (all measured rates are zero)' : 'unknown');
        foreach ($intervals as $item) {
            $item['x1'] = self::trafficCoordinate(50 + 860 * ($item['from'] - $first) / $span);
            $item['x2'] = self::trafficCoordinate(50 + 860 * ($item['to'] - $first) / $span);
            $item['width'] = self::trafficCoordinate(max(0.5, 860 * ($item['to'] - $item['from']) / $span));
            $item['upload_y'] = self::trafficCoordinate(200 - 170 * ($item['upload_bps'] ?? 0) / $scale);
            $item['download_y'] = self::trafficCoordinate(200 - 170 * ($item['download_bps'] ?? 0) / $scale);
            $item['rate_label'] = $item['kind'] === 'unknown' ? 'unknown' : sprintf('upload %.3g / download %.3g bps (interval averages)', $item['upload_bps'], $item['download_bps']);
            unset($item['from'], $item['to'], $item['upload_bps'], $item['download_bps']);
            $chart['intervals'][] = $item;
        }
        $chart['rows'] = array_slice($chart['intervals'], 0, 20);
        foreach ($samples as $sample) {
            $sample['x'] = self::trafficCoordinate(50 + 860 * ($sample['at'] - $first) / $span);
            unset($sample['at']);
            $chart['samples'][] = $sample;
        }
        return $chart;
    }

    private static function trafficBytes($value): bool
    {
        return is_string($value) && preg_match('/^(0|[1-9][0-9]{0,29})$/D', $value) === 1;
    }

    private static function trafficCode($value): string
    {
        return is_string($value) && preg_match('/^[a-zA-Z0-9_-]{1,64}$/D', $value) === 1 ? $value : 'unknown';
    }

    private static function trafficTimestamp($value): ?float
    {
        if (!is_string($value) || preg_match('/^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/D', $value, $matches) !== 1) { return null; }
        $zone = $matches[3] === 'Z' ? '+00:00' : $matches[3];
        $date = \DateTimeImmutable::createFromFormat('!Y-m-d\TH:i:sP', $matches[1] . $zone);
        $errors = \DateTimeImmutable::getLastErrors();
        if ($date === false || ($errors !== false && ($errors['warning_count'] || $errors['error_count']))) { return null; }
        return (float) $date->getTimestamp() + (float) ('0.' . ($matches[2] ?? '0'));
    }

    private static function trafficCoordinate(float $value): string
    {
        return number_format(max(0.0, min(960.0, $value)), 2, '.', '');
    }
}
