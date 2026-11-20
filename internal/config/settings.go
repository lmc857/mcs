package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ServerSettings are the allowlisted fields that can be edited from the
// web console. They are persisted in SQLite and applied on process start.
type ServerSettings struct {
	Advertise   string `json:"advertise"`
	SIPUDP      string `json:"sip_udp"`
	HTTP        string `json:"http"`
	HTTPS       string `json:"https"`
	Domain      string `json:"domain"`
	AlarmNumber string `json:"alarm_number"`
	AutoAnswer  string `json:"auto_answer"`
	Transcode   bool   `json:"transcode"`
}

func (c Config) ServerSettings() ServerSettings {
	return ServerSettings{
		Advertise:   c.Server.Advertise,
		SIPUDP:      c.Server.SIPUDP,
		HTTP:        c.Server.HTTP,
		HTTPS:       c.Server.HTTPS,
		Domain:      c.Server.Domain,
		AlarmNumber: c.Alarm.Number,
		AutoAnswer:  c.Broadcast.AutoAnswer,
		Transcode:   c.Media.Transcode,
	}
}

func (c *Config) ApplyServerSettings(v ServerSettings) error {
	if err := ValidateServerSettings(v); err != nil {
		return err
	}
	c.Server.Advertise = v.Advertise
	c.Server.SIPUDP = v.SIPUDP
	c.Server.HTTP = v.HTTP
	c.Server.HTTPS = v.HTTPS
	c.Server.Domain = v.Domain
	c.Alarm.Number = v.AlarmNumber
	c.Broadcast.AutoAnswer = v.AutoAnswer
	c.Media.Transcode = v.Transcode
	return nil
}

func (v ServerSettings) Equal(w ServerSettings) bool { return v == w }

func ValidateServerSettings(v ServerSettings) error {
	if err := validateHostPort("advertise", v.Advertise, false, true); err != nil {
		return err
	}
	if err := validateHostPort("sip_udp", v.SIPUDP, false, false); err != nil {
		return err
	}
	if err := validateHostPort("http", v.HTTP, false, false); err != nil {
		return err
	}
	if err := validateHostPort("https", v.HTTPS, true, false); err != nil {
		return err
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
