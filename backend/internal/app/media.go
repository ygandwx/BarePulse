package app

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) listMedia(w http.ResponseWriter, r *http.Request, u User) error {
	s, e := a.consoleSession(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return e
	}
	rows, e := a.DB.Query(r.Context(), `SELECT id,filename,size_bytes::text,sha256,state FROM media_assets WHERE session_id=$1 AND owner_id=$2 AND state<>'deleted' ORDER BY created_at`, s.ID, u.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, name, size, hash, state string
		if e = rows.Scan(&id, &name, &size, &hash, &state); e != nil {
			return e
		}
		out = append(out, map[string]string{"id": id, "filename": name, "size_bytes": size, "sha256": hash, "state": state})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	jsonResponse(w, 200, out)
	return nil
}
func (a *App) uploadMedia(w http.ResponseWriter, r *http.Request, u User) error {
	s, e := a.verifiedConsole(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return e
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		return fail(415, "CONTENT_TYPE", "Upload raw ISO bytes as application/octet-stream")
	}
	name := r.Header.Get("X-Filename")
	if len(name) < 5 || len(name) > 180 || strings.ContainsAny(name, "/\\\r\n\x00") || !strings.EqualFold(filepath.Ext(name), ".iso") {
		return fail(400, "INVALID_MEDIA", "An ISO filename without paths is required")
	}
	if a.Config.MediaDir == "" {
		return fail(503, "MEDIA_STORAGE_UNAVAILABLE", "Media storage is not configured")
	}
	if e = os.MkdirAll(a.Config.MediaDir, 0700); e != nil {
		return e
	}
	id := newID()
	path := filepath.Join(a.Config.MediaDir, id+".iso")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	saved := false
	defer func() {
		f.Close()
		if !saved {
			os.Remove(path)
		}
	}()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(r.Body, a.Config.MaxMediaBytes+1))
	if e != nil {
		return fail(400, "UPLOAD_INTERRUPTED", "Upload interrupted")
	}
	if n == 0 || n > a.Config.MaxMediaBytes {
		return fail(413, "MEDIA_SIZE", "Media exceeds configured capacity or is empty")
	}
	if e = f.Close(); e != nil {
		return e
	}
	tx, _, e := a.lockConsoleMutation(r, u, s, false)
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	var state string
	if e = tx.QueryRow(r.Context(), `SELECT state FROM console_sessions WHERE id=$1 FOR UPDATE`, s.ID).Scan(&state); e != nil {
		return e
	}
	if state != "active" && state != "protected" {
		return fail(409, "SESSION_EXPIRED", "Session ended during upload")
	}
	var used int64
	if e = tx.QueryRow(r.Context(), `SELECT COALESCE(sum(size_bytes),0)::bigint FROM media_assets WHERE session_id=$1 AND state<>'deleted'`, s.ID).Scan(&used); e != nil {
		return e
	}
	if used+n > a.Config.MaxMediaBytes {
		return fail(413, "MEDIA_QUOTA", "Session media quota exceeded")
	}
	if _, e = tx.Exec(r.Context(), `INSERT INTO media_assets(id,owner_id,session_id,filename,size_bytes,sha256,state) VALUES($1,$2,$3,$4,$5,$6,'ready')`, id, u.ID, s.ID, name, n, hex.EncodeToString(h.Sum(nil))); e != nil {
		return e
	}
	if e = audit(r.Context(), tx, u, "media.upload", id, map[string]any{"bytes": n, "session_id": s.ID}); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	saved = true
	jsonResponse(w, 201, map[string]string{"id": id, "state": "ready"})
	return nil
}
func (a *App) mediaTransaction(r *http.Request, u User) (pgx.Tx, ConsoleSession, error) {
	s, e := a.verifiedConsole(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return nil, s, e
	}
	tx, s, e := a.lockConsoleMutation(r, u, s, false)
	if e != nil {
		return nil, s, e
	}
	var state string
	e = tx.QueryRow(r.Context(), `SELECT state FROM console_sessions WHERE id=$1 FOR UPDATE`, s.ID).Scan(&state)
	if e != nil {
		tx.Rollback(r.Context())
		return nil, s, e
	}
	if state != "active" && state != "protected" {
		tx.Rollback(r.Context())
		return nil, s, fail(409, "SESSION_EXPIRED", "Session ended")
	}
	s.State = state
	return tx, s, nil
}
func (a *App) mountMedia(w http.ResponseWriter, r *http.Request, u User) error {
	tx, s, e := a.mediaTransaction(r, u)
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if s.State == "protected" {
		return fail(409, "PROTECTED_SESSION", "End protected work before replacing media")
	}
	id := r.PathValue("media")
	var state string
	e = tx.QueryRow(r.Context(), `SELECT state FROM media_assets WHERE id=$1 AND session_id=$2 AND owner_id=$3 FOR UPDATE`, id, s.ID, u.ID).Scan(&state)
	if e != nil {
		return dbError(e)
	}
	if state != "ready" {
		return fail(409, "MEDIA_STATE", "Only ready media can be mounted")
	}
	if _, e = os.Stat(filepath.Join(a.Config.MediaDir, id+".iso")); e != nil {
		return fail(409, "MEDIA_MISSING", "Media file is missing; restore or upload again")
	}
	var existing *string
	if e = tx.QueryRow(r.Context(), `SELECT media_id FROM mock_console_state WHERE device_id=$1 AND session_id=$2 FOR UPDATE`, s.DeviceID, s.ID).Scan(&existing); e != nil {
		return e
	}
	if existing != nil {
		return fail(409, "MEDIA_BUSY", "Detach current media first")
	}
	if _, e = tx.Exec(r.Context(), `UPDATE mock_console_state SET media_id=$2 WHERE device_id=$1`, s.DeviceID, id); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), `UPDATE media_assets SET state='mounted' WHERE id=$1`, id); e != nil {
		return e
	}
	if e = audit(r.Context(), tx, u, "media.mount", id, map[string]any{"session_id": s.ID, "simulated": true}); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	jsonResponse(w, 200, map[string]any{"state": "mounted", "simulated": true})
	return nil
}
func (a *App) unmountMedia(w http.ResponseWriter, r *http.Request, u User) error {
	tx, s, e := a.mediaTransaction(r, u)
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if s.State == "protected" {
		return fail(409, "PROTECTED_SESSION", "End protected work before unmounting media")
	}
	id := r.PathValue("media")
	tag, e := tx.Exec(r.Context(), `UPDATE media_assets SET state='ready' WHERE id=$1 AND owner_id=$2 AND session_id=$3 AND state='mounted'`, id, u.ID, s.ID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return fail(404, "NOT_FOUND", "Mounted media not found")
	}
	if _, e = tx.Exec(r.Context(), `UPDATE mock_console_state SET media_id=NULL WHERE device_id=$1 AND session_id=$2 AND media_id=$3`, s.DeviceID, s.ID, id); e != nil {
		return e
	}
	if e = audit(r.Context(), tx, u, "media.unmount", id, map[string]any{"simulated": true}); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	jsonResponse(w, 200, map[string]string{"state": "ready"})
	return nil
}
func (a *App) deleteMedia(w http.ResponseWriter, r *http.Request, u User) error {
	s, e := a.consoleSession(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return e
	}
	tx, _, e := a.lockConsoleAuthority(r, u, s)
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	id := r.PathValue("media")
	var state string
	if e = tx.QueryRow(r.Context(), `SELECT state FROM media_assets WHERE id=$1 AND session_id=$2 AND owner_id=$3 FOR UPDATE`, id, s.ID, u.ID).Scan(&state); e != nil {
		return dbError(e)
	}
	if state == "mounted" {
		return fail(409, "MEDIA_BUSY", "Unmount media first")
	}
	// Remove is confined to a server-generated ID obtained from this row.
	if e = os.Remove(filepath.Join(a.Config.MediaDir, id+".iso")); e != nil && !os.IsNotExist(e) {
		return e
	}
	if _, e = tx.Exec(r.Context(), `UPDATE media_assets SET state='deleted' WHERE id=$1`, id); e != nil {
		return e
	}
	if e = audit(r.Context(), tx, u, "media.delete", id, map[string]string{"session_id": s.ID}); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	jsonResponse(w, 200, map[string]string{"state": "deleted"})
	return nil
}
