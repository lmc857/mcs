package config

import "testing"

func TestValidateServerSettings(t *testing.T) {
	base := ServerSettings{
		Advertise: "192.168.1.10:5060", SIPUDP: "0.0.0.0:5060", HTTP: "0.0.0.0:8080",
		Domain: "mcs.local", AlarmNumber: "8000", AutoAnswer: "<http://x>;info=alert-autoanswer",
	}
	if err := ValidateServerSettings(base); err != nil {
		t.Fatal(err)
	}
	bad := base
	bad.HTTP = "localhost"
	if err := ValidateServerSettings(bad); err == nil {
		t.Fatal("expected invalid http")
	}
	bad = base
	bad.Advertise = ""
	if err := ValidateServerSettings(bad); err == nil {
		t.Fatal("expected empty advertise error")
	}
}

func TestApplyServerSettings(t *testing.T) {
	cfg := Config{}
	v := ServerSettings{
		Advertise: "192.168.1.10:5060", SIPUDP: "0.0.0.0:5060", HTTP: "0.0.0.0:8080",
		Domain: "mcs.local", AlarmNumber: "8000", AutoAnswer: "<http://x>;info=alert-autoanswer",
		Transcode: true,
	}
	if err := cfg.ApplyServerSettings(v); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Advertise != v.Advertise || !cfg.Media.Transcode {
		t.Fatalf("not applied: %+v", cfg.ServerSettings())
	}
}

func TestValidateICESettings(t *testing.T) {
	base := ServerSettings{
		Advertise: "192.168.1.10:5060", SIPUDP: "0.0.0.0:5060", HTTP: "0.0.0.0:8080",
		Domain: "mcs.local", AlarmNumber: "8000", AutoAnswer: "<http://x>;info=alert-autoanswer",
		STUNURLs:     []string{"stun:stun.example.com:3478"},
		TURNURLs:     []string{"turn:turn.example.com:3478?transport=udp"},
		TURNUsername: "mcs", TURNPassword: "secret", WebRTCPublicIP: "203.0.113.10",
	}
	if err := ValidateServerSettings(base); err != nil {
		t.Fatal(err)
	}
	bad := base
	bad.TURNURLs = []string{"https://turn.example.com"}
	if err := ValidateServerSettings(bad); err == nil {
		t.Fatal("accepted non-turn URL")
	}
	bad = base
	bad.TURNPassword = ""
	if err := ValidateServerSettings(bad); err == nil {
		t.Fatal("accepted TURN without password")
	}
	bad = base
	bad.WebRTCPublicIP = "not-an-ip"
	if err := ValidateServerSettings(bad); err == nil {
		t.Fatal("accepted invalid public IP")
	}
}

func TestApplyICESettings(t *testing.T) {
	cfg := Config{}
	v := ServerSettings{
		Advertise: "192.168.1.10:5060", SIPUDP: "0.0.0.0:5060", HTTP: "0.0.0.0:8080",
		Domain: "mcs.local", AlarmNumber: "8000", AutoAnswer: "<http://x>;info=alert-autoanswer",
		STUNURLs:     []string{"stun:stun.example.com:3478"},
		TURNURLs:     []string{"turn:turn.example.com:3478?transport=udp"},
		TURNUsername: "mcs", TURNPassword: "secret", WebRTCPublicIP: "203.0.113.10",
	}
	if err := cfg.ApplyServerSettings(v); err != nil {
		t.Fatal(err)
	}
	if len(cfg.WebRTC.ICEServers) != 2 || cfg.WebRTC.PublicIP != v.WebRTCPublicIP {
		t.Fatalf("webrtc config = %+v", cfg.WebRTC)
	}
	round := cfg.ServerSettings()
	if round.TURNUsername != "mcs" || round.TURNPassword != "secret" || len(round.TURNURLs) != 1 {
		t.Fatalf("roundtrip = %+v", round)
	}
}
