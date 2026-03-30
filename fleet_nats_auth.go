package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

const nkeyFile = "nats.nk"

// generateNKeySeed creates a new NATS user NKey pair.
func generateNKeySeed() (seed []byte, pubKey string, err error) {
	user, err := nkeys.CreateUser()
	if err != nil {
		return nil, "", fmt.Errorf("create nkey user: %w", err)
	}
	seed, err = user.Seed()
	if err != nil {
		return nil, "", fmt.Errorf("get seed: %w", err)
	}
	pub, err := user.PublicKey()
	if err != nil {
		return nil, "", fmt.Errorf("get public key: %w", err)
	}
	return seed, pub, nil
}

// loadNKeySeed reads an NKey seed from file.
func loadNKeySeed(dir string) ([]byte, error) {
	path := filepath.Join(dir, nkeyFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(string(data))), nil
}

// saveNKeySeed writes an NKey seed to file with restricted permissions.
func saveNKeySeed(seed []byte, dir string) error {
	path := filepath.Join(dir, nkeyFile)
	return os.WriteFile(path, seed, 0600)
}

// nkeyOptionFromSeed returns a NATS auth option using an NKey seed.
func nkeyOptionFromSeed(seed []byte) nats.Option {
	return nats.Option(func(o *nats.Options) error {
		kp, err := nkeys.FromSeed(seed)
		if err != nil {
			return fmt.Errorf("nkey from seed: %w", err)
		}
		pub, err := kp.PublicKey()
		if err != nil {
			return fmt.Errorf("nkey public key: %w", err)
		}
		o.Nkey = pub
		seedCopy := make([]byte, len(seed))
		copy(seedCopy, seed)
		o.SignatureCB = func(nonce []byte) ([]byte, error) {
			kp2, err := nkeys.FromSeed(seedCopy)
			if err != nil {
				return nil, err
			}
			defer kp2.Wipe()
			return kp2.Sign(nonce)
		}
		return nil
	})
}

// natsAuthOption returns the appropriate NATS auth option.
// Priority: NatsNKeySeed config path > default nkey file > NatsToken > no auth.
func natsAuthOption() nats.Option {
	// Try NKey seed from explicit config path.
	if cfg.NatsNKeySeed != "" {
		seed, err := os.ReadFile(cfg.NatsNKeySeed)
		if err == nil {
			trimmed := []byte(strings.TrimSpace(string(seed)))
			if len(trimmed) > 0 {
				return nkeyOptionFromSeed(trimmed)
			}
		}
	}
	// Try NKey from default location.
	seed, err := loadNKeySeed(configDir())
	if err == nil && len(seed) > 0 {
		return nkeyOptionFromSeed(seed)
	}
	// Fall back to token auth.
	if cfg.NatsToken != "" {
		return nats.Token(cfg.NatsToken)
	}
	return nil
}

// natsConnectOptions returns the standard NATS connection options including auth.
func natsConnectOptions(name string) []nats.Option {
	opts := []nats.Option{
		nats.Name(name),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
	}
	if auth := natsAuthOption(); auth != nil {
		opts = append(opts, auth)
	}
	return opts
}

// cliGenerateNKey generates a new NATS NKey and saves it.
func cliGenerateNKey() {
	dir := configDir()
	os.MkdirAll(dir, 0755)

	seed, pubKey, err := generateNKeySeed()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating NKey: %v\n", err)
		os.Exit(1)
	}

	if err := saveNKeySeed(seed, dir); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving NKey: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("NKey generated:\n")
	fmt.Printf("  Seed:       %s\n", filepath.Join(dir, nkeyFile))
	fmt.Printf("  Public key: %s\n", pubKey)
	fmt.Printf("\nAdd this public key to the NATS server config.\n")
}
