package config

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
)

// ServerSettings are the allowlisted fields that can be edited from the
// web console. They are persisted in SQLite and applied on process start.
type ServerSettings struct {
	Version            int      `json:"settings_version,omitempty"`
	Advertise          string   `json:"advertise"`
	SIPUDP             string   `json:"sip_udp"`
	SIPTCP             string   `json:"sip_tcp"`
	HTTP               string   `json:"http"`
	HTTPS              string   `json:"https"`
	SIPTLS             string   `json:"sip_tls"`
	TLSCert            string   `json:"tls_cert"`
	TLSKey             string   `json:"tls_key"`
	TLSCA              string   `json:"tls_ca"`
	TLSInsecure        bool     `json:"tls_insecure"`
	HTTPSCert          string   `json:"https_cert"`
	HTTPSKey           string   `json:"https_key"`
	SRTPMode           string   `json:"srtp_mode"`
	STUNURLs           []string `json:"stun_urls"`
	TURNURLs           []string `json:"turn_urls"`
	TURNUsername       string   `json:"turn_username"`
	TURNPassword       string   `json:"turn_password"`
	WebRTCPublicIP     string   `json:"webrtc_public_ip"`
	Domain             string   `json:"domain"`
	AlarmNumber        string   `json:"alarm_number"`
	AutoAnswer         string   `json:"auto_answer"`
	Transcode          bool     `json:"transcode"`
	HeartbeatEnabled   bool     `json:"heartbeat_enabled"`
	HeartbeatIntervalS int      `json:"heartbeat_interval_s"`
	HeartbeatTimeoutS  int      `json:"heartbeat_timeout_s"`
	HeartbeatFailures  int      `json:"heartbeat_failures"`
}

func (c Config) ServerSettings() ServerSettings {
	srtpMode := c.Server.SRTPMode
	if srtpMode == "" {
		srtpMode = "auto"
	}
	var stunURLs, turnURLs []string
	turnUser, turnPass := "", ""
	for _, srv := range c.WebRTC.ICEServers {
		for _, u := range srv.URLs {
			if strings.HasPrefix(strings.ToLower(u), "turn:") || strings.HasPrefix(strings.ToLower(u), "turns:") {
				turnURLs = append(turnURLs, u)
				if turnUser == "" {
					turnUser, turnPass = srv.Username, srv.Credential
				}
			} else {
				stunURLs = append(stunURLs, u)
			}
		}
	}
	return ServerSettings{
		Version:        4,
		Advertise:      c.Server.Advertise,
		SIPUDP:         c.Server.SIPUDP,
		SIPTCP:         c.Server.SIPTCP,
		HTTP:           c.Server.HTTP,
		HTTPS:          c.Server.HTTPS,
		SIPTLS:         c.Server.SIPTLS,
		TLSCert:        c.Server.TLSCert,
		TLSKey:         c.Server.TLSKey,
		TLSCA:          c.Server.TLSCA,
		TLSInsecure:    c.Server.TLSInsecure,
		HTTPSCert:      c.Server.HTTPSCert,
		HTTPSKey:       c.Server.HTTPSKey,
		SRTPMode:       srtpMode,
		STUNURLs:       stunURLs,
		TURNURLs:       turnURLs,
		TURNUsername:   turnUser,
		TURNPassword:   turnPass,
		WebRTCPublicIP: c.WebRTC.PublicIP,
		Domain:         c.Server.Domain,
		AlarmNumber:    c.Alarm.Number,
		AutoAnswer:     c.Broadcast.AutoAnswer,
		Transcode:      c.Media.Transcode,
	}
}

func (c *Config) ApplyServerSettings(v ServerSettings) error {
	if err := ValidateServerSettings(v); err != nil {
		return err
	}
	if v.SRTPMode == "" {
		v.SRTPMode = "auto"
	}
	c.Server.Advertise = v.Advertise
	c.Server.SIPUDP = v.SIPUDP
	c.Server.SIPTCP = v.SIPTCP
	c.Server.HTTP = v.HTTP
	c.Server.HTTPS = v.HTTPS
	c.Server.SIPTLS = v.SIPTLS
	c.Server.TLSCert = v.TLSCert
	c.Server.TLSKey = v.TLSKey
	c.Server.TLSCA = v.TLSCA
	c.Server.TLSInsecure = v.TLSInsecure
	c.Server.HTTPSCert = v.HTTPSCert
	c.Server.HTTPSKey = v.HTTPSKey
	c.Server.SRTPMode = v.SRTPMode
	c.Server.Domain = v.Domain
	c.WebRTC = WebRTCConfig{PublicIP: strings.TrimSpace(v.WebRTCPublicIP)}
	for _, u := range v.STUNURLs {
		u = strings.TrimSpace(u)
		if u != "" {
			c.WebRTC.ICEServers = append(c.WebRTC.ICEServers, ICEServerConfig{URLs: []string{u}})
		}
	}
	if len(v.TURNURLs) > 0 {
		urls := make([]string, 0, len(v.TURNURLs))
		for _, u := range v.TURNURLs {
			if u = strings.TrimSpace(u); u != "" {
				urls = append(urls, u)
			}
		}
		if len(urls) > 0 {
			c.WebRTC.ICEServers = append(c.WebRTC.ICEServers, ICEServerConfig{
				URLs: urls, Username: v.TURNUsername, Credential: v.TURNPassword,
			})
		}
	}
	c.Alarm.Number = v.AlarmNumber
	c.Broadcast.AutoAnswer = v.AutoAnswer
	c.Media.Transcode = v.Transcode
	c.Registration.Heartbeat.Enabled = v.HeartbeatEnabled
	c.Registration.Heartbeat.IntervalS = v.HeartbeatIntervalS
	c.Registration.Heartbeat.TimeoutS = v.HeartbeatTimeoutS
	c.Registration.Heartbeat.Failures = v.HeartbeatFailures
	return nil
}

func (v ServerSettings) Equal(w ServerSettings) bool {
	v.STUNURLs = append([]string(nil), v.STUNURLs...)
	w.STUNURLs = append([]string(nil), w.STUNURLs...)
	v.TURNURLs = append([]string(nil), v.TURNURLs...)
	w.TURNURLs = append([]string(nil), w.TURNURLs...)
	return v.Version == w.Version &&
		v.Advertise == w.Advertise && v.SIPUDP == w.SIPUDP && v.SIPTCP == w.SIPTCP && v.HTTP == w.HTTP &&
		v.HTTPS == w.HTTPS && v.SIPTLS == w.SIPTLS && v.TLSCert == w.TLSCert &&
		v.TLSKey == w.TLSKey && v.TLSCA == w.TLSCA && v.TLSInsecure == w.TLSInsecure &&
		v.HTTPSCert == w.HTTPSCert && v.HTTPSKey == w.HTTPSKey && v.SRTPMode == w.SRTPMode &&
		v.Domain == w.Domain && v.AlarmNumber == w.AlarmNumber && v.AutoAnswer == w.AutoAnswer &&
		v.Transcode == w.Transcode && v.HeartbeatEnabled == w.HeartbeatEnabled &&
		v.HeartbeatIntervalS == w.HeartbeatIntervalS && v.HeartbeatTimeoutS == w.HeartbeatTimeoutS &&
		v.HeartbeatFailures == w.HeartbeatFailures && slices.Equal(v.STUNURLs, w.STUNURLs) &&
		slices.Equal(v.TURNURLs, w.TURNURLs) && v.TURNUsername == w.TURNUsername &&
		v.TURNPassword == w.TURNPassword && v.WebRTCPublicIP == w.WebRTCPublicIP
}

func ValidateServerSettings(v ServerSettings) error {
	if err := validateHostPort("advertise", v.Advertise, false, true); err != nil {
		return err
	}
	if err := validateHostPort("sip_udp", v.SIPUDP, false, false); err != nil {
		return err
	}
	if err := validateHostPort("sip_tcp", v.SIPTCP, true, false); err != nil {
		return err
	}
	if err := validateHostPort("http", v.HTTP, false, false); err != nil {
		return err
	}
	if err := validateHostPort("https", v.HTTPS, true, false); err != nil {
		return err
	}
	if err := validateHostPort("sip_tls", v.SIPTLS, true, false); err != nil {
		return err
	}
	switch v.SRTPMode {
	case "", "auto":
	case "required", "off":
	default:
		return fmt.Errorf("srtp_mode 必须是 auto、required 或 off")
	}
	if v.SIPTLS != "" {
		if strings.TrimSpace(v.TLSCert) == "" || strings.TrimSpace(v.TLSKey) == "" {
			return fmt.Errorf("启用 SIP TLS 时必须填写证书和私钥路径")
		}
	}
	if v.HTTPS != "" && (v.HTTPSCert != "" || v.HTTPSKey != "") {
		if v.HTTPSCert == "" || v.HTTPSKey == "" {
			return fmt.Errorf("HTTPS 证书和私钥必须同时填写")
		}
	}
	if err := validateICEURLs("stun_urls", v.STUNURLs, "stun:"); err != nil {
		return err
	}
	if err := validateICEURLs("turn_urls", v.TURNURLs, "turn:", "turns:"); err != nil {
		return err
	}
	if len(v.TURNURLs) > 0 && (strings.TrimSpace(v.TURNUsername) == "" || v.TURNPassword == "") {
		return fmt.Errorf("配置 TURN 时必须填写用户名和密码")
	}
	if v.WebRTCPublicIP != "" && net.ParseIP(strings.TrimSpace(v.WebRTCPublicIP)) == nil {
		return fmt.Errorf("webrtc_public_ip 不是合法 IP")
	}
	if strings.TrimSpace(v.Domain) == "" {
		return fmt.Errorf("domain 不能为空")
	}
	if strings.TrimSpace(v.AlarmNumber) == "" {
		return fmt.Errorf("报警号码不能为空")
	}
	if strings.TrimSpace(v.AutoAnswer) == "" {
		return fmt.Errorf("auto_answer 不能为空")
	}
	if len(v.AutoAnswer) > 512 {
		return fmt.Errorf("auto_answer 太长")
	}
	if v.HeartbeatEnabled {
		if v.HeartbeatIntervalS < 5 || v.HeartbeatIntervalS > 3600 {
			return fmt.Errorf("heartbeat_interval_s 必须在 5-3600")
		}
		if v.HeartbeatTimeoutS < 1 || v.HeartbeatTimeoutS > v.HeartbeatIntervalS {
			return fmt.Errorf("heartbeat_timeout_s 必须在 1-间隔秒数")
		}
		if v.HeartbeatFailures < 1 || v.HeartbeatFailures > 10 {
			return fmt.Errorf("heartbeat_failures 必须在 1-10")
		}
	}
	if strings.ContainsAny(v.Domain, " \t\r\n") {
		return fmt.Errorf("domain 不能包含空白字符")
	}
	return nil
}

func validateHostPort(name, value string, allowEmpty, allowHostname bool) error {
	value = strings.TrimSpace(value)
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%s 不能为空", name)
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("%s 格式应为 host:port：%w", name, err)
	}
	if host != "" {
		if ip := net.ParseIP(host); ip == nil {
			if !allowHostname || strings.ContainsAny(host, " /\\") {
				return fmt.Errorf("%s 的 host 不是合法 IP", name)
			}
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%s 的端口必须在 1-65535", name)
	}
	return nil
}

func validateICEURLs(name string, urls []string, prefixes ...string) error {
	if len(urls) > 8 {
		return fmt.Errorf("%s 最多 8 个地址", name)
	}
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			return fmt.Errorf("%s 不能包含空地址", name)
		}
		lower := strings.ToLower(u)
		ok := false
		for _, p := range prefixes {
			if strings.HasPrefix(lower, p) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%s 地址格式无效: %s", name, raw)
		}
		if strings.ContainsAny(u, " \t\r\n") {
			return fmt.Errorf("%s 地址不能包含空白", name)
		}
	}
	return nil
}
