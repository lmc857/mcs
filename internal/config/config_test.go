package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMediaTranscodeDefaultAndOverride(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Media.Transcode {
		t.Fatal("media.transcode must default to false")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("media:\n  transcode: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Media.Transcode {
		t.Fatal("media.transcode override not loaded")
	}
}
