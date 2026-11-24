package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"mcs/internal/config"
	"mcs/internal/license"
	"mcs/internal/logbus"
	"mcs/internal/sipserver"
	"mcs/internal/store"
	"mcs/internal/webui"
)

type Server struct {
	store          *store.Store
	cdr            *store.CDRStore
	alarms         *store.AlarmStore
	sip            *sipserver.Server
	license        *license.Manager
	serviceMode    bool
	dataDir        string
	db             *store.DB
	logs           *logbus.Hub
	activeSettings config.ServerSettings
	version        string
	started        time.Time

	consoleUser string
	consoleHash []byte
	sessMu      sync.Mutex
	sessions    map[string]consoleSession
}

// WithDB enables persistence for runtime settings such as TTS.
func WithDB(db *store.DB) Option {
	return func(s *Server) { s.db = db }
}

// WithLogHub exposes the in-memory log ring and live subscribers.
func WithLogHub(hub *logbus.Hub) Option {
	return func(s *Server) { s.logs = hub }
}

// WithServiceMode tells the restart endpoint that a Windows service manager
// is responsible for starting a fresh process.
func WithServiceMode(v bool) Option {
	return func(s *Server) { s.serviceMode = v }
}

// WithLicense attaches the runtime license manager.
func WithLicense(m *license.Manager) Option {
	return func(s *Server) { s.license = m }
}

// WithServerSettings passes the effective startup settings to the API.
func WithServerSettings(v config.ServerSettings) Option {
	return func(s *Server) { s.activeSettings = v }
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
	mux.HandleFunc("GET /api/v1/users", s.usersList)
	mux.HandleFunc("POST /api/v1/users", s.userCreate)
	mux.HandleFunc("PATCH /api/v1/users/{username}", s.userUpdate)
	mux.HandleFunc("DELETE /api/v1/users/{username}", s.userDelete)
	mux.HandleFunc("POST /api/v1/me/password", s.mePassword)
	mux.HandleFunc("GET /api/v1/devices", s.listDevices)
	mux.HandleFunc("POST /api/v1/devices", s.deviceCreate)
	mux.HandleFunc("PATCH /api/v1/devices/{ext}", s.deviceUpdate)
	mux.HandleFunc("DELETE /api/v1/devices/{ext}", s.deviceDelete)
	mux.HandleFunc("GET /api/v1/registrations", s.listDevices)
	mux.HandleFunc("GET /api/v1/calls/active", s.activeCalls)
	mux.HandleFunc("GET /api/v1/license", s.licenseGet)
	mux.HandleFunc("POST /api/v1/license/activate", s.licenseActivate)
	mux.HandleFunc("GET /api/v1/conference", s.conferenceList)
	mux.HandleFunc("GET /api/v1/conference/audio", s.conferenceAudioGet)
	mux.HandleFunc("PUT /api/v1/conference/audio", s.conferenceAudioSave)
	mux.HandleFunc("POST /api/v1/conference/rooms", s.conferenceRoomCreate)
	mux.HandleFunc("PATCH /api/v1/conference/rooms/{number}", s.conferenceRoomUpdate)
	mux.HandleFunc("DELETE /api/v1/conference/rooms/{number}", s.conferenceRoomDelete)
	mux.HandleFunc("POST /api/v1/conference/{room}/webrtc", s.conferenceWebRTC)
	mux.HandleFunc("POST /api/v1/conference/{room}/participants/{id}/kick", s.conferenceKick)
	mux.HandleFunc("POST /api/v1/conference/{room}/participants/{id}/mute", s.conferenceMute)
	mux.HandleFunc("POST /api/v1/intercom/{call_id}/unlock", s.intercomUnlock)
	mux.HandleFunc("POST /api/v1/webrtc/offer", s.webrtcOffer)
	mux.HandleFunc("GET /api/v1/devices/{ext}/snapshot", s.deviceSnapshot)
	mux.HandleFunc("GET /api/v1/ws", s.ws)
	mux.HandleFunc("POST /api/v1/alarm/test", s.testAlarm)
	mux.HandleFunc("GET /api/v1/cdr", s.cdrList)
	mux.HandleFunc("GET /api/v1/broadcast/zones", s.broadcastZones)
	mux.HandleFunc("POST /api/v1/broadcast/zones", s.zoneCreate)
	mux.HandleFunc("PATCH /api/v1/broadcast/zones/{id}", s.zoneUpdate)
	mux.HandleFunc("DELETE /api/v1/broadcast/zones/{id}", s.zoneDelete)
	mux.HandleFunc("GET /api/v1/broadcast/active", s.broadcastActive)
	mux.HandleFunc("GET /api/v1/broadcast/sessions", s.broadcastSessions)
	mux.HandleFunc("GET /api/v1/broadcast/schedules", s.scheduleList)
	mux.HandleFunc("POST /api/v1/broadcast/schedules", s.scheduleAdd)
	mux.HandleFunc("PATCH /api/v1/broadcast/schedules/{id}", s.scheduleUpdate)
	mux.HandleFunc("DELETE /api/v1/broadcast/schedules/{id}", s.scheduleDelete)
	mux.HandleFunc("POST /api/v1/broadcast/start", s.broadcastStart)
	mux.HandleFunc("POST /api/v1/broadcast/stop", s.broadcastStop)
	mux.HandleFunc("GET /api/v1/paging", s.pagingInfo)
	mux.HandleFunc("GET /api/v1/paging/events", s.pagingEvents)
	mux.HandleFunc("POST /api/v1/paging/webrtc", s.pagingWebRTC)
	mux.HandleFunc("POST /api/v1/paging/{id}/stop", s.pagingStop)
	mux.HandleFunc("GET /api/v1/media", s.mediaList)
	mux.HandleFunc("POST /api/v1/media", s.mediaUpload)
	mux.HandleFunc("DELETE /api/v1/media/{id}", s.mediaDelete)
	mux.HandleFunc("GET /api/v1/media/{id}/download", s.mediaDownload)
	mux.HandleFunc("GET /api/v1/alarms", s.alarmList)
	mux.HandleFunc("GET /api/v1/alarms/{id}", s.alarmGet)
	mux.HandleFunc("POST /api/v1/alarms/{id}/close", s.alarmClose)
	mux.HandleFunc("GET /api/v1/system/settings", s.systemSettings)
	mux.HandleFunc("PUT /api/v1/system/settings", s.systemSettingsSave)
	mux.HandleFunc("POST /api/v1/system/settings/rollback", s.systemSettingsRollback)
	mux.HandleFunc("GET /api/v1/system/interfaces", s.systemInterfaces)
	mux.HandleFunc("POST /api/v1/system/restart", s.systemRestart)
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
	if _, exists := s.store.Get(req.Ext); !exists && s.license != nil {
		if max := s.license.MaxPhones(); max > 0 {
			total, _ := s.store.Count()
			if total >= max {
				writeJSON(w, 403, map[string]any{
					"code":    "LICENSE_LIMIT",
					"message": fmt.Sprintf("授权话机数量已达上限（%d 台），请激活更多授权", max),
				})
				return
			}
		}
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
		CallID   string `json:"call_id"`
		SDP      string `json:"sdp"`
		Talkback bool   `json:"talkback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CallID == "" || req.SDP == "" {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "call_id and sdp are required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	answer, err := s.sip.HandleWebRTCOffer(ctx, req.CallID, req.SDP, req.Talkback)
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

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !s.authEnabled() {
		return true
	}
	sess, ok := s.sessionFor(r)
	if !ok || sess.role != "admin" {
		writeJSON(w, 403, map[string]any{"code": "FORBIDDEN", "message": "admin role required"})
		return false
	}
	return true
}

func (s *Server) zoneActive(id string) bool {
	for _, v := range s.sip.BroadcastSessions(true) {
		if v.ZoneID == id {
			return true
		}
	}
	return false
}

func (s *Server) persistZones() {
	if s.db == nil {
		return
	}
	if raw, err := json.Marshal(s.sip.ZoneConfigs()); err == nil {
		_ = s.db.SetSetting("zones", string(raw))
	}
}

func (s *Server) zoneCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var z config.ZoneConfig
	if err := json.NewDecoder(r.Body).Decode(&z); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	zones := s.sip.ZoneConfigs()
	zones = append(zones, z)
	if err := s.sip.SetZones(zones); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	s.persistZones()
	writeJSON(w, 201, map[string]any{"status": "ok"})
}

func (s *Server) zoneUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if s.zoneActive(id) {
		writeJSON(w, 409, map[string]any{"code": "ZONE_BUSY", "message": "分区正在广播，不能修改"})
		return
	}
	var z config.ZoneConfig
	if err := json.NewDecoder(r.Body).Decode(&z); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	z.ID = id
	zones := s.sip.ZoneConfigs()
	found := false
	for i := range zones {
		if zones[i].ID == id {
			zones[i] = z
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "分区不存在"})
		return
	}
	if err := s.sip.SetZones(zones); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	s.persistZones()
	writeJSON(w, 200, map[string]any{"status": "ok"})
}

func (s *Server) zoneDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if s.zoneActive(id) {
		writeJSON(w, 409, map[string]any{"code": "ZONE_BUSY", "message": "分区正在广播，不能删除"})
		return
	}
	zones := s.sip.ZoneConfigs()
	if len(zones) <= 1 {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "至少保留一个广播分区"})
		return
	}
	out := zones[:0]
	found := false
	for _, z := range zones {
		if z.ID == id {
			found = true
			continue
		}
		out = append(out, z)
	}
	if !found {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": "分区不存在"})
		return
	}
	if err := s.sip.SetZones(out); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	s.persistZones()
	writeJSON(w, 200, map[string]any{"status": "ok"})
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

type serverSettingsView struct {
	Active          config.ServerSettings `json:"active"`
	Pending         config.ServerSettings `json:"pending"`
	RestartRequired bool                  `json:"restart_required"`
}

func (s *Server) systemSettings(w http.ResponseWriter, r *http.Request) {
	pending := s.activeSettings
	if s.db != nil {
		if raw, ok := s.db.GetSetting("server_settings"); ok {
			var v config.ServerSettings
			if json.Unmarshal([]byte(raw), &v) == nil && config.ValidateServerSettings(v) == nil {
				pending = v
			}
		}
	}
	writeJSON(w, 200, serverSettingsView{
		Active: s.activeSettings, Pending: pending,
		RestartRequired: !pending.Equal(s.activeSettings),
	})
}

func (s *Server) systemSettingsSave(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, 503, map[string]any{"code": "UNAVAILABLE", "message": "settings storage unavailable"})
		return
	}
	var v config.ServerSettings
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	if err := config.ValidateServerSettings(v); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	if err := checkChangedListeners(s.activeSettings, v); err != nil {
		writeJSON(w, 400, map[string]any{"code": "PORT_UNAVAILABLE", "message": err.Error()})
		return
	}
	raw, _ := json.Marshal(v)
	if err := s.db.SetSetting("server_settings", string(raw)); err != nil {
		writeJSON(w, 500, map[string]any{"code": "INTERNAL", "message": err.Error()})
		return
	}
	// Keep the previous active settings for one-click rollback.
	if prev, err := json.Marshal(s.activeSettings); err == nil {
		_ = s.db.SetSetting("server_settings_previous", string(prev))
	}
	writeJSON(w, 200, serverSettingsView{Active: s.activeSettings, Pending: v, RestartRequired: !v.Equal(s.activeSettings)})
}

func (s *Server) systemSettingsRollback(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, 503, map[string]any{"code": "UNAVAILABLE", "message": "settings storage unavailable"})
		return
	}
	raw, _ := json.Marshal(s.activeSettings)
	_ = s.db.SetSetting("server_settings", string(raw))
	writeJSON(w, 200, serverSettingsView{Active: s.activeSettings, Pending: s.activeSettings})
}

func (s *Server) systemInterfaces(w http.ResponseWriter, r *http.Request) {
	defaultIP := ""
	if c, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defaultIP = c.LocalAddr().(*net.UDPAddr).IP.String()
		_ = c.Close()
	}
	items := []map[string]any{}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			items = append(items, map[string]any{
				"name": iface.Name, "ip": ip.String(), "default": ip.String() == defaultIP,
			})
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "default": defaultIP})
}

func (s *Server) systemRestart(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 202, map[string]any{"status": "restarting"})
	go func() {
		// Windows SCM recovery is configured by the installer, but an existing
		// installation may not have the failure flag set. A detached starter
		// makes the web restart reliable even in that case.
		if s.serviceMode && runtime.GOOS == "windows" {
			cmd := exec.Command("cmd.exe", "/c", "timeout /t 3 /nobreak >nul & sc.exe start MCS")
			if err := cmd.Start(); err != nil {
				log.Printf("restart helper failed: %v", err)
			}
		}
		time.Sleep(700 * time.Millisecond)
		log.Printf("restarting MCS process")
		os.Exit(1)
	}()
}

func checkChangedListeners(old, next config.ServerSettings) error {
	type check struct {
		name, oldAddr, newAddr, network string
	}
	for _, c := range []check{
		{"http", old.HTTP, next.HTTP, "tcp"},
		{"https", old.HTTPS, next.HTTPS, "tcp"},
		{"sip_udp", old.SIPUDP, next.SIPUDP, "udp"},
	} {
		if c.newAddr == "" || c.newAddr == c.oldAddr {
			continue
		}
		op := addrPort(c.oldAddr)
		np := addrPort(c.newAddr)
		if op != "" && op == np {
			continue
		}
		var err error
		if c.network == "udp" {
			var conn *net.UDPConn
			conn, err = net.ListenUDP("udp", mustUDPAddr(c.newAddr))
			if err == nil {
				_ = conn.Close()
			}
		} else {
			var ln net.Listener
			ln, err = net.Listen("tcp", c.newAddr)
			if err == nil {
				_ = ln.Close()
			}
		}
		if err != nil {
			return fmt.Errorf("%s 新地址不可用：%v", c.name, err)
		}
	}
	return nil
}

func addrPort(addr string) string {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return p
}

func mustUDPAddr(addr string) *net.UDPAddr {
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return &net.UDPAddr{}
	}
	return a
}

func (s *Server) pagingInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.sip.PagingInfo())
}

func (s *Server) pagingEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	events := s.sip.PagingEvents(limit)
	if events == nil {
		events = []store.PagingEvent{}
	}
	writeJSON(w, 200, map[string]any{"items": events, "total": len(events)})
}

func (s *Server) pagingWebRTC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ZoneID string `json:"zone_id"`
		PIN    string `json:"pin"`
		SDP    string `json:"sdp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ZoneID) == "" || strings.TrimSpace(req.SDP) == "" {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "zone_id and sdp required"})
		return
	}
	user := "operator"
	if sess, ok := s.sessionFor(r); ok && sess.username != "" {
		user = sess.username
	}
	answer, id, err := s.sip.HandlePagingWebRTC(r.Context(), req.ZoneID, user, req.PIN, req.SDP)
	if err != nil {
		writeJSON(w, 400, map[string]any{"code": "PAGING_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"sdp": answer, "id": id})
}

func (s *Server) pagingStop(w http.ResponseWriter, r *http.Request) {
	if err := s.sip.PagingStop(r.PathValue("id")); err != nil {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok"})
}

func (s *Server) conferenceList(w http.ResponseWriter, r *http.Request) {
	rooms := s.sip.ConferenceView()
	if rooms == nil {
		rooms = []sipserver.ConferenceRoomView{}
	}
	writeJSON(w, 200, map[string]any{"items": rooms, "total": len(rooms)})
}

type licenseView struct {
	license.Info
	UsedPhones   int `json:"used_phones"`
	OnlinePhones int `json:"online_phones"`
}

func (s *Server) licenseGet(w http.ResponseWriter, r *http.Request) {
	if s.license == nil {
		writeJSON(w, 503, map[string]any{"code": "UNAVAILABLE", "message": "license unavailable"})
		return
	}
	used, online := s.store.Count()
	writeJSON(w, 200, licenseView{Info: s.license.Info(), UsedPhones: used, OnlinePhones: online})
}

func (s *Server) licenseActivate(w http.ResponseWriter, r *http.Request) {
	if s.license == nil {
		writeJSON(w, 503, map[string]any{"code": "UNAVAILABLE", "message": "license unavailable"})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "激活码不能为空"})
		return
	}
	info, err := s.license.Activate(req.Code)
	if err != nil {
		writeJSON(w, 400, map[string]any{"code": "LICENSE_INVALID", "message": err.Error()})
		return
	}
	if s.db != nil {
		_ = s.db.SetSetting("license_code", strings.TrimSpace(req.Code))
	}
	used, online := s.store.Count()
	writeJSON(w, 200, licenseView{Info: info, UsedPhones: used, OnlinePhones: online})
}

func (s *Server) conferenceAudioGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.sip.ConferenceAudio())
}

func (s *Server) conferenceAudioSave(w http.ResponseWriter, r *http.Request) {
	var req config.ConferenceAudioConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	req = config.NormalizeConferenceAudio(req)
	if err := config.ValidateConferenceAudio(req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	if err := s.sip.SetConferenceAudio(req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	if s.db != nil {
		if raw, err := json.Marshal(req); err == nil {
			_ = s.db.SetSetting("conference_audio", string(raw))
		}
	}
	writeJSON(w, 200, req)
}

func (s *Server) conferenceRoomCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Number     string `json:"number"`
		Name       string `json:"name"`
		MaxMembers int    `json:"max_members"`
		Record     bool   `json:"record"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	if err := s.sip.CreateConferenceRoom(req.Number, req.Name, req.MaxMembers, req.Record); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 201, map[string]any{"status": "ok", "number": req.Number})
}

func (s *Server) conferenceRoomUpdate(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	var req struct {
		Name       string `json:"name"`
		MaxMembers int    `json:"max_members"`
		Record     bool   `json:"record"`
		Enabled    bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	if err := s.sip.UpdateConferenceRoom(number, req.Name, req.MaxMembers, req.Record, req.Enabled); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok"})
}

func (s *Server) conferenceRoomDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.sip.DeleteConferenceRoom(r.PathValue("number")); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok"})
}

func (s *Server) conferenceWebRTC(w http.ResponseWriter, r *http.Request) {
	room := r.PathValue("room")
	var req struct {
		SDP string `json:"sdp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.SDP) == "" {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "sdp required"})
		return
	}
	user := "admin"
	if sess, ok := s.sessionFor(r); ok && sess.username != "" {
		user = sess.username
	}
	answer, id, err := s.sip.HandleConferenceWebRTC(r.Context(), room, user, req.SDP)
	if err != nil {
		writeJSON(w, 400, map[string]any{"code": "WEBRTC_FAILED", "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"sdp": answer, "id": id})
}

func (s *Server) conferenceKick(w http.ResponseWriter, r *http.Request) {
	room := r.PathValue("room")
	id := r.PathValue("id")
	if err := s.sip.ConferenceKick(room, id); err != nil {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok"})
}

func (s *Server) conferenceMute(w http.ResponseWriter, r *http.Request) {
	room := r.PathValue("room")
	id := r.PathValue("id")
	var req struct {
		Muted bool `json:"muted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"code": "VALIDATION_FAILED", "message": "invalid json"})
		return
	}
	if err := s.sip.ConferenceMute(room, id, req.Muted); err != nil {
		writeJSON(w, 404, map[string]any{"code": "NOT_FOUND", "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "muted": req.Muted})
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
