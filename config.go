package main

import (
	"fmt"
	"net/url"
	"os"
	"time"
)

type Config struct {
	OllamaURL    string
	Addr         string
	FastModel    string
	QualityModel string
}

func loadConfig() (Config, error) {
	ollamaURL := os.Getenv("OLLAMA_URL")
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}
	// Loopback by default: the console returns prompt history verbatim and can
	// start GPU work, so a fresh install must not be reachable from the LAN.
	// Publishing it is an explicit act: ADDR=0.0.0.0:8080 (and, for the console,
	// CONSOLE_REMOTE=true).
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	fast := os.Getenv("FAST_MODEL")
	if fast == "" {
		fast = "qwen2.5:3b-instruct"
	}
	quality := os.Getenv("QUALITY_MODEL")
	if quality == "" {
		quality = "qwen2.5:7b-instruct-q4_K_M"
	}
	if ollamaURL == "" || addr == "" {
		return Config{}, fmt.Errorf("invalid config")
	}
	return Config{OllamaURL: ollamaURL, Addr: addr, FastModel: fast, QualityModel: quality}, nil
}

// envOr returns the environment variable or a default when it is unset or empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// gpuPollInterval bounds how often nvidia-smi is asked; 10s keeps a model swap
// visible without paying the ~100ms subprocess cost more than six times a minute.
func gpuPollInterval() time.Duration {
	d, err := time.ParseDuration(envOr("GPU_POLL_INTERVAL", "10s"))
	if err != nil || d < time.Second {
		return 10 * time.Second
	}
	return d
}

// gpuHistoryKeep bounds how much history gpu_samples retains; 168h is a week of
// one sample per ten seconds, past which the chart's window cannot reach.
func gpuHistoryKeep() time.Duration {
	d, err := time.ParseDuration(envOr("GPU_HISTORY_KEEP", "168h"))
	if err != nil || d < time.Hour {
		return 168 * time.Hour
	}
	return d
}

// defaultDSN assembles the local-dev Postgres DSN from the same POSTGRES_* variables
// docker-compose.yml uses, so `docker compose up postgres` and `go run .` agree without
// any configuration. POSTGRES_DSN, when set, wins outright (see main.go).
func defaultDSN() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(envOr("POSTGRES_USER", "tower"), envOr("POSTGRES_PASSWORD", "tower")),
		Host:   envOr("POSTGRES_HOST", "localhost") + ":" + envOr("POSTGRES_PORT", "5432"),
		Path:   "/" + envOr("POSTGRES_DB", "tower"),
	}
	return u.String()
}
