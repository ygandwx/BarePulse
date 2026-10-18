package app

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// ConsoleReadiness is a database-only preflight, not a device probe or lease.
// POST /console-sessions repeats authorization and binding checks atomically.
type ConsoleReadiness struct {
	ServerID       string   `json:"server_id"`
	Mode           string   `json:"mode"`
	RequestAllowed bool     `json:"request_allowed"`
	QueueExpected  bool     `json:"queue_expected"`
	Code           string   `json:"code"`
	Message        string   `json:"message"`
	Channels       []string `json:"channels"`
}

func (a *App) consoleReadiness(w http.ResponseWriter, r *http.Request, u User) error {
	// The inventory lists owned servers even when their service is suspended.
	// Read only ownership and maintenance here so preflight can explain that
	// restriction; action endpoints still use the stricter service-access check.
	var s Server
	err := a.DB.QueryRow(r.Context(), `SELECT id,maintenance FROM servers WHERE id=$1 AND ($2='admin' OR owner_id=$3)`, r.PathValue("id"), u.Role, u.ID).Scan(&s.ID, &s.Maintenance)
	if err != nil {
		return dbError(err)
	}
	v := ConsoleReadiness{ServerID: s.ID, Mode: "unbound", Code: "CONSOLE_BINDING_REQUIRED", Message: "尚未绑定控制台设备。", Channels: []string{}}
	var kind string
	var port int
	var enabled, occupied bool
	var cooldown *time.Time
	err = a.DB.QueryRow(r.Context(), `SELECT d.kind,b.port,d.enabled,(d.lease_owner IS NOT NULL OR d.blocked_reason<>''),d.cooldown_until FROM bindings b JOIN devices d ON d.id=b.device_id WHERE b.server_id=$1 AND b.kind='console' AND b.active`, s.ID).Scan(&kind, &port, &enabled, &occupied, &cooldown)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if err == nil {
		v.Mode = "real"
		v.Code = "ISOLATION_UNVERIFIED"
		v.Message = "真实控制台尚未开放：已知目标核验契约可离线验证，视频、键鼠、介质和旧连接撤销仍需定制固件证据。"
		if kind == "mock_pikvm" {
			v.Mode = "simulated"
			v.Code = "SIMULATION_READY"
			v.Message = "可申请模拟控制台；模拟画面不代表真实设备视频。"
			v.RequestAllowed = true
			v.QueueExpected = occupied || (cooldown != nil && time.Now().Before(*cooldown))
			v.Channels = []string{"mock_events", "mock_input", "private_media"}
		}
		if !enabled {
			v.RequestAllowed = false
			v.Code = "DEVICE_DISABLED"
			v.Message = "绑定设备已停用。"
		}
		if port < 1 || port > 16 {
			v.RequestAllowed = false
			v.Code = "INVALID_PORT"
			v.Message = "控制台绑定端口无效。"
		}
	}
	var inactive bool
	if err = a.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM service_mappings WHERE server_id=$1 AND state<>'active') AND NOT EXISTS(SELECT 1 FROM service_mappings WHERE server_id=$1 AND state='active')`, s.ID).Scan(&inactive); err != nil {
		return err
	}
	if inactive {
		v.RequestAllowed = false
		v.Code = "SERVICE_INACTIVE"
		v.Message = "服务已暂停或终止，控制台访问不可用。"
	}
	if s.Maintenance {
		v.RequestAllowed = false
		v.Code = "MAINTENANCE"
		v.Message = "服务器处于维护状态。"
	}
	if u.Role == "operator" {
		v.RequestAllowed = false
		v.Code = "FORBIDDEN"
		v.Message = "Operator 为只读角色。"
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonResponse(w, 200, v)
	return nil
}
