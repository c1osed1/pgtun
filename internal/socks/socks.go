// Package socks implements a minimal SOCKS5 CONNECT server (RFC 1928).
package socks

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"time"
)

const handshakeTimeout = 30 * time.Second

// Serve accepts SOCKS5 connections on ln and calls handle for every successful
// CONNECT. It returns when ln is closed (normally after ctx is cancelled).
func Serve(ctx context.Context, ln net.Listener, handle func(target string, conn net.Conn)) {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			log.Printf("pgtun: socks accept: %v", err)
			return
		}
		go serveConn(c, handle)
	}
}

func serveConn(c net.Conn, handle func(target string, conn net.Conn)) {
	target, err := handshake(c)
	if err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Printf("pgtun: socks handshake from %s: %v", c.RemoteAddr(), err)
		}
		_ = c.Close()
		return
	}
	handle(target, c)
	_ = c.Close()
}

func handshake(c net.Conn) (string, error) {
	_ = c.SetDeadline(time.Now().Add(handshakeTimeout))
	defer c.SetDeadline(time.Time{}) //nolint:errcheck

	// Greeting: VER, NMETHODS, METHODS...
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return "", err
	}
	if hdr[0] != 0x05 {
		return "", fmt.Errorf("unsupported version %d", hdr[0])
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", err
	}
	noAuth := false
	for _, m := range methods {
		if m == 0x00 {
			noAuth = true
			break
		}
	}
	if !noAuth {
		_, _ = c.Write([]byte{0x05, 0xff})
		return "", fmt.Errorf("no acceptable auth method")
	}
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return "", err
	}

	// Request: VER, CMD, RSV, ATYP, DST.ADDR, DST.PORT
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return "", err
	}
	if req[0] != 0x05 {
		return "", fmt.Errorf("unsupported request version %d", req[0])
	}
	if req[1] != 0x01 { // CONNECT only
		_, _ = c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return "", fmt.Errorf("unsupported command %d", req[1])
	}

	var host string
	switch req[3] {
	case 0x01: // IPv4
		var ip [4]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return "", err
		}
		host = net.IP(ip[:]).String()
	case 0x04: // IPv6
		var ip [16]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return "", err
		}
		host = net.IP(ip[:]).String()
	case 0x03: // domain
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return "", err
		}
		if l[0] == 0 {
			_, _ = c.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return "", fmt.Errorf("empty domain")
		}
		name := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, name); err != nil {
			return "", err
		}
		host = string(name)
	default:
		_, _ = c.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return "", fmt.Errorf("unsupported address type %d", req[3])
	}

	var port [2]byte
	if _, err := io.ReadFull(c, port[:]); err != nil {
		return "", err
	}
	portNum := binary.BigEndian.Uint16(port[:])

	// Success reply with BND.ADDR 0.0.0.0:0.
	if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(portNum))), nil
}
