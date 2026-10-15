package drivers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NetworkPolicy defaults to no networking, including reads. Real access requires
// an exact enrolled host, HTTPS, explicit write permission and external approval.
// AllowLoopback is only for offline contract servers; it permits literal loopback
// IPs and never follows redirects, proxies or hostnames that may re-resolve.
type NetworkPolicy struct {
	AllowReal     bool
	AllowWrite    bool
	AllowLoopback bool
	AllowedHosts  []string
}

func (p NetworkPolicy) validate(address string, write bool) (*url.URL, error) {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid enrolled device address")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := ip != nil && ip.IsLoopback()
	if loopback && p.AllowLoopback {
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, errors.New("unsupported scheme")
		}
	} else {
		if !p.AllowReal || u.Scheme != "https" {
			return nil, errors.New("real device access disabled or insecure")
		}
		allowed := false
		for _, host := range p.AllowedHosts {
			if u.Host == host {
				allowed = true
			}
		}
		if !allowed {
			return nil, errors.New("device is not enrolled in allowed hosts")
		}
	}
	if write && !p.AllowWrite {
		return nil, errors.New("device writes disabled")
	}
	return u, nil
}

type httpAdapter struct {
	policy NetworkPolicy
	client *http.Client
}

func newHTTP(policy NetworkPolicy, timeout time.Duration) httpAdapter {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return httpAdapter{policy: policy, client: &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type response struct {
	data   []byte
	status int
}

func (h httpAdapter) request(ctx context.Context, t Target, method, path string, payload any, pikvm bool) (response, error) {
	u, err := h.policy.validate(t.Address, method != "GET")
	if err != nil {
		return response{}, err
	}
	ref, err := url.Parse(path)
	if err != nil || ref.IsAbs() || ref.Host != "" || !strings.HasPrefix(ref.Path, "/") || ref.Fragment != "" {
		return response{}, errors.New("invalid fixed device path")
	}
	u.Path = ref.Path
	u.RawPath = ref.RawPath
	u.RawQuery = ref.RawQuery
	var body io.Reader
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return response{}, e
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if pikvm {
		req.Header.Set("X-KVMD-User", t.Username)
		req.Header.Set("X-KVMD-Passwd", t.Password)
	} else {
		req.SetBasicAuth(t.Username, t.Password)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return response{status: resp.StatusCode}, err
	}
	if len(b) > 1<<20 {
		return response{status: resp.StatusCode}, errors.New("device response too large")
	}
	return response{data: b, status: resp.StatusCode}, nil
}

func httpFailure(r response, err error, write bool) Result {
	if err != nil {
		if write {
			return unknown()
		}
		return outcome("failed", "DEVICE_UNREACHABLE", "Device request failed; state is unavailable")
	}
	switch r.status {
	case 401, 403:
		return outcome("failed", "DEVICE_AUTH_FAILED", "Device authentication failed")
	case 409:
		return outcome("failed", "DEVICE_BUSY", "Device is busy")
	default:
		if write && r.status >= 500 {
			return unknown()
		}
		return outcome("failed", "DEVICE_HTTP_ERROR", "Device returned an unsuccessful HTTP status")
	}
}
