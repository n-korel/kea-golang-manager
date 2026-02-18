package config

import "time"

// Config представляет конфигурацию приложения
type Config struct {
	KeaURL  string
	Timeout time.Duration
}

// New создает новую конфигурацию приложения
func New(keaURL string, timeout time.Duration) *Config {
	return &Config{
		KeaURL:  keaURL,
		Timeout: timeout,
	}
}
