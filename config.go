package main

import (
	"fmt"
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
