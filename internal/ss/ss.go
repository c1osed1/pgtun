// Package ss runs a Shadowsocks AEAD server on top of go-shadowsocks2.
package ss

import (
	"context"
	"log"
	"net"
	"time"

	"github.com/shadowsocks/go-shadowsocks2/core"
	ssocks "github.com/shadowsocks/go-shadowsocks2/socks"
)

// Serve accepts Shadowsocks AEAD connections on ln, decrypts the target
// address and calls handle with the decrypted connection.
func Serve(ctx context.Context, ln net.Listener, method, password string, handle func(target string, conn net.Conn)) error {
	ciph, err := core.PickCipher(method, nil, password)
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			log.Printf("pgtun: ss accept: %v", err)
			return err
		}
		go func(c net.Conn) {
			defer c.Close()
			rc := ciph.StreamConn(c)
			addr, err := ssocks.ReadAddr(rc)
			if err != nil {
				log.Printf("pgtun: ss read target from %s: %v", c.RemoteAddr(), err)
				return
			}
			handle(addr.String(), rc)
		}(c)
	}
}
