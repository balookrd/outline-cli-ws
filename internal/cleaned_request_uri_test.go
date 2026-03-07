//go:build !unit

package internal

import (
	"net/url"
	"testing"
)

func TestCleanedRequestURI_StripsHealthcheckControlParams(t *testing.T) {
	u, err := url.Parse("wss://edge.example.com/maiRfy1HEEkssRrSfffYu8/tcp?h3=1&hc_path=%2Fhealth%2Ftcp&health_path=%2Fh&test_path=%2Ft&foo=bar")
	if err != nil {
		t.Fatal(err)
	}
	got := cleanedRequestURI(u)
	if got != "/maiRfy1HEEkssRrSfffYu8/tcp?foo=bar" {
		t.Fatalf("cleanedRequestURI=%q", got)
	}
}
