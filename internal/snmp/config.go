package snmp

import (
	"os"
	"strconv"
	"time"
)

// Env-переменные для SNMP (snmp_rules: credentials from environment).
const (
	EnvTarget    = "SNMP_TARGET"
	EnvPort      = "SNMP_PORT"
	EnvVersion   = "SNMP_VERSION"
	EnvCommunity = "SNMP_COMMUNITY"
	EnvV3User    = "SNMP_V3_USER"
	EnvV3AuthPass = "SNMP_V3_AUTH_PASS"
	EnvV3PrivPass = "SNMP_V3_PRIV_PASS"
)

// Config — конфигурация SNMP-поллера из env (без хардкода секретов).
type Config struct {
	Target       string
	Port         uint16
	Version      string
	Community    string
	V3User       string
	V3AuthPass   string
	V3PrivPass   string
	PollInterval time.Duration
	Timeout      time.Duration
}

// ConfigFromEnv заполняет Config из переменных окружения.
func ConfigFromEnv() Config {
	port := 161
	if p := os.Getenv(EnvPort); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			port = v
		}
	}
	version := os.Getenv(EnvVersion)
	if version == "" {
		version = "3"
	}
	interval := 60 * time.Second
	if s := os.Getenv("SNMP_POLL_INTERVAL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			interval = d
		}
	}
	timeout := 5 * time.Second
	if s := os.Getenv("SNMP_TIMEOUT"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			timeout = d
		}
	}
	return Config{
		Target:       os.Getenv(EnvTarget),
		Port:         uint16(port),
		Version:      version,
		Community:    os.Getenv(EnvCommunity),
		V3User:       os.Getenv(EnvV3User),
		V3AuthPass:   os.Getenv(EnvV3AuthPass),
		V3PrivPass:   os.Getenv(EnvV3PrivPass),
		PollInterval: interval,
		Timeout:      timeout,
	}
}
