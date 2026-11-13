package client

import (
	"encoding/json"
	"fmt"
	"os"
)

// FileConfig is the JSON shape accepted by --config. Pointer fields distinguish
// "absent" from an explicit empty value (e.g. "socks": "" disables SOCKS).
//
//	{
//	  "dsn": "postgres://pgtun:pass@vps:5432/postgres?sslmode=require",
//	  "socks": "127.0.0.1:1080",
//	  "ss": "0.0.0.0:8388",
//	  "ss_method": "chacha20-ietf-poly1305",
//	  "ss_password": "secret",
//	  "max_streams": 256,
//	  "public_host": "203.0.113.10",
//	  "tag": "pgtun"
//	}
type FileConfig struct {
	DSN        *string `json:"dsn"`
	Socks      *string `json:"socks"`
	SS         *string `json:"ss"`
	SSMethod   *string `json:"ss_method"`
	SSPassword *string `json:"ss_password"`
	MaxStreams *int    `json:"max_streams"`
	PublicHost *string `json:"public_host"`
	Tag        *string `json:"tag"`
}

// LoadConfigFile reads and parses a JSON client config.
func LoadConfigFile(path string) (FileConfig, error) {
	var cfg FileConfig

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}
