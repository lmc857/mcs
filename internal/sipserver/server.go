package sipserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mcs/internal/config"
	"mcs/internal/license"
	"mcs/internal/store"
	"mcs/internal/tts"
)

const serverName = "MCS/0.1"

// Server owns the SIP stack: registration plus the M0 B2BUA call engine.
type Server struct {
	ua              *sipgo.UserAgent
	srv             *sipgo.Server
	client          *sipgo.Client
	dua             *sipgo.DialogUA
	store           *store.Store
	auth            *Authenticator
	log             *slog.Logger
	domain          string
	advertise       string
	advertiseHost   string
	dataDir         string
	cdr             *store.CDRStore
	alarms          *store.AlarmStore
	alarmCfg        config.AlarmConfig
	registrationCfg config.RegistrationConfig
	hbMu            sync.Mutex
	hbFailures      map[string]int
	broadcastCfg    config.BroadcastConfig
	pagingCfg       config.PagingConfig
	license         *license.Manager
	audioMu         sync.RWMutex
	conferenceAudio config.ConferenceAudioConfig
	mediaTranscode  bool
	zonesMu         sync.RWMutex
	zones           []zone
	ttsMu           sync.RWMutex
	ttsCfg          config.TTSConfig
	ttsClient       *tts.Client
	media           *mediaLibrary
	schedules       *scheduleStore
	bc              *broadcastManager
	conference      *conferenceManager
	calls           *callRegistry
	events          *eventBus
	pagingMu        sync.Mutex
	paging          map[string]*pagingSession
}

// Options carries the runtime configuration for the SIP server.
type Options struct {
	Domain       string
	Advertise    string
	DataDir      string
	Alarm        config.AlarmConfig
	Registration config.RegistrationConfig
	Broadcast    config.BroadcastConfig
	Paging       config.PagingConfig
	Conference   config.ConferenceConfig
	License      *license.Manager
	Media        config.MediaConfig
	TTS          config.TTSConfig
	Zones        []config.ZoneConfig
}

func New(st *store.Store, auth *Authenticator, cdr *store.CDRStore, alarms *store.AlarmStore, opts Options, log *slog.Logger) (*Server, error) {
	ua, err := sipgo.NewUA(sipgo.WithUserAgent(serverName))
	if err != nil {
		return nil, fmt.Errorf("sip ua: %w", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		_ = ua.Close()
		return nil, fmt.Errorf("sip server: %w", err)
	}
	client, err := sipgo.NewClient(ua, sipgo.WithClientLogger(log))
	if err != nil {
		_ = ua.Close()
		return nil, fmt.Errorf("sip client: %w", err)
	}
	advHost, advPort, err := splitHostPort(opts.Advertise)
	if err != nil {
		_ = ua.Close()
		return nil, err
	}
	dua := &sipgo.DialogUA{
		Client: client,
		ContactHDR: sip.ContactHeader{Address: sip.Uri{
			Scheme: "sip", User: "mcs", Host: advHost, Port: advPort,
		}},
	}
	pagingCfg := opts.Paging
	s := &Server{
		ua: ua, srv: srv, client: client, dua: dua, store: st, auth: auth,
		log: log, domain: opts.Domain, advertise: opts.Advertise, advertiseHost: advHost,
		dataDir: opts.DataDir, cdr: cdr, alarms: alarms, alarmCfg: opts.Alarm,
		registrationCfg: opts.Registration,
		hbFailures:      map[string]int{},
		broadcastCfg:    opts.Broadcast,
		pagingCfg:       pagingCfg,
		license:         opts.License,
		conferenceAudio: config.NormalizeConferenceAudio(opts.Conference.Audio),
		mediaTranscode:  opts.Media.Transcode,
		zones:           buildZones(opts.Zones, st),
		ttsCfg:          opts.TTS,
		ttsClient: tts.NewFromConfig(tts.Config{
			Provider: opts.TTS.Provider, BaseURL: opts.TTS.BaseURL, APIKey: opts.TTS.APIKey,
			Model: opts.TTS.Model, VoiceZH: opts.TTS.VoiceZH, VoiceEN: opts.TTS.VoiceEN,
			Timeout: time.Duration(opts.TTS.TimeoutS) * time.Second,
		}, filepath.Join(opts.DataDir, "audio", "tts")),
		media:     newMediaLibrary(opts.DataDir),
		schedules: newScheduleStore(opts.DataDir),
		bc:        newBroadcastManager(),
		calls:     newCallRegistry(), events: newEventBus(),
		paging: map[string]*pagingSession{},
	}
	s.conference = newConferenceManager(s, opts.Conference)
	srv.OnRegister(s.onRegister)
	srv.OnOptions(s.onOptions)
	srv.OnInvite(s.handleInvite)
	srv.OnAck(s.handleAck)
	srv.OnBye(s.handleBye)
	srv.OnCancel(s.handleCancel)
	srv.OnInfo(s.handleInfo)
	srv.OnNoRoute(s.onNoRoute)
	return s, nil
}

// Serve blocks until ctx is cancelled.
func (s *Server) MediaTranscode() bool { return s.mediaTranscode }

func (s *Server) TTSClient() *tts.Client {
	s.ttsMu.RLock()
	defer s.ttsMu.RUnlock()
	return s.ttsClient
}

func (s *Server) TTSConfig() config.TTSConfig {
	s.ttsMu.RLock()
	defer s.ttsMu.RUnlock()
	return s.ttsCfg
}

func (s *Server) SetTTS(cfg config.TTSConfig) error {
	if cfg.Provider == "" {
		cfg.Provider = "openai"
	}
	if cfg.Provider != "openai" && cfg.Provider != "minimax" {
		return fmt.Errorf("tts provider must be openai or minimax")
	}
	if cfg.TimeoutS <= 0 {
		cfg.TimeoutS = 30
	}
	client := tts.NewFromConfig(tts.Config{
		Provider: cfg.Provider, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey,
		Model: cfg.Model, VoiceZH: cfg.VoiceZH, VoiceEN: cfg.VoiceEN,
		Timeout: time.Duration(cfg.TimeoutS) * time.Second,
	}, filepath.Join(s.dataDir, "audio", "tts"))
	s.ttsMu.Lock()
	s.ttsCfg = cfg
	s.ttsClient = client
	s.ttsMu.Unlock()
	s.log.Info("tts config updated", "provider", cfg.Provider, "base_url", cfg.BaseURL, "model", cfg.Model, "voice_zh", cfg.VoiceZH, "voice_en", cfg.VoiceEN)
	return nil
}

func (s *Server) Serve(ctx context.Context, addr string) error {
	s.log.Info("sip listening", "transport", "udp", "addr", addr, "domain", s.domain)
	return s.srv.ListenAndServe(ctx, "udp", addr)
}

// ServeTLS listens for SIP over TLS using PEM cert/key files.
func (s *Server) ServeTLS(ctx context.Context, addr, certFile, keyFile string) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load tls cert: %w", err)
	}
	conf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	s.log.Info("sip listening", "transport", "tls", "addr", addr, "domain", s.domain)
	return s.srv.ListenAndServeTLS(ctx, "tcp", addr, conf)
}

func (s *Server) Close() error {
	if s.srv != nil {
		_ = s.srv.Close()
	}
	if s.ua != nil {
		return s.ua.Close()
	}
	return nil
}

func (s *Server) onRegister(req *sip.Request, tx sip.ServerTransaction) {
	ext := aor(req)
	if ext == "" {
		s.respond(req, tx, 400, "Bad Request")
		return
	}
	authHeader := req.GetHeader("Authorization")
	s.log.Info("register attempt", "ext", ext, "source", req.Source(), "authorized", authHeader != nil)
	if authHeader == nil {
		s.challenge(req, tx)
		return
	}
	password, ok := s.store.Password(ext)
	if !ok {
		s.log.Warn("register unknown ext", "ext", ext, "source", req.Source())
		s.respond(req, tx, 403, "Forbidden")
		return
	}
	if !s.auth.Verify(authHeader.Value(), req.Method.String(), req.Recipient.String(), ext, password) {
		s.log.Warn("register auth failed", "ext", ext, "source", req.Source())
		s.challenge(req, tx)
		return
	}

	ttl, hasContact := registrationTTL(req)
	if !hasContact {
		// REGISTER query: answer with current bindings (M0: simple 200).
		s.respond(req, tx, 200, "OK")
		return
	}
	maxTTL := s.registrationCfg.MaxExpiresS
	if maxTTL <= 0 {
		maxTTL = 300
	}
	defaultTTL := s.registrationCfg.DefaultExpiresS
	if defaultTTL <= 0 {
		defaultTTL = maxTTL
	}
	if ttl == 0 && req.GetHeader("Expires") == nil && !contactHasExpires(req) {
		ttl = defaultTTL
	}
	if ttl > maxTTL {
		ttl = maxTTL
	}
	if ttl < 0 {
		ttl = 0
	}
	if ttl <= 0 {
		s.store.Unregister(ext)
		s.resetHeartbeat(ext)
		s.log.Info("unregistered", "ext", ext, "source", req.Source())
		s.emit("device.unregistered", map[string]any{"ext": ext, "source": req.Source()})
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Expires", "0"))
		_ = tx.Respond(res)
		return
	}

	if s.license != nil && s.license.MaxPhones() > 0 {
		view, known := s.store.Get(ext)
		if !known || !view.Online {
			_, online := s.store.Count()
			if online >= s.license.MaxPhones() {
				s.log.Warn("register license limit", "ext", ext, "online", online, "max_phones", s.license.MaxPhones())
				s.respond(req, tx, 403, "Forbidden")
				return
			}
		}
	}
	contact := ""
	if ch := req.GetHeader("Contact"); ch != nil {
		contact = ch.Value()
	}
	src := req.Source()
	ua := headerValue(req, "User-Agent")
	model := headerValue(req, "X-MCS-Model")
	firmware := headerValue(req, "X-MCS-Firmware")
	if !s.store.Register(ext, contact, src, ua, model, firmware, time.Duration(ttl)*time.Second) {
		s.respond(req, tx, 403, "Forbidden")
		return
	}
	s.resetHeartbeat(ext)
	s.log.Info("registered", "ext", ext, "contact", contact, "source", src, "ttl", ttl, "model", model, "firmware", firmware)
	s.emit("device.registered", map[string]any{
		"ext": ext, "contact": contact, "source": src, "model": model, "firmware": firmware,
	})

	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	if contact != "" {
		res.AppendHeader(sip.NewHeader("Contact", contact))
	}
	res.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(ttl)))
	_ = tx.Respond(res)
}

func (s *Server) onOptions(req *sip.Request, tx sip.ServerTransaction) {
	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	res.AppendHeader(sip.NewHeader("Allow", "INVITE, ACK, CANCEL, BYE, REGISTER, OPTIONS, INFO"))
	res.AppendHeader(sip.NewHeader("Server", serverName))
	_ = tx.Respond(res)
}

func (s *Server) onNoRoute(req *sip.Request, tx sip.ServerTransaction) {
	s.log.Info("unsupported method", "method", req.Method.String(), "source", req.Source())
	_ = tx.Respond(sip.NewResponseFromRequest(req, 405, "Method Not Allowed", nil))
}

func (s *Server) challenge(req *sip.Request, tx sip.ServerTransaction) {
	res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
	res.AppendHeader(sip.NewHeader("WWW-Authenticate", s.auth.Challenge()))
	res.AppendHeader(sip.NewHeader("Server", serverName))
	_ = tx.Respond(res)
}

func (s *Server) respond(req *sip.Request, tx sip.ServerTransaction, code int, reason string) {
	res := sip.NewResponseFromRequest(req, code, reason, nil)
	res.AppendHeader(sip.NewHeader("Server", serverName))
	_ = tx.Respond(res)
}

// aor returns the To user part, falling back to From.
func aor(req *sip.Request) string {
	if to := req.To(); to != nil && to.Address.User != "" {
		return to.Address.User
	}
	if from := req.From(); from != nil {
		return from.Address.User
	}
	return ""
}

// registrationTTL extracts expires from the Expires header or Contact; hasContact
// reports whether a Contact header is present at all.
func registrationTTL(req *sip.Request) (ttl int, hasContact bool) {
	contact := req.GetHeader("Contact")
	if contact == nil {
		return 0, false
	}
	hasContact = true
	if exp := req.GetHeader("Expires"); exp != nil {
		if v, err := strconv.Atoi(strings.TrimSpace(exp.Value())); err == nil {
			return v, true
		}
	}
	if ch, ok := contact.(*sip.ContactHeader); ok {
		if v, ok := ch.Params.Get("expires"); ok {
			if n, err := strconv.Atoi(v); err == nil {
				return n, true
			}
		}
	}
	return 0, true
}

func contactHasExpires(req *sip.Request) bool {
	ch, ok := req.GetHeader("Contact").(*sip.ContactHeader)
	if !ok {
		return false
	}
	_, ok = ch.Params.Get("expires")
	return ok
}

func headerValue(req *sip.Request, name string) string {
	if h := req.GetHeader(name); h != nil {
		return strings.TrimSpace(h.Value())
	}
	return ""
}

func (s *Server) ConferenceAudio() config.ConferenceAudioConfig {
	s.audioMu.RLock()
	defer s.audioMu.RUnlock()
	return s.conferenceAudio
}

func (s *Server) SetConferenceAudio(c config.ConferenceAudioConfig) error {
	c = config.NormalizeConferenceAudio(c)
	if err := config.ValidateConferenceAudio(c); err != nil {
		return err
	}
	s.audioMu.Lock()
	s.conferenceAudio = c
	s.audioMu.Unlock()
	return nil
}
