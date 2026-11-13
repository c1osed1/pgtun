// Package sub builds Shadowsocks SIP002 subscription links and renders them as
// terminal QR codes. It is shared by `pgtun client` and `--print-sub`.
package sub

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"

	qrcode "github.com/skip2/go-qrcode"
)

// DetectHost returns the first non-loopback local IP that would be used to
// reach the internet. It falls back to 127.0.0.1.
func DetectHost() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()

	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil {
		if ip := addr.IP.String(); ip != "" {
			return ip
		}
	}
	return "127.0.0.1"
}

// ResolveHost picks the host advertised in the subscription: an explicit
// override wins, then a concrete listen host, otherwise the detected LAN/public
// IP (wildcard listeners such as 0.0.0.0 and :: are replaced).
func ResolveHost(listenHost, override string) string {
	if override != "" {
		return override
	}
	switch listenHost {
	case "", "0.0.0.0", "::", "[::]":
		return DetectHost()
	}
	return listenHost
}

// URI builds a SIP002 ss:// URI:
//
//	ss://<base64url(method:password)>@<host>:<port>#<tag>
func URI(host string, port int, method, password, tag string) string {
	cred := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password))
	uri := "ss://" + cred + "@" + net.JoinHostPort(host, strconv.Itoa(port))
	if tag != "" {
		uri += "#" + url.PathEscape(tag)
	}
	return uri
}

// Subscription returns the base64 (std encoding, no line breaks) body of a
// one-entry subscription: the URI followed by a newline.
func Subscription(uri string) string {
	return base64.StdEncoding.EncodeToString([]byte(uri + "\n"))
}

// Print writes the ss:// URI, the base64 subscription line and a QR code for
// the URI to w. It is meant for stdout; logs stay on stderr.
func Print(w io.Writer, host string, port int, method, password, tag string) error {
	uri := URI(host, port, method, password, tag)

	if _, err := fmt.Fprintln(w, uri); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, Subscription(uri)); err != nil {
		return err
	}

	code, err := qrcode.New(uri, qrcode.Medium)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, code.ToString(false))
	return err
}

// ParseListenAddr splits "host:port" into host and port. An empty host means a
// wildcard listener. The port is required.
func ParseListenAddr(addr string) (string, int, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q: %w", portText, err)
	}
	return host, port, nil
}
