package config

import "time"

// Config представляет конфигурацию приложения
type Config struct {
	KeaURL        string
	KeaPrimaryURL string
	KeaStandbyURL string
	Timeout       time.Duration
	HAPollInterval time.Duration
	HAMinFailures  int
}

// New создает новую конфигурацию приложения
func New(keaURL string, timeout time.Duration) *Config {
	return &Config{
		KeaURL:         keaURL,
		KeaPrimaryURL:  keaURL,
		KeaStandbyURL:  "",
		Timeout:        timeout,
		HAPollInterval: 5 * time.Second,
		HAMinFailures:  3,
	}
}
