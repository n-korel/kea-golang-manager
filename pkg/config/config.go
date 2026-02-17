package config

import (
	"flag"
	"time"
)

// Config представляет конфигурацию приложения
type Config struct {
	KeaURL   string
	Timeout  time.Duration
}

// Load загружает конфигурацию из флагов
func Load() *Config {
	cfg := &Config{}
	
	flag.StringVar(&cfg.KeaURL, "kea-url", "http://localhost:8000", "Kea Control Agent URL")
	duration := flag.Duration("timeout", 10*time.Second, "HTTP request timeout")
	
	flag.Parse()
	
	cfg.Timeout = *duration
	return cfg
}
