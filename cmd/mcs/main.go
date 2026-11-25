package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mcs/internal/api"
	"mcs/internal/auth"
	"mcs/internal/config"
	"mcs/internal/license"
	"mcs/internal/logbus"
	"mcs/internal/sipserver"
	"mcs/internal/store"
)

var version = "0.1.0-dev"

func main() {
	var (
		configPath  = flag.String("config", "config.yaml", "path to YAML config")
		logPath     = flag.String("log", "", "optional log file (also logs to stdout)")
		serviceMode = flag.Bool("service", false, "run as Windows service")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println("mcs", version)
		return
	}
	if *serviceMode {
		if err := runWindowsService(*configPath, *logPath); err != nil {
			fmt.Fprintf(os.Stderr, "mcs service: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runServer(ctx, *configPath, *logPath, false); err != nil {
		fmt.Fprintf(os.Stderr, "mcs: %v\n", err)
		os.Exit(1)
	}
}

func newLogger(logPath string) (*slog.Logger, io.Closer, *logbus.Hub, error) {
	var writers []io.Writer
	writers = append(writers, os.Stdout)
	var closer io.Closer
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil && filepath.Dir(logPath) != "." {
			return nil, nil, nil, err
		}
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, nil, err
		}
		closer = f
		writers = append(writers, f)
	}
	hub := logbus.New(1500)
	base := slog.NewTextHandler(io.MultiWriter(writers...), &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(logbus.NewHandler(hub, base)), closer, hub, nil
}

func bootstrapUsers(db *store.DB, cfg config.Config, logger *slog.Logger) error {
	if db.CountUsers() > 0 {
		return nil
	}
	username := strings.TrimSpace(cfg.Auth.ConsoleUser)
	if username == "" {
		username = "admin"
	}
	password := cfg.Auth.ConsolePassword
	generated := false
	if password == "" {
		random, err := auth.RandomPassword(18)
		if err != nil {
			return err
		}
		password = random
		generated = true
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := db.CreateUser(store.User{
		Username: username, PasswordHash: hash, Role: "admin", Enabled: true,
	}); err != nil {
		return err
	}
	if generated {
		path := filepath.Join(cfg.Storage.DataDir, "initial-admin.txt")
		content := "MCS 初始管理员账号\nusername: " + username + "\npassword: " + password + "\n请登录后立即修改密码并删除此文件。\n"
		_ = os.WriteFile(path, []byte(content), 0o600)
		logger.Warn("initial admin generated", "username", username, "password_file", path)
	} else {
		logger.Info("initial admin created from console credentials", "username", username)
	}
	return nil
}

func runServer(parent context.Context, configPath, logPath string, serviceMode bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("%w\nhint: cp config.example.yaml config.yaml && edit it", err)
	}

	logger, logCloser, logHub, err := newLogger(logPath)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	if logCloser != nil {
		defer logCloser.Close()
	}
	slog.SetDefault(logger)

	secret := cfg.Auth.Secret
	if secret == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return fmt.Errorf("generate auth secret: %w", err)
		}
		secret = hex.EncodeToString(buf)
		logger.Warn("auth.secret is empty, generated an ephemeral secret (nonces reset on restart)")
	}

	if err := os.MkdirAll(cfg.Storage.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	dbPath := filepath.Join(cfg.Storage.DataDir, "mcs.db")
	db, err := store.OpenDB(dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	defer db.Close()

	if err := bootstrapUsers(db, cfg, logger); err != nil {
		return fmt.Errorf("bootstrap users: %w", err)
	}

	ttsCfg := cfg.TTS
	if raw, ok := db.GetSetting("tts"); ok {
		var saved config.TTSConfig
		if err := json.Unmarshal([]byte(raw), &saved); err == nil {
			ttsCfg = saved
		}
	}
	if raw, ok := db.GetSetting("server_settings"); ok {
		var saved config.ServerSettings
		if err := json.Unmarshal([]byte(raw), &saved); err != nil {
			logger.Warn("ignore invalid server settings", "error", err)
		} else {
			// Settings saved before 0.11.0 have no TLS fields. Preserve the
			// certificate paths already configured in YAML on upgrade.
			if saved.Version == 0 {
				if saved.SIPTLS == "" {
					saved.SIPTLS = cfg.Server.SIPTLS
					saved.TLSCert = cfg.Server.TLSCert
					saved.TLSKey = cfg.Server.TLSKey
					saved.TLSCA = cfg.Server.TLSCA
					saved.TLSInsecure = cfg.Server.TLSInsecure
					saved.HTTPSCert = cfg.Server.HTTPSCert
					saved.HTTPSKey = cfg.Server.HTTPSKey
				}
				if saved.SRTPMode == "" {
					saved.SRTPMode = cfg.Server.SRTPMode
				}
			}
			if saved.Version < 3 && len(saved.STUNURLs) == 0 && len(saved.TURNURLs) == 0 && saved.WebRTCPublicIP == "" {
				ws := cfg.ServerSettings()
				saved.STUNURLs = ws.STUNURLs
				saved.TURNURLs = ws.TURNURLs
				saved.TURNUsername = ws.TURNUsername
				saved.TURNPassword = ws.TURNPassword
				saved.WebRTCPublicIP = ws.WebRTCPublicIP
			}
			if err := cfg.ApplyServerSettings(saved); err != nil {
				logger.Warn("ignore invalid server settings", "error", err)
			} else {
				logger.Info("server settings loaded from db", "advertise", saved.Advertise, "http", saved.HTTP, "sip_udp", saved.SIPUDP)
			}
		}
	}
	if raw, ok := db.GetSetting("conference_audio"); ok {
		var saved config.ConferenceAudioConfig
		if err := json.Unmarshal([]byte(raw), &saved); err == nil {
			saved = config.NormalizeConferenceAudio(saved)
			if err := config.ValidateConferenceAudio(saved); err == nil {
				cfg.Conference.Audio = saved
			} else {
				logger.Warn("ignore invalid conference audio settings", "error", err)
			}
		}
	}
	machineCode, err := license.MachineCode()
	if err != nil {
		logger.Warn("machine code unavailable; license activation disabled", "error", err)
	}
	lic := license.New(license.DefaultPublicKey(), 4, machineCode)
	if raw, ok := db.GetSetting("license_code"); ok {
		if info, err := lic.Activate(raw); err != nil {
			logger.Warn("ignore invalid license code", "error", err)
		} else {
			logger.Info("license loaded", "customer", info.Customer, "max_phones", info.MaxPhones)
		}
	}
	if raw, ok := db.GetSetting("zones"); ok {
		var saved []config.ZoneConfig
		if err := json.Unmarshal([]byte(raw), &saved); err != nil {
			logger.Warn("ignore invalid zones setting", "error", err)
		} else if len(saved) > 0 {
			if err := config.ValidateZones(saved); err != nil {
				logger.Warn("ignore invalid zones setting", "error", err)
			} else {
				cfg.Zones = saved
				logger.Info("broadcast zones loaded from db", "zones", len(saved))
			}
		}
	}
	tlsClient, err := buildTLSClientConfig(cfg.Server)
	if err != nil {
		return fmt.Errorf("SIP TLS client config: %w", err)
	}
	st := store.NewWithDB(cfg.Devices, db)
	cdr := store.NewCDRStoreDB(2000, db)
	alarms := store.NewAlarmStoreDB(1000, db)
	auth := sipserver.NewAuthenticator(cfg.Auth.Realm, secret)
	sipSrv, err := sipserver.New(st, auth, cdr, alarms, sipserver.Options{
		Domain: cfg.Server.Domain, Advertise: cfg.Server.Advertise, DataDir: cfg.Storage.DataDir,
		Alarm: cfg.Alarm, Registration: cfg.Registration, Broadcast: cfg.Broadcast, Paging: cfg.Paging, Conference: cfg.Conference, Media: cfg.Media, TTS: ttsCfg, Zones: cfg.Zones, License: lic,
		TLSClientConfig: tlsClient, SRTPMode: cfg.Server.SRTPMode, WebRTC: cfg.WebRTC,
	}, logger)
	if err != nil {
		return fmt.Errorf("init sip server: %w", err)
	}

	consoleUser := cfg.Auth.ConsoleUser
	if consoleUser == "" {
		consoleUser = "admin"
	}
	apiOpts := []api.Option{}
	if cfg.Auth.ConsolePassword != "" {
		apiOpts = append(apiOpts, api.WithConsoleAuth(consoleUser, cfg.Auth.ConsolePassword))
	}
	apiOpts = append(apiOpts, api.WithDB(db), api.WithLogHub(logHub), api.WithServerSettings(cfg.ServerSettings()), api.WithLicense(lic), api.WithServiceMode(serviceMode))
	handler := api.New(st, cdr, alarms, sipSrv, cfg.Storage.DataDir, version, apiOpts...).Handler()
	httpSrv := &http.Server{
		Addr:              cfg.Server.HTTP,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	var httpsSrv *http.Server

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	sipSrv.StartScheduler(ctx)
	sipSrv.StartHeartbeat(ctx)
	go func() {
		if err := sipSrv.Serve(ctx, cfg.Server.SIPUDP); err != nil && ctx.Err() == nil {
			logger.Error("sip server stopped", "error", err)
			cancel()
		}
	}()
	if cfg.Server.SIPTLS != "" {
		if cfg.Server.TLSCert == "" || cfg.Server.TLSKey == "" {
			logger.Warn("sip_tls configured without tls_cert/tls_key, TLS not started")
		} else {
			go func() {
				if err := sipSrv.ServeTLS(ctx, cfg.Server.SIPTLS, cfg.Server.TLSCert, cfg.Server.TLSKey); err != nil && ctx.Err() == nil {
					logger.Error("sip tls stopped", "error", err)
					cancel()
				}
			}()
		}
	}
	if cfg.Server.HTTPS != "" {
		cert, key := cfg.Server.HTTPSCert, cfg.Server.HTTPSKey
		if cert == "" {
			cert = cfg.Server.TLSCert
		}
		if key == "" {
			key = cfg.Server.TLSKey
		}
		if cert == "" || key == "" {
			logger.Warn("https configured without certificate, HTTPS not started")
		} else {
			httpsSrv = &http.Server{
				Addr:              cfg.Server.HTTPS,
				Handler:           handler,
				ReadHeaderTimeout: 5 * time.Second,
				TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
			}
			go func() {
				logger.Info("https listening", "addr", cfg.Server.HTTPS)
				if err := httpsSrv.ListenAndServeTLS(cert, key); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("https server stopped", "error", err)
					cancel()
				}
			}()
		}
	}
	go func() {
		logger.Info("http listening", "addr", cfg.Server.HTTP)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", "error", err)
			cancel()
		}
	}()

	logger.Info("mcs started", "version", version, "sip", cfg.Server.SIPUDP, "http", cfg.Server.HTTP, "devices", len(cfg.Devices), "db", dbPath)
	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)
	if httpsSrv != nil {
		_ = httpsSrv.Shutdown(shutdownCtx)
	}
	_ = sipSrv.Close()
	return nil
}

func buildTLSClientConfig(sc config.ServerConfig) (*tls.Config, error) {
	if sc.TLSCA == "" && !sc.TLSInsecure {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: sc.TLSInsecure}
	if sc.TLSCA != "" {
		data, err := os.ReadFile(sc.TLSCA)
		if err != nil {
			return nil, fmt.Errorf("read tls_ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("tls_ca has no valid PEM certificate")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}
