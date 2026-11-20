package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"mcs/internal/logbus"
	"mcs/internal/sipserver"
	"mcs/internal/store"
	"mcs/internal/webui"
)

type Server struct {
	store   *store.Store
	cdr     *store.CDRStore
	alarms  *store.AlarmStore
	sip     *sipserver.Server
	dataDir string
	db      *store.DB
	logs    *logbus.Hub
	version string
	started time.Time

	consoleUser string
	consoleHash []byte
	sessMu      sync.Mutex
	sessions    map[string]time.Time
}

// WithDB enables persistence for runtime settings such as TTS.
func WithDB(db *store.DB) Option {
	return func(s *Server) { s.db = db }
}

// WithLogHub exposes the in-memory log ring and live subscribers.
func WithLogHub(hub *logbus.Hub) Option {
	return func(s *Server) { s.logs = hub }
}

func New(st *store.Store, cdr *store.CDRStore, alarms *store.AlarmStore, sip *sipserver.Server, dataDir, version string, opts ...Option) *Server {
	s := &Server{store: st, cdr: cdr, alarms: alarms, sip: sip, dataDir: dataDir, version: version, started: time.Now()}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/login", s.login)
	mux.HandleFunc("POST /api/v1/logout", s.logout)
	mux.HandleFunc("GET /api/v1/session", s.sessionInfo)
	mux.HandleFunc("GET /api/v1/devices", s.listDevices)
	mux.HandleFunc("POST /api/v1/devices", s.deviceCreate)
	mux.HandleFunc("PATCH /api/v1/devices/{ext}", s.deviceUpdate)
	mux.HandleFunc("DELETE /api/v1/devices/{ext}", s.deviceDelete)
	mux.HandleFunc("GET /api/v1/registrations", s.listDevices)
	mux.HandleFunc("GET /api/v1/calls/active", s.activeCalls)
	mux.HandleFunc("POST /api/v1/intercom/{call_id}/unlock", s.intercomUnlock)
	mux.HandleFunc("POST /api/v1/webrtc/offer", s.webrtcOffer)
	mux.HandleFunc("GET /api/v1/devices/{ext}/snapshot", s.deviceSnapshot)
	mux.HandleFunc("GET /api/v1/ws", s.ws)
	mux.HandleFunc("POST /api/v1/alarm/test", s.testAlarm)
	mux.HandleFunc("GET /api/v1/cdr", s.cdrList)
	mux.HandleFunc("GET /api/v1/broadcast/zones", s.broadcastZones)
	mux.HandleFunc("GET /api/v1/broadcast/active", s.broadcastActive)
	mux.HandleFunc("GET /api/v1/broadcast/sessions", s.broadcastSessions)
	mux.HandleFunc("GET /api/v1/broadcast/schedules", s.scheduleList)
	mux.HandleFunc("POST /api/v1/broadcast/schedules", s.scheduleAdd)
	mux.HandleFunc("PATCH /api/v1/broadcast/schedules/{id}", s.scheduleUpdate)
	mux.HandleFunc("DELETE /api/v1/broadcast/schedules/{id}", s.scheduleDelete)
	mux.HandleFunc("POST /api/v1/broadcast/start", s.broadcastStart)
	mux.HandleFunc("POST /api/v1/broadcast/stop", s.broadcastStop)
	mux.HandleFunc("GET /api/v1/media", s.mediaList)
	mux.HandleFunc("POST /api/v1/media", s.mediaUpload)
	mux.HandleFunc("DELETE /api/v1/media/{id}", s.mediaDelete)
	mux.HandleFunc("GET /api/v1/media/{id}/download", s.mediaDownload)
	mux.HandleFunc("GET /api/v1/alarms", s.alarmList)
	mux.HandleFunc("GET /api/v1/alarms/{id}", s.alarmGet)
	mux.HandleFunc("POST /api/v1/alarms/{id}/close", s.alarmClose)
	mux.HandleFunc("GET /api/v1/logs", s.logRecent)
	mux.HandleFunc("GET /api/v1/logs/ws", s.logsWS)
	mux.HandleFunc("GET /api/v1/healthz", s.health)
	mux.HandleFunc("GET /api/v1/system/info", s.info)
	mux.HandleFunc("GET /api/v1/tts/config", s.ttsConfig)
	mux.HandleFunc("PUT /api/v1/tts/config", s.ttsUpdate)
	mux.HandleFunc("POST /api/v1/tts/test", s.ttsTest)
	mux.Handle("/recordings/", http.StripPrefix("/recordings/", http.FileServer(http.Dir(filepath.Join(s.dataDir, "recordings")))))
	mux.Handle("/", webui.Handler())
	return logRequests(s.authMiddleware(mux))
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	items := s.store.List()
	total, online := s.store.Count()
	writeJSON(w, 200, map[string]any{
		"items":  items,
		"total":  total,
		"online": online,
	})
}

func (s *Server) deviceCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ext      string `json:"ext"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Zone     string `json:"zone"`
		Snapshot string `json:"snapshot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	if err := s.store.UpsertDevice(req.Ext, req.Name, req.Password, req.Zone, req.Snapshot); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	dev, _ := s.store.Get(req.Ext)
	writeJSON(w, 201, dev)
}

func (s *Server) deviceUpdate(w http.ResponseWriter, r *http.Request) {
	ext := r.PathValue("ext")
	cur, ok := s.store.Get(ext)
	if !ok {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "device not found"})
		return
	}
	var req struct {
		Name     *string `json:"name"`
		Password *string `json:"password"`
		Zone     *string `json:"zone"`
		Snapshot *string `json:"snapshot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	name, password, zone, snapshot := cur.Name, "", cur.Zone, cur.Snapshot
	if req.Name != nil {
		name = *req.Name
	}
	if req.Password != nil {
		password = *req.Password
	}
	if req.Zone != nil {
		zone = *req.Zone
	}
	if req.Snapshot != nil {
		snapshot = *req.Snapshot
	}
	if err := s.store.UpsertDevice(ext, name, password, zone, snapshot); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	dev, _ := s.store.Get(ext)
	writeJSON(w, 200, dev)
}

func (s *Server) deviceDelete(w http.ResponseWriter, r *http.Request) {
	ext := r.PathValue("ext")
	if !s.store.DeleteDevice(ext) {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "device not found"})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "deleted"})
}

func (s *Server) webrtcOffer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CallID string `json:"call_id"`
		SDP    string `json:"sdp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CallID == "" || req.SDP == "" {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "call_id and sdp are required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	answer, err := s.sip.HandleWebRTCOffer(ctx, req.CallID, req.SDP)
	if err != nil {
		writeJSON(w, 409, map[string]any{"code": "WEBRTC_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"type": "answer", "sdp": answer})
}

func (s *Server) intercomUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	if err := s.sip.UnlockCall(ctx, r.PathValue("call_id"), req.Code); err != nil {
		writeJSON(w, 409, map[string]any{"code": "UNLOCK_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok"})
}

var snapshotClient = &http.Client{Timeout: 5 * time.Second}

func (s *Server) deviceSnapshot(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.store.Get(r.PathValue("ext"))
	if !ok {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "device not found"})
		return
	}
	if dev.Snapshot == "" {
		writeJSON(w, 404, map[string]any{"code": "NO_SNAPSHOT", "message": "device has no snapshot url"})
		return
	}
	u, err := url.Parse(dev.Snapshot)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		writeJSON(w, 502, map[string]any{"code": "BAD_SNAPSHOT_URL", "message": "snapshot url must be http(s)"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, dev.Snapshot, nil)
	if err != nil {
		writeJSON(w, 502, map[string]any{"code": "SNAPSHOT_FAILED", "message": err.Error()})
		return
	}
	res, err := snapshotClient.Do(req)
	if err != nil {
		writeJSON(w, 502, map[string]any{"code": "SNAPSHOT_FAILED", "message": err.Error()})
		return
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		writeJSON(w, 502, map[string]any{"code": "SNAPSHOT_FAILED", "message": res.Status})
		return
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 5<<20+1))
	if err != nil || len(data) > 5<<20 {
		writeJSON(w, 502, map[string]any{"code": "SNAPSHOT_FAILED", "message": "invalid snapshot response"})
		return
	}
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Server) activeCalls(w http.ResponseWriter, r *http.Request) {
	items := s.sip.ActiveCalls()
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) mediaList(w http.ResponseWriter, r *http.Request) {
	items := s.sip.ListMedia()
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) mediaUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "multipart form required"})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "file is required"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 20<<20+1))
	if err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	if len(data) > 20<<20 {
		writeJSON(w, 413, map[string]any{"code": "TOO_LARGE", "message": "file is larger than 20MB"})
		return
	}
	name := r.FormValue("name")
	if name == "" {
		name = header.Filename
	}
	item, err := s.sip.AddMedia(name, data)
	if err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 201, item)
}

func (s *Server) mediaDelete(w http.ResponseWriter, r *http.Request) {
	if !s.sip.DeleteMedia(r.PathValue("id")) {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "media not found"})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "deleted"})
}

func (s *Server) mediaDownload(w http.ResponseWriter, r *http.Request) {
	item, data, ok := s.sip.MediaData(r.PathValue("id"))
	if !ok {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "media not found"})
		return
	}
	ct, ext := "audio/wav", ".wav"
	if item.Format == "mp3" {
		ct, ext = "audio/mpeg", ".mp3"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "attachment; filename="+item.ID+ext)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Server) broadcastZones(w http.ResponseWriter, r *http.Request) {
	items := s.sip.BroadcastZones()
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) broadcastActive(w http.ResponseWriter, r *http.Request) {
	items := s.sip.BroadcastSessions(true)
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) broadcastSessions(w http.ResponseWriter, r *http.Request) {
	items := s.sip.BroadcastSessions(false)
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) scheduleList(w http.ResponseWriter, r *http.Request) {
	items := s.sip.ListSchedules()
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) scheduleAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		ZoneID   string `json:"zone_id"`
		Text     string `json:"text"`
		Voice    string `json:"voice"`
		MediaID  string `json:"media_id"`
		Priority int    `json:"priority"`
		Cron     string `json:"cron"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	sch, err := s.sip.AddSchedule(sipserver.Schedule{
		Name: req.Name, ZoneID: req.ZoneID, Text: req.Text, Voice: req.Voice,
		MediaID: req.MediaID, Priority: req.Priority, Cron: req.Cron, Enabled: enabled,
	})
	if err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 201, sch)
}

func (s *Server) scheduleUpdate(w http.ResponseWriter, r *http.Request) {
	var p sipserver.SchedulePatch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	sch, ok, err := s.sip.UpdateSchedule(r.PathValue("id"), p)
	if !ok {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "schedule not found"})
		return
	}
	if err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 200, sch)
}

func (s *Server) scheduleDelete(w http.ResponseWriter, r *http.Request) {
	if !s.sip.DeleteSchedule(r.PathValue("id")) {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "schedule not found"})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "deleted"})
}

func (s *Server) broadcastStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ZoneID   string `json:"zone_id"`
		Text     string `json:"text"`
		Voice    string `json:"voice"`
		MediaID  string `json:"media_id"`
		Priority int    `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	if req.ZoneID == "" {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "zone_id is required"})
		return
	}
	view, err := s.sip.StartBroadcast(r.Context(), sipserver.BroadcastRequest{
		ZoneID: req.ZoneID, Text: req.Text, Voice: req.Voice, MediaID: req.MediaID, Priority: req.Priority,
	})
	if err != nil {
		status := 502
		switch {
		case errors.Is(err, sipserver.ErrZoneBusy):
			status = 409
		case errors.Is(err, sipserver.ErrNoEndpoints):
			status = 503
		}
		writeJSON(w, status, map[string]any{"code": "BROADCAST_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 202, view)
}

func (s *Server) broadcastStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ZoneID string `json:"zone_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ZoneID == "" {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "zone_id is required"})
		return
	}
	view, ok := s.sip.StopBroadcast(req.ZoneID)
	if !ok {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "no active broadcast in zone"})
		return
	}
	writeJSON(w, 200, view)
}

func (s *Server) alarmList(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	items := s.alarms.List(limit)
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) alarmGet(w http.ResponseWriter, r *http.Request) {
	a, ok := s.alarms.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "alarm not found"})
		return
	}
	writeJSON(w, 200, a)
}

func (s *Server) alarmClose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Disposition string `json:"disposition"`
		Note        string `json:"note"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	a, ok := s.sip.CloseAlarm(r.PathValue("id"), req.Disposition, req.Note)
	if !ok {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "alarm not found"})
		return
	}
	writeJSON(w, 200, a)
}

func (s *Server) cdrList(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	items := s.cdr.List(limit)
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) testAlarm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.sip.EmitTestAlarm(req.From, req.To)
	writeJSON(w, 202, map[string]any{"status": "accepted"})
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	ch, unsubscribe := s.sip.Subscribe()
	defer unsubscribe()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = conn.Write(wctx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) logRecent(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		writeJSON(w, 503, map[string]any{"code": "UNAVAILABLE", "message": "log hub not enabled"})
		return
	}
	items := s.logs.Recent()
	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > len(items) {
		limit = len(items)
	}
	writeJSON(w, 200, map[string]any{"items": items[len(items)-limit:], "total": len(items)})
}

func (s *Server) logsWS(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		http.Error(w, "log hub not enabled", http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	ch, unsubscribe := s.logs.Subscribe()
	defer unsubscribe()

	ctx := r.Context()
	write := func(v any) bool {
		payload, err := json.Marshal(v)
		if err != nil {
			return true
		}
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = conn.Write(wctx, websocket.MessageText, payload)
		cancel()
		return err == nil
	}
	if !write(map[string]any{"type": "history", "items": s.logs.Recent()}) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if !write(map[string]any{"type": "entry", "entry": e}) {
				return
			}
		}
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "ok"})
}

type ttsConfigView struct {
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	VoiceZH   string `json:"voice_zh"`
	VoiceEN   string `json:"voice_en"`
	TimeoutS  int    `json:"timeout_s"`
	HasAPIKey bool   `json:"has_api_key"`
}

func (s *Server) ttsConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.sip.TTSConfig()
	writeJSON(w, 200, ttsConfigView{
		Provider: cfg.Provider, BaseURL: cfg.BaseURL, Model: cfg.Model,
		VoiceZH: cfg.VoiceZH, VoiceEN: cfg.VoiceEN, TimeoutS: cfg.TimeoutS,
		HasAPIKey: cfg.APIKey != "",
	})
}

func (s *Server) ttsUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider    string `json:"provider"`
		BaseURL     string `json:"base_url"`
		APIKey      string `json:"api_key"`
		Model       string `json:"model"`
		VoiceZH     string `json:"voice_zh"`
		VoiceEN     string `json:"voice_en"`
		TimeoutS    int    `json:"timeout_s"`
		ClearAPIKey bool   `json:"clear_api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	cfg := s.sip.TTSConfig()
	if req.Provider != "" {
		cfg.Provider = req.Provider
	}
	if req.BaseURL != "" {
		cfg.BaseURL = req.BaseURL
	}
	if req.Model != "" || req.Provider != "" {
		cfg.Model = req.Model
	}
	if req.VoiceZH != "" {
		cfg.VoiceZH = req.VoiceZH
	}
	if req.VoiceEN != "" {
		cfg.VoiceEN = req.VoiceEN
	}
	if req.TimeoutS > 0 {
		cfg.TimeoutS = req.TimeoutS
	}
	if req.ClearAPIKey {
		cfg.APIKey = ""
	} else if req.APIKey != "" {
		cfg.APIKey = req.APIKey
	}
	if err := s.sip.SetTTS(cfg); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	if s.db != nil {
		if raw, err := json.Marshal(cfg); err == nil {
			_ = s.db.SetSetting("tts", string(raw))
		}
	}
	writeJSON(w, 200, ttsConfigView{
		Provider: cfg.Provider, BaseURL: cfg.BaseURL, Model: cfg.Model,
		VoiceZH: cfg.VoiceZH, VoiceEN: cfg.VoiceEN, TimeoutS: cfg.TimeoutS,
		HasAPIKey: cfg.APIKey != "",
	})
}

func (s *Server) ttsTest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	data, err := s.sip.TTSClient().Synthesize(ctx, "TTS 测试", "")
	if err != nil {
		writeJSON(w, 502, map[string]any{"code": "TTS_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "bytes": len(data)})
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	total, online := s.store.Count()
	writeJSON(w, 200, map[string]any{
		"name":      "MCS",
		"version":   s.version,
		"uptime_s":  int(time.Since(s.started).Seconds()),
		"devices":   total,
		"online":    online,
		"transcode": s.sip.MediaTranscode(),
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
