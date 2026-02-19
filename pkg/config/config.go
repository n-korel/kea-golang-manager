package config

import "time"

// Config представляет конфигурацию приложения
type Config struct {
	PrimaryURL string
	StandbyURL string
	Timeout    time.Duration
}

// New создает новую конфигурацию приложения
func New(primaryURL, standbyURL string, timeout time.Duration) *Config {
	return &Config{
		PrimaryURL: primaryURL,
		StandbyURL: standbyURL,
		Timeout:    timeout,
	}
}
