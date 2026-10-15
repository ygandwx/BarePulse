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
            } catch (ApiException $e) { $view['traffic_error'] = $e->errorCode . ': ' . $e->getMessage(); }
        } catch (ApiException $e) { $view['error'] = trim($view['error'] . ' ' . $e->errorCode . ': ' . $e->getMessage()); }
        $view['csrf'] = $state['csrf'];
        $view['request_id'] = $state['form_key'];
        return $view;
    }
}
