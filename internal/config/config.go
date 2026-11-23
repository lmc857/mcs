package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the MCS runtime configuration (M0 slice).
type Config struct {
	Server       ServerConfig       `yaml:"server"`
	Storage      StorageConfig      `yaml:"storage"`
	Auth         AuthConfig         `yaml:"auth"`
	Alarm        AlarmConfig        `yaml:"alarm"`
	Registration RegistrationConfig `yaml:"registration"`
	Broadcast    BroadcastConfig    `yaml:"broadcast"`
	Paging       PagingConfig       `yaml:"paging"`
	Conference   ConferenceConfig   `yaml:"conference"`
	Media        MediaConfig        `yaml:"media"`
	TTS          TTSConfig          `yaml:"tts"`
	Zones        []ZoneConfig       `yaml:"zones"`
	Devices      []DeviceConfig     `yaml:"devices"`
}

type ServerConfig struct {
	SIPUDP    string `yaml:"sip_udp"`
	SIPTLS    string `yaml:"sip_tls"`  // 可选，例如 0.0.0.0:5061
	TLSCert   string `yaml:"tls_cert"` // PEM 证书
	TLSKey    string `yaml:"tls_key"`  // PEM 私钥
	HTTP      string `yaml:"http"`
	HTTPS     string `yaml:"https"`      // 可选，例如 0.0.0.0:8443
	HTTPSCert string `yaml:"https_cert"` // 为空时复用 tls_cert
	HTTPSKey  string `yaml:"https_key"`  // 为空时复用 tls_key
	Domain    string `yaml:"domain"`
	Advertise string `yaml:"advertise"`
}

type StorageConfig struct {
	DataDir string `yaml:"data_dir"`
}

type AuthConfig struct {
	Realm           string `yaml:"realm"`
	Secret          string `yaml:"secret"`
	ConsoleUser     string `yaml:"console_user"`
	ConsolePassword string `yaml:"console_password"`
}

type RegistrationConfig struct {
	DefaultExpiresS int             `yaml:"default_expires_s"`
	MaxExpiresS     int             `yaml:"max_expires_s"`
	Heartbeat       HeartbeatConfig `yaml:"heartbeat"`
}

type HeartbeatConfig struct {
	Enabled   bool `yaml:"enabled"`
	IntervalS int  `yaml:"interval_s"`
	TimeoutS  int  `yaml:"timeout_s"`
	Failures  int  `yaml:"failures"`
}

type AlarmConfig struct {
	Number string       `yaml:"number"`
	Levels []AlarmLevel `yaml:"levels"`
}

type AlarmLevel struct {
	Name     string   `yaml:"name"`
	Members  []string `yaml:"members"`
	TimeoutS int      `yaml:"timeout_s"`
}

type ConferenceConfig struct {
	Enabled bool                   `yaml:"enabled"`
	Rooms   []ConferenceRoomConfig `yaml:"rooms"`
}

type ConferenceRoomConfig struct {
	Number     string `yaml:"number"`
	Name       string `yaml:"name"`
	MaxMembers int    `yaml:"max_members"`
	Record     bool   `yaml:"record"`
}

type PagingConfig struct {
	Enabled     bool     `yaml:"enabled"`
	PIN         string   `yaml:"pin"`
	AllowedExts []string `yaml:"allowed_exts"`
}

type BroadcastConfig struct {
	AutoAnswer     string `yaml:"auto_answer"`
	InviteTimeoutS int    `yaml:"invite_timeout_s"`
	MaxConcurrency int    `yaml:"max_concurrency"`
}

// MediaConfig controls optional media compatibility handling.
type MediaConfig struct {
	// Transcode enables G.711 A/U conversion when the two legs negotiated
	// different codecs. Default false: relay codecs as negotiated by the
	// endpoints, which is the correct B2BUA behavior.
	Transcode bool `yaml:"transcode"`
}

type TTSConfig struct {
	Provider string `yaml:"provider" json:"provider"` // openai（OpenAI 兼容）| minimax
	BaseURL  string `yaml:"base_url" json:"base_url"`
	APIKey   string `yaml:"api_key" json:"api_key,omitempty"`
	Model    string `yaml:"model" json:"model,omitempty"`
	VoiceZH  string `yaml:"voice_zh" json:"voice_zh"`
	VoiceEN  string `yaml:"voice_en" json:"voice_en"`
	TimeoutS int    `yaml:"timeout_s" json:"timeout_s"`
}

type ZoneConfig struct {
	ID        string   `yaml:"id"`
	Name      string   `yaml:"name"`
	Dial      string   `yaml:"dial"`  // 实时寻呼短号，例如 5001
	Codec     string   `yaml:"codec"` // pcmu（默认）| opus
	Members   []string `yaml:"members"`
	Mode      string   `yaml:"mode"` // unicast | multicast
	Multicast string   `yaml:"multicast"`
}

type DeviceConfig struct {
	Ext      string `yaml:"ext"`
	Name     string `yaml:"name"`
	Password string `yaml:"password"`
	Zone     string `yaml:"zone"`
	Snapshot string `yaml:"snapshot"` // 设备 HTTP 抓拍地址，可选
}

func defaults() Config {
	return Config{
		Server: ServerConfig{
			SIPUDP:    "0.0.0.0:5060",
			HTTP:      "127.0.0.1:8080",
			Domain:    "mcs.local",
			Advertise: "127.0.0.1:5060",
		},
		Storage: StorageConfig{DataDir: "./data"},
		Auth:    AuthConfig{Realm: "mcs"},
		Alarm:   AlarmConfig{Number: "8000"},
		Broadcast: BroadcastConfig{
			AutoAnswer:     "<http://127.0.0.1>;info=alert-autoanswer",
			InviteTimeoutS: 5,
			MaxConcurrency: 16,
		},
		Paging: PagingConfig{Enabled: true},
		TTS: TTSConfig{
			Provider: "openai",
			BaseURL:  "http://127.0.0.1:8399",
			VoiceZH:  "zf_001",
			VoiceEN:  "af_heart",
			TimeoutS: 30,
		},
	}
}

// Load reads a YAML config file. Empty path returns defaults.
func Load(path string) (Config, error) {
	cfg := defaults()
	if path == "" {
		return cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Server.SIPUDP == "" {
		cfg.Server.SIPUDP = "0.0.0.0:5060"
	}
	if cfg.Server.HTTP == "" {
		cfg.Server.HTTP = "127.0.0.1:8080"
	}
	if cfg.Server.Advertise == "" {
		cfg.Server.Advertise = "127.0.0.1:5060"
	}
	if cfg.Storage.DataDir == "" {
		cfg.Storage.DataDir = "./data"
	}
	if cfg.Registration.DefaultExpiresS <= 0 {
		cfg.Registration.DefaultExpiresS = 300
	}
	if cfg.Registration.MaxExpiresS <= 0 {
		cfg.Registration.MaxExpiresS = 300
	}
	if cfg.Registration.DefaultExpiresS > cfg.Registration.MaxExpiresS {
		cfg.Registration.DefaultExpiresS = cfg.Registration.MaxExpiresS
	}
	if cfg.Registration.Heartbeat.IntervalS <= 0 {
		cfg.Registration.Heartbeat.IntervalS = 30
	}
	if cfg.Registration.Heartbeat.TimeoutS <= 0 {
		cfg.Registration.Heartbeat.TimeoutS = 5
	}
	if cfg.Registration.Heartbeat.Failures <= 0 {
		cfg.Registration.Heartbeat.Failures = 3
	}
	if cfg.Registration.Heartbeat.IntervalS < 5 {
		cfg.Registration.Heartbeat.IntervalS = 5
	}
	if cfg.Registration.Heartbeat.TimeoutS > cfg.Registration.Heartbeat.IntervalS {
		cfg.Registration.Heartbeat.TimeoutS = cfg.Registration.Heartbeat.IntervalS
	}
	if cfg.Registration.Heartbeat.Failures > 10 {
		cfg.Registration.Heartbeat.Failures = 10
	}
	if cfg.Alarm.Number == "" {
		cfg.Alarm.Number = "8000"
	}
	for i, lvl := range cfg.Alarm.Levels {
		if lvl.TimeoutS <= 0 {
			cfg.Alarm.Levels[i].TimeoutS = 20
		}
		if lvl.Name == "" {
			cfg.Alarm.Levels[i].Name = fmt.Sprintf("Level %d", i+1)
		}
	}
	if cfg.Auth.Realm == "" {
		cfg.Auth.Realm = "mcs"
	}
	roomNumbers := map[string]bool{}
	for i := range cfg.Conference.Rooms {
		r := &cfg.Conference.Rooms[i]
		r.Number = strings.TrimSpace(r.Number)
		if r.Number == "" {
			return cfg, fmt.Errorf("conference.rooms[%d].number is required", i)
		}
		if roomNumbers[r.Number] {
			return cfg, fmt.Errorf("conference room %q duplicated", r.Number)
		}
		roomNumbers[r.Number] = true
		if r.Name == "" {
			r.Name = "会议 " + r.Number
		}
		if r.MaxMembers <= 0 {
			r.MaxMembers = 8
		}
		if r.MaxMembers > 16 {
			r.MaxMembers = 16
		}
	}
	if cfg.Paging.PIN != "" {
		cfg.Paging.PIN = strings.TrimSpace(cfg.Paging.PIN)
	}
	if cfg.Broadcast.AutoAnswer == "" {
		cfg.Broadcast.AutoAnswer = "<http://127.0.0.1>;info=alert-autoanswer"
	}
	if cfg.Broadcast.InviteTimeoutS <= 0 {
		cfg.Broadcast.InviteTimeoutS = 5
	}
	if cfg.Broadcast.MaxConcurrency <= 0 {
		cfg.Broadcast.MaxConcurrency = 16
	}
	if cfg.TTS.Provider == "" {
		cfg.TTS.Provider = "openai"
	}
	if cfg.TTS.Provider != "openai" && cfg.TTS.Provider != "minimax" {
		return cfg, fmt.Errorf("tts.provider must be openai or minimax")
	}
	if cfg.TTS.BaseURL == "" {
		cfg.TTS.BaseURL = "http://127.0.0.1:8399"
	}
	if cfg.TTS.VoiceZH == "" {
		cfg.TTS.VoiceZH = "zf_001"
	}
	if cfg.TTS.VoiceEN == "" {
		cfg.TTS.VoiceEN = "af_heart"
	}
	if cfg.TTS.TimeoutS <= 0 {
		cfg.TTS.TimeoutS = 30
	}
	zoneIDs := map[string]bool{}
	dials := map[string]bool{}
	for i, z := range cfg.Zones {
		if z.ID == "" {
			return cfg, fmt.Errorf("zones[%d]: id is required", i)
		}
		if zoneIDs[z.ID] {
			return cfg, fmt.Errorf("zone id %q duplicated", z.ID)
		}
		zoneIDs[z.ID] = true
		if z.Dial != "" {
			if dials[z.Dial] {
				return cfg, fmt.Errorf("zone dial %q duplicated", z.Dial)
			}
			dials[z.Dial] = true
		}
		if z.Name == "" {
			cfg.Zones[i].Name = z.ID
		}
		if z.Codec == "" {
			z.Codec = "pcmu"
			cfg.Zones[i].Codec = z.Codec
		}
		if z.Codec != "pcmu" && z.Codec != "opus" {
			return cfg, fmt.Errorf("zones[%d]: codec must be pcmu or opus", i)
		}
		if z.Mode == "" {
			cfg.Zones[i].Mode = "unicast"
		}
		if z.Mode != "unicast" && z.Mode != "multicast" {
			return cfg, fmt.Errorf("zone %s: mode must be unicast or multicast", z.ID)
		}
		if z.Mode == "multicast" && z.Multicast == "" {
			return cfg, fmt.Errorf("zone %s: multicast address required", z.ID)
		}
	}
	seen := map[string]bool{}
	for i, d := range cfg.Devices {
		if d.Ext == "" || d.Password == "" {
			return cfg, fmt.Errorf("device[%d]: ext and password are required", i)
		}
		if seen[d.Ext] {
			return cfg, fmt.Errorf("device ext %q duplicated", d.Ext)
		}
		seen[d.Ext] = true
		if d.Name == "" {
			cfg.Devices[i].Name = d.Ext
		}
	}
	return cfg, nil
}
