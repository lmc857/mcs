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
