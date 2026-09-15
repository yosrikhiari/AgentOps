package main

import (
	"fmt"
	"net/url"
	"os"
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
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
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

// defaultDSN assembles the local-dev Postgres DSN from the same POSTGRES_* variables
// docker-compose.yml uses, so `docker compose up postgres` and `go run .` agree without
// any configuration. POSTGRES_DSN, when set, wins outright (see main.go).
func defaultDSN() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(envOr("POSTGRES_USER", "agentops"), envOr("POSTGRES_PASSWORD", "agentops")),
		Host:   envOr("POSTGRES_HOST", "localhost") + ":" + envOr("POSTGRES_PORT", "5432"),
		Path:   "/" + envOr("POSTGRES_DB", "agentops"),
	}
	return u.String()
}
