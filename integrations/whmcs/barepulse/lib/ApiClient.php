<?php
declare(strict_types=1);

namespace BarePulse;

interface ServiceAPI
{
    public function request(string $method, string $serviceId, string $action, array $data): array;
}

final class ApiException extends \RuntimeException
{
    public string $errorCode;
    public int $httpStatus;

    public function __construct(string $code, string $message, int $status = 0)
    {
        parent::__construct($message);
        $this->errorCode = $code;
        $this->httpStatus = $status;
    }
}

final class ApiClient implements ServiceAPI
{
    private string $baseURL;
    private string $instance;
    private string $secret;
    private $transport;

    public function __construct(string $baseURL, string $instance, string $secret, ?callable $transport = null)
    {
        $baseURL = rtrim($baseURL, '/');
        $url = parse_url($baseURL);
        $loopback = is_array($url) && in_array($url['host'] ?? '', ['127.0.0.1', '[::1]'], true)
            && defined('BAREPULSE_ALLOW_LOOPBACK_TESTS') && BAREPULSE_ALLOW_LOOPBACK_TESTS === true;
        if (!is_array($url) || empty($url['host']) || isset($url['user'], $url['pass'])
            || isset($url['user']) || isset($url['pass']) || isset($url['query']) || isset($url['fragment'])
            || !empty($url['path']) || (($url['scheme'] ?? '') !== 'https' && !($loopback && ($url['scheme'] ?? '') === 'http'))) {
            throw new ApiException('CONFIG_INVALID', 'BarePulse requires an HTTPS origin without credentials, path or query.');
        }
        if (!preg_match('/^[a-zA-Z0-9_-]{1,128}$/D', $instance) || strlen($secret) < 32) {
            throw new ApiException('CONFIG_INVALID', 'Configure the integration ID and a server-side secret of at least 32 bytes.');
        }
        $this->baseURL = $baseURL;
        $this->instance = $instance;
        $this->secret = $secret;
        $this->transport = $transport;
    }

    public static function signature(string $secret, string $method, string $uri, string $timestamp, string $nonce, string $body): string
    {
        return hash_hmac('sha256', $method . "\n" . $uri . "\n" . $timestamp . "\n" . $nonce . "\n" . hash('sha256', $body), $secret);
    }

    public function request(string $method, string $serviceId, string $action, array $data): array
    {
        if (!preg_match('/^[1-9][0-9]{0,17}$/D', $serviceId)
            || !in_array($method, ['GET', 'POST'], true)
            || !in_array($action, ['summary', 'power', 'traffic', 'task', 'power-actions', 'console', 'lifecycle'], true)) {
            throw new ApiException('REQUEST_INVALID', 'Invalid service request.');
        }
        $uri = '/api/v1/whmcs/services/' . $serviceId . '/' . $action;
        $body = '';
        if ($method === 'GET') {
            $uri .= '?' . http_build_query($data, '', '&', PHP_QUERY_RFC3986);
        } else {
            $body = json_encode($data, JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR);
        }
        $timestamp = (string) time();
        $nonce = bin2hex(random_bytes(24));
        $headers = [
            'Content-Type: application/json', 'Accept: application/json',
            'X-BarePulse-Instance: ' . $this->instance,
            'X-BarePulse-Timestamp: ' . $timestamp,
            'X-BarePulse-Nonce: ' . $nonce,
            'X-BarePulse-Signature: ' . self::signature($this->secret, $method, $uri, $timestamp, $nonce, $body),
        ];
        // Deliberately one request. Network errors do not trigger automatic replay.
        $response = $this->transport
            ? ($this->transport)($method, $this->baseURL . $uri, $headers, $body)
            : $this->curl($method, $this->baseURL . $uri, $headers, $body);
        $status = (int) ($response['status'] ?? 0);
        try {
            $result = json_decode((string) ($response['body'] ?? ''), true, 64, JSON_THROW_ON_ERROR | JSON_BIGINT_AS_STRING);
        } catch (\JsonException $e) {
            throw new ApiException('RESPONSE_INVALID', 'BarePulse returned an unreadable response. The action outcome may be unknown.', $status);
        }
        if (!is_array($result)) {
            throw new ApiException('RESPONSE_INVALID', 'BarePulse returned an invalid response.', $status);
        }
        if ($status < 200 || $status > 299 || isset($result['error'])) {
            $code = $result['error']['code'] ?? 'API_ERROR';
            $message = $result['error']['message'] ?? 'BarePulse could not complete this request.';
            throw new ApiException(is_string($code) ? $code : 'API_ERROR', is_string($message) ? $message : 'BarePulse request failed.', $status);
        }
        return $result;
    }

    public function validatePortalURL(string $url): string
    {
        // The short-lived URL must point at this configured platform, not a URL
        // supplied by the browser or a different origin returned by an upstream.
        if (!preg_match('~^' . preg_quote($this->baseURL, '~') . '/portal\.html#[a-f0-9]{48}$~D', $url)) {
            throw new ApiException('PORTAL_INVALID', 'The console response did not contain a valid platform ticket.');
        }
        return $url;
    }

    private function curl(string $method, string $url, array $headers, string $body): array
    {
        if (!function_exists('curl_init')) {
            throw new ApiException('CURL_REQUIRED', 'The server PHP cURL extension is required.');
        }
        $handle = curl_init($url);
        $responseBody = '';
        curl_setopt_array($handle, [
            CURLOPT_CUSTOMREQUEST => $method, CURLOPT_HTTPHEADER => $headers,
            CURLOPT_RETURNTRANSFER => false, CURLOPT_FOLLOWLOCATION => false,
            CURLOPT_CONNECTTIMEOUT => 3, CURLOPT_TIMEOUT => 10,
            CURLOPT_SSL_VERIFYPEER => true, CURLOPT_SSL_VERIFYHOST => 2,
            CURLOPT_PROXY => '',
            CURLOPT_WRITEFUNCTION => static function ($handle, string $chunk) use (&$responseBody): int {
                if (strlen($responseBody) + strlen($chunk) > 1048576) { return 0; }
                $responseBody .= $chunk;
                return strlen($chunk);
            },
        ]);
        if ($method === 'POST') { curl_setopt($handle, CURLOPT_POSTFIELDS, $body); }
        $ok = curl_exec($handle);
        $status = (int) curl_getinfo($handle, CURLINFO_RESPONSE_CODE);
        if ($ok === false) {
            throw new ApiException('PLATFORM_UNAVAILABLE', 'BarePulse is unavailable or timed out. Do not assume the action failed; retry only the same request after checking its task.');
        }
        return ['status' => $status, 'body' => $responseBody];
    }
}
