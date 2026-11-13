package sub

import (
	"encoding/base64"
	"net"
	"testing"
)

func TestURI(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		port     int
		method   string
		password string
		tag      string
		want     string
	}{
		{
			name:     "base64url credentials and tag",
			host:     "192.0.2.10",
			port:     8388,
			method:   "chacha20-ietf-poly1305",
			password: "secret",
			tag:      "home",
			want:     "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpzZWNyZXQ@192.0.2.10:8388#home",
		},
		{
			name:     "ipv6 host without tag",
			host:     "2001:db8::1",
			port:     8388,
			method:   "aes-256-gcm",
			password: "p@ss:word",
			tag:      "",
			want:     "ss://YWVzLTI1Ni1nY206cEBzczp3b3Jk@[2001:db8::1]:8388",
		},
		{
			name:     "empty password has no base64 padding",
			host:     "127.0.0.1",
			port:     1080,
			method:   "chacha20-ietf-poly1305",
			password: "",
			tag:      "",
			want:     "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTo@127.0.0.1:1080",
		},
		{
			name:     "tag is path escaped",
			host:     "203.0.113.5",
			port:     8388,
			method:   "aes-256-gcm",
			password: "pw",
			tag:      "home 2",
			want:     "ss://YWVzLTI1Ni1nY206cHc@203.0.113.5:8388#home%202",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := URI(tt.host, tt.port, tt.method, tt.password, tt.tag)
			if got != tt.want {
				t.Fatalf("URI() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSubscription(t *testing.T) {
	uri := "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpzZWNyZXQ@192.0.2.10:8388#home"

	got := Subscription(uri)
	want := "c3M6Ly9ZMmhoWTJoaE1qQXRhV1YwWmkxd2IyeDVNVE13TlRwelpXTnlaWFFAMTkyLjAuMi4xMDo4Mzg4I2hvbWUK"
	if got != want {
		t.Fatalf("Subscription() = %q, want %q", got, want)
	}

	decoded, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("decode subscription: %v", err)
	}
	if string(decoded) != uri+"\n" {
		t.Fatalf("decoded subscription = %q, want %q", string(decoded), uri+"\n")
	}
}

func TestParseListenAddr(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{name: "wildcard ipv4", addr: "0.0.0.0:8388", wantHost: "0.0.0.0", wantPort: 8388},
		{name: "loopback", addr: "127.0.0.1:8388", wantHost: "127.0.0.1", wantPort: 8388},
		{name: "ipv6", addr: "[::1]:8388", wantHost: "::1", wantPort: 8388},
		{name: "port zero", addr: "127.0.0.1:0", wantHost: "127.0.0.1", wantPort: 0},
		{name: "missing port", addr: "0.0.0.0", wantErr: true},
		{name: "non numeric port", addr: "0.0.0.0:http", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := ParseListenAddr(tt.addr)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseListenAddr(%q) error = nil, want error", tt.addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseListenAddr(%q) error = %v", tt.addr, err)
			}
			if host != tt.wantHost || port != tt.wantPort {
				t.Fatalf("ParseListenAddr(%q) = (%q, %d), want (%q, %d)",
					tt.addr, host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestResolveHost(t *testing.T) {
	t.Run("override wins", func(t *testing.T) {
		if got := ResolveHost("0.0.0.0", "203.0.113.10"); got != "203.0.113.10" {
			t.Fatalf("ResolveHost() = %q, want override", got)
		}
	})

	t.Run("concrete host kept", func(t *testing.T) {
		if got := ResolveHost("192.0.2.10", ""); got != "192.0.2.10" {
			t.Fatalf("ResolveHost() = %q, want 192.0.2.10", got)
		}
	})

	t.Run("wildcard falls back to detected host", func(t *testing.T) {
		detected := DetectHost()
		if ip := net.ParseIP(detected); ip == nil {
			t.Fatalf("DetectHost() = %q, want a valid IP (fallback is 127.0.0.1)", detected)
		}
		for _, wildcard := range []string{"", "0.0.0.0", "::", "[::]"} {
			if got := ResolveHost(wildcard, ""); got != detected {
				t.Fatalf("ResolveHost(%q, \"\") = %q, want %q", wildcard, got, detected)
			}
		}
	})
}
