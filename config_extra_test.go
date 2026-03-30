package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteConfig(t *testing.T) {
	dir := t.TempDir()
	configSubdir := filepath.Join(dir, ".config", "claude-peers")
	configFile := filepath.Join(configSubdir, "config.json")
	t.Setenv("HOME", dir)
	t.Setenv("CLAUDE_PEERS_CONFIG", configFile)

	c := defaultConfig()
	c.Role = "broker"
	c.MachineName = "test-host"

	if err := writeConfig(c); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("config file is empty")
	}
}

func TestConfigDir(t *testing.T) {
	dir := configDir()
	if dir == "" {
		t.Fatal("configDir returned empty string")
	}
}

func TestConfigPath(t *testing.T) {
	// Default path.
	os.Unsetenv("CLAUDE_PEERS_CONFIG")
	p := configPath()
	if p == "" {
		t.Fatal("configPath returned empty string")
	}

	// Override via env.
	t.Setenv("CLAUDE_PEERS_CONFIG", "/tmp/custom-config.json")
	p = configPath()
	if p != "/tmp/custom-config.json" {
		t.Fatalf("expected custom path, got %s", p)
	}
}

func TestIsLocalBroker(t *testing.T) {
	origCfg := cfg
	defer func() { cfg = origCfg }()

	cfg.BrokerURL = "http://127.0.0.1:7899"
	if !isLocalBroker() {
		t.Fatal("expected local broker for 127.0.0.1")
	}

	cfg.BrokerURL = "http://localhost:7899"
	if !isLocalBroker() {
		t.Fatal("expected local broker for localhost")
	}

	cfg.BrokerURL = "http://10.0.0.1:7899"
	if isLocalBroker() {
		t.Fatal("expected remote broker for 10.0.0.1")
	}
}

func TestClaudeProjectKey(t *testing.T) {
	key := claudeProjectKey()
	if key == "" {
		t.Fatal("claudeProjectKey returned empty string")
	}
	if key[0] != '-' {
		t.Fatalf("expected key to start with '-', got %q", key)
	}
}

func TestClaudeMemoryDir(t *testing.T) {
	dir := claudeMemoryDir()
	if dir == "" {
		t.Fatal("claudeMemoryDir returned empty string")
	}
}

func TestUpdateMemoryIndex(t *testing.T) {
	dir := t.TempDir()

	updateMemoryIndex(dir)

	data, err := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("index file is empty")
	}

	updateMemoryIndex(dir)

	data2, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if string(data) != string(data2) {
		t.Fatal("updateMemoryIndex is not idempotent")
	}
}

func TestDefaultDBPath(t *testing.T) {
	p := defaultDBPath()
	if p == "" {
		t.Fatal("defaultDBPath returned empty string")
	}
}

func TestInitConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PEERS_CONFIG", filepath.Join(dir, "nonexistent.json"))
	initConfig()
	if cfg.Role != "client" {
		t.Fatalf("expected default role client, got %s", cfg.Role)
	}
}

func TestLoadConfigAllEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PEERS_CONFIG", filepath.Join(dir, "nonexistent.json"))
	t.Setenv("CLAUDE_PEERS_BROKER_URL", "http://override:7899")
	t.Setenv("CLAUDE_PEERS_LISTEN", "0.0.0.0:9000")
	t.Setenv("CLAUDE_PEERS_MACHINE", "test-machine")
	t.Setenv("CLAUDE_PEERS_DB", "/tmp/test.db")
	t.Setenv("CLAUDE_PEERS_NATS", "nats://nats:4222")
	t.Setenv("CLAUDE_PEERS_LLM_URL", "http://llm:4000/v1")
	t.Setenv("CLAUDE_PEERS_LLM_MODEL", "gpt-4")
	t.Setenv("CLAUDE_PEERS_NATS_TOKEN", "nats-token")
	t.Setenv("CLAUDE_PEERS_LLM_API_KEY", "llm-key")
	t.Setenv("CLAUDE_PEERS_NATS_NKEY", "/tmp/nkey.nk")

	c := loadConfig()

	if c.BrokerURL != "http://override:7899" {
		t.Fatalf("BrokerURL = %s", c.BrokerURL)
	}
	if c.Listen != "0.0.0.0:9000" {
		t.Fatalf("Listen = %s", c.Listen)
	}
	if c.MachineName != "test-machine" {
		t.Fatalf("MachineName = %s", c.MachineName)
	}
	if c.DBPath != "/tmp/test.db" {
		t.Fatalf("DBPath = %s", c.DBPath)
	}
	if c.NatsURL != "nats://nats:4222" {
		t.Fatalf("NatsURL = %s", c.NatsURL)
	}
	if c.LLMBaseURL != "http://llm:4000/v1" {
		t.Fatalf("LLMBaseURL = %s", c.LLMBaseURL)
	}
	if c.LLMModel != "gpt-4" {
		t.Fatalf("LLMModel = %s", c.LLMModel)
	}
	if c.NatsToken != "nats-token" {
		t.Fatalf("NatsToken = %s", c.NatsToken)
	}
	if c.LLMAPIKey != "llm-key" {
		t.Fatalf("LLMAPIKey = %s", c.LLMAPIKey)
	}
	if c.NatsNKeySeed != "/tmp/nkey.nk" {
		t.Fatalf("NatsNKeySeed = %s", c.NatsNKeySeed)
	}
}

func TestLoadAuthToken(t *testing.T) {
	t.Setenv("CLAUDE_PEERS_TOKEN", "my-token")
	got := loadAuthToken()
	if got != "my-token" {
		t.Fatalf("expected my-token, got %s", got)
	}

	os.Unsetenv("CLAUDE_PEERS_TOKEN")
	got = loadAuthToken()
	_ = got
}
