//go:build !unit

package internal

import (
	"net/url"
	"testing"
)

func TestH3HealthcheckURL_UsesHealthPathOverride(t *testing.T) {
	u, err := url.Parse("wss://example.com/live/tcp?h3=1&hc_path=%2Fprobe%2Fws&origin=https%3A%2F%2Fclient")
	if err != nil {
		t.Fatal(err)
	}
	got := h3HealthcheckURL(u)
	if got.Path != "/probe/ws" {
		t.Fatalf("path=%q", got.Path)
	}
	q := got.Query()
	if q.Get("hc_path") != "" {
		t.Fatalf("hc_path should be removed")
	}
	if q.Get("origin") == "" {
		t.Fatalf("origin should be preserved")
	}
}

func TestH3HealthcheckURL_LeavesPathWhenNoOverride(t *testing.T) {
	u, err := url.Parse("wss://example.com/live/tcp?h3=1")
	if err != nil {
		t.Fatal(err)
	}
	got := h3HealthcheckURL(u)
	if got.Path != "/live/tcp" {
		t.Fatalf("path=%q", got.Path)
	}
}
