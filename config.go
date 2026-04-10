package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config holds all runtime configuration for claude-peers.
// Loaded once at startup from file -> env overrides -> defaults.
type Config struct {
	// Role determines whether this instance runs a broker or connects to one.
	// "broker" = run the HTTP broker daemon locally.
	// "client" = connect to an existing broker (default).
	Role string `json:"role"`

	// BrokerURL is the HTTP endpoint of the broker.
	// Clients use this to register, send messages, etc.
	BrokerURL string `json:"broker_url"`

	// Listen is the address the broker binds to.
	// Only used when Role is "broker".
	// Use "0.0.0.0:7899" or a Tailscale IP to accept remote peers.
	Listen string `json:"listen"`

	// MachineName identifies this machine in the peer network.
	// Defaults to os.Hostname().
	MachineName string `json:"machine_name"`

	// DBPath is the SQLite database path for the broker.
	// Defaults to ~/.claude-peers.db.
	DBPath string `json:"db_path"`

	// StaleTimeout is how many seconds without a heartbeat before
	// a peer is considered stale and removed. Defaults to 300.
	StaleTimeout int `json:"stale_timeout"`

	// NatsURL is the NATS server address. Defaults to deriving from BrokerURL.
	NatsURL string `json:"nats_url"`

	// NatsToken is the auth token for NATS server connections.
	NatsToken string `json:"nats_token"`

	// NatsNKeySeed is the path to a NATS NKey seed file for per-machine auth.
	// Takes priority over NatsToken when set.
	NatsNKeySeed string `json:"nats_nkey_seed"`

	// LLMBaseURL is the OpenAI-compatible LLM endpoint for auto-summary generation.
	LLMBaseURL string `json:"llm_base_url"`

	// LLMModel is the default model for summary generation.
	LLMModel string `json:"llm_model"`

	// LLMAPIKey is the API key for the LLM endpoint (used for summary generation).
	LLMAPIKey string `json:"llm_api_key"`

	// DefaultChildTTL is the default lifetime for child tokens minted by the
	// broker — used by both `issue-token` (when no --ttl flag is passed) and
	// by the POST /refresh-token endpoint. Accepts Go durations ("24h",
	// "72h30m") or day shorthand ("30d"). Defaults to "24h" when unset for
	// backward compatibility with pre-T3 installs. Set to "30d" or similar
	// to stop the rotation treadmill for always-on peers.
	DefaultChildTTL string `json:"default_child_ttl"`
}

// cfg is the global config, loaded once at startup.
var cfg Config

func initConfig() {
	cfg = loadConfig()
}

func loadConfig() Config {
	c := defaultConfig()

	if data, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(data, &c)
	}

	// Env overrides take priority over config file.
	if v := os.Getenv("CLAUDE_PEERS_BROKER_URL"); v != "" {
		c.BrokerURL = v
	}
	if v := os.Getenv("CLAUDE_PEERS_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("CLAUDE_PEERS_MACHINE"); v != "" {
		c.MachineName = v
	}
	if v := os.Getenv("CLAUDE_PEERS_DB"); v != "" {
		c.DBPath = v
	}

	if v := os.Getenv("CLAUDE_PEERS_NATS"); v != "" {
		c.NatsURL = v
	}
	if v := os.Getenv("CLAUDE_PEERS_LLM_URL"); v != "" {
		c.LLMBaseURL = v
	}
	if v := os.Getenv("CLAUDE_PEERS_LLM_MODEL"); v != "" {
		c.LLMModel = v
	}
	if v := os.Getenv("CLAUDE_PEERS_NATS_TOKEN"); v != "" {
		c.NatsToken = v
	}
	if v := os.Getenv("CLAUDE_PEERS_LLM_API_KEY"); v != "" {
		c.LLMAPIKey = v
	}
	if v := os.Getenv("CLAUDE_PEERS_NATS_NKEY"); v != "" {
		c.NatsNKeySeed = v
	}
	if v := os.Getenv("CLAUDE_PEERS_DEFAULT_TTL"); v != "" {
		c.DefaultChildTTL = v
	}

	// Legacy env var
	if v := os.Getenv("CLAUDE_PEERS_PORT"); v != "" {
		c.Listen = "127.0.0.1:" + v
		if c.BrokerURL == defaultConfig().BrokerURL {
			c.BrokerURL = "http://127.0.0.1:" + v
		}
	}

	return c
}

func defaultConfig() Config {
	hostname, _ := os.Hostname()
	return Config{
		Role:         "client",
		BrokerURL:    "http://127.0.0.1:7899",
		Listen:       "127.0.0.1:7899",
		MachineName:  hostname,
		DBPath:       defaultDBPath(),
		StaleTimeout:    20,
		NatsURL:         "",
		LLMBaseURL:      "http://127.0.0.1:4000/v1",
		LLMModel:        "claude-haiku",
		DefaultChildTTL: "24h",
	}
}

func defaultDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-peers.db")
}

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "claude-peers")
}

func configPath() string {
	if p := os.Getenv("CLAUDE_PEERS_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(configDir(), "config.json")
}

// writeConfig writes a config to the standard config path.
func writeConfig(c Config) error {
	dir := configDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0644)
}

// cliInit generates a config file for broker or client role.
func cliInit(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `Usage:
  claude-peers init broker                  Set up as broker (generates keypair + root token)
  claude-peers init client <broker-url>     Connect to a remote broker

Examples:
  claude-peers init broker
  claude-peers init client http://your-server:7899`)
		os.Exit(1)
	}

	c := defaultConfig()
	dir := configDir()
	os.MkdirAll(dir, 0755)

	switch args[0] {
	case "broker":
		c.Role = "broker"
		c.Listen = "0.0.0.0:7899"
		c.BrokerURL = "http://127.0.0.1:7899"

		kp, err := GenerateKeyPair()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating keypair: %v\n", err)
			os.Exit(1)
		}
		if err := SaveKeyPair(kp, dir); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving keypair: %v\n", err)
			os.Exit(1)
		}
		if err := SavePublicKey(kp.PublicKey, filepath.Join(dir, rootPubKeyFile)); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving root public key: %v\n", err)
			os.Exit(1)
		}

		token, err := MintRootToken(kp.PrivateKey, AllCapabilities(), 365*24*time.Hour)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error minting root token: %v\n", err)
			os.Exit(1)
		}
		if err := SaveRootToken(token, dir); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving root token: %v\n", err)
			os.Exit(1)
		}
		// Also save to token.jwt for backward compat (broker uses root token as its own auth).
		if err := SaveToken(token, dir); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving token: %v\n", err)
			os.Exit(1)
		}

	case "client":
		c.Role = "client"
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Error: client requires a broker URL")
			fmt.Fprintln(os.Stderr, "  claude-peers init client http://<broker-ip>:7899")
			os.Exit(1)
		}
		c.BrokerURL = args[1]

		kp, err := GenerateKeyPair()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating keypair: %v\n", err)
			os.Exit(1)
		}
		if err := SaveKeyPair(kp, dir); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving keypair: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown role: %s (use 'broker' or 'client')\n", args[0])
		os.Exit(1)
	}

	if err := writeConfig(c); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Config written to %s\n\n", configPath())
	fmt.Printf("  role:         %s\n", c.Role)
	fmt.Printf("  machine_name: %s\n", c.MachineName)
	fmt.Printf("  broker_url:   %s\n", c.BrokerURL)
	fmt.Printf("  identity:     %s\n", filepath.Join(dir, privateKeyFile))
	fmt.Printf("  public key:   %s\n", filepath.Join(dir, publicKeyFile))
	if c.Role == "broker" {
		fmt.Printf("  root.pub:     %s\n", filepath.Join(dir, rootPubKeyFile))
		fmt.Printf("  root token:   %s\n", filepath.Join(dir, rootTokenFile))
		fmt.Printf("  token:        %s\n", filepath.Join(dir, tokenFile))
		fmt.Printf("  listen:       %s\n", c.Listen)
		fmt.Printf("  db_path:      %s\n", c.DBPath)
	}
	fmt.Println()

	if c.Role == "broker" {
		fmt.Println("Start the broker with: claude-peers broker")
	} else {
		fmt.Println("Next steps:")
		fmt.Println("  1. Copy root.pub from broker machine to " + filepath.Join(dir, rootPubKeyFile))
		fmt.Println("  2. On the broker, run: claude-peers issue-token <this-machine.pub> peer-session")
		fmt.Println("  3. Save the issued token: claude-peers save-token <jwt>")
	}
}
