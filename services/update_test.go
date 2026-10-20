package services

import (
	"strings"
	"testing"
)

func TestUpdateServiceVersion(t *testing.T) {
	service := NewUpdateService(nil, "1.2.3", true)
	if got := service.Version(); got != "1.2.3" {
		t.Fatalf("Version() = %q, want %q", got, "1.2.3")
	}
}

func TestUpdateServiceRejectsLocalBuild(t *testing.T) {
	service := NewUpdateService(nil, "0.0.0", false)
	err := service.CheckForUpdates()
	if err == nil {
		t.Fatal("CheckForUpdates() = nil, want a local build to be rejected")
	}
	if !strings.Contains(err.Error(), "本地构建") {
		t.Fatalf("CheckForUpdates() error = %q, want the local build message", err)
	}
}
