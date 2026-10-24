package main

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port          string
	Version       string
	Color         string
	Message       string
	ConfigDir     string
	SecretDir     string
	StartupDelay  time.Duration
	ShutdownDelay time.Duration
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getenvSeconds(key string, def float64) time.Duration {
	v, err := strconv.ParseFloat(getenv(key, ""), 64)
	if err != nil {
		v = def
	}
	return time.Duration(v * float64(time.Second))
}

func loadConfig() Config {
	return Config{
		Port:          getenv("PORT", "8080"),
		Version:       getenv("APP_VERSION", "v1"),
		Color:         getenv("APP_COLOR", "#3b82f6"),
		Message:       getenv("APP_MESSAGE", "Hello from KubePlay!"),
		ConfigDir:     getenv("CONFIG_DIR", "/etc/kubeplay/config"),
		SecretDir:     getenv("SECRET_DIR", "/etc/kubeplay/secrets"),
		StartupDelay:  getenvSeconds("STARTUP_DELAY_SECONDS", 0),
		ShutdownDelay: getenvSeconds("SHUTDOWN_DELAY_SECONDS", 5),
	}
}
