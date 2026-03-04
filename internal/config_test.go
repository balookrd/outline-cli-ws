package internal

import "testing"

func TestNormalizeHostPort(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"1.1.1.1:53", "1.1.1.1:53"},
		{"example.com:53", "example.com:53"},
		{"[2606:4700:4700::1111]:53", "[2606:4700:4700::1111]:53"},
		// IPv6 literal without brackets should be normalized.
		{"2606:4700:4700::1111:53", "[2606:4700:4700::1111]:53"},
		// No port: cannot normalize.
		{"2606:4700:4700::1111", "2606:4700:4700::1111"},
		// Empty port: cannot normalize.
		{"2606:4700:4700::1111:", "2606:4700:4700::1111:"},
	}

	for _, tc := range cases {
		got := normalizeHostPort(tc.in)
		if got != tc.want {
			t.Fatalf("normalizeHostPort(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
