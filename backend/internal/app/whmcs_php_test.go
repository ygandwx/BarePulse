package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This exercises the shipped PHP client against the real Go handler and PG
// schema. A PHP stub server cannot catch drift in Go IDs, routes or HMAC input.
func TestWHMCSPHPRealHandlerContract(t *testing.T) {
	php := os.Getenv("PHP_BIN")
	explicitPHP := php != ""
	if php == "" {
		php = "php"
	}
	php, err := exec.LookPath(php)
	if err != nil {
		if explicitPHP {
			t.Fatalf("Configured PHP_BIN is unavailable: %v", err)
		}
		t.Skip("NOT_RUN: PHP executable unavailable; install PHP with cURL or set PHP_BIN")
	}
	php, err = filepath.Abs(php)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{}
	if ext := os.Getenv("PHP_EXTENSION_DIR"); ext != "" {
		ext, err = filepath.Abs(ext)
		if err != nil {
			t.Fatal(err)
		}
		args = append(args, "-d", "extension_dir="+ext, "-d", "extension=curl")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, php, append(append([]string{}, args...), "-r", `if (!extension_loaded('curl')) { fwrite(STDERR, 'PHP cURL extension required'); exit(1); }`)...)
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("PHP exists but its cURL prerequisite failed: %v: %s", err, output)
	}
	a := testApp(t)
	admin, owner, server, instance, _, secret := setupWHMCS(t, a)
	const quota = "900719925474099312345"
	account := decodeTest[TrafficAccount](t, testRequest(t, a, "POST", "/api/v1/traffic/accounts", map[string]any{
		"name": "PHP cross-language quota", "owner_id": owner.ID, "quota_bytes": quota,
		"direction": "both", "timezone": "UTC", "cycle_day": 1, "server_ids": []string{server.ID},
	}, admin), 201)
	host := httptest.NewUnstartedServer(a.Handler())
	a.Config.BaseURL = "http://" + host.Listener.Addr().String()
	host.Start()
	defer host.Close()
	input, err := json.Marshal(map[string]string{
		"origin": host.URL, "instance": instance, "secret": secret,
		"service_id": "101", "client_id": "201", "server_id": server.ID,
		"account_id": account.ID, "quota": quota,
	})
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../../../integrations/whmcs/tests/platform_contract.php")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, php, append(args, script)...)
	// Fixture credentials go through stdin, never command-line arguments or logs.
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("PHP to real Go contract failed: %v: %s", err, output)
	}
	var report struct {
		Assertions int    `json:"assertions"`
		TaskID     string `json:"task_id"`
		ConsoleID  string `json:"console_id"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("Invalid PHP contract report: %v: %s", err, output)
	}
	if report.Assertions < 15 || len(report.TaskID) != 48 || len(report.ConsoleID) != 48 {
		t.Fatalf("Incomplete PHP contract report: %+v", report)
	}
	var tasks, consoles int
	if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM operation_tasks WHERE server_id=$1`, server.ID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM console_sessions WHERE server_id=$1 AND id=$2`, server.ID, report.ConsoleID).Scan(&consoles); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || consoles != 1 {
		t.Fatalf("Cross-language idempotency/scoping failed: tasks=%d consoles=%d", tasks, consoles)
	}
	t.Logf("PASS: %d PHP assertions against real Go HTTP handler and PostgreSQL; one task and one console; no device worker executed", report.Assertions)
}
