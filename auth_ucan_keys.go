package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	privateKeyFile = "identity.pem"
	publicKeyFile  = "identity.pub"
	rootPubKeyFile = "root.pub"
	tokenFile      = "token.jwt"
	rootTokenFile  = "root-token.jwt"
)

func GenerateKeyPair() (KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return KeyPair{PrivateKey: priv, PublicKey: pub}, nil
}

func SaveKeyPair(kp KeyPair, dir string) error {
	privBytes, err := x509.MarshalPKCS8PrivateKey(kp.PrivateKey)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privBytes,
	})
	if err := os.WriteFile(filepath.Join(dir, privateKeyFile), privPEM, 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	pubBytes, err := x509.MarshalPKIXPublicKey(kp.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal public key: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	if err := os.WriteFile(filepath.Join(dir, publicKeyFile), pubPEM, 0644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}

	return nil
}

func LoadKeyPair(dir string) (KeyPair, error) {
	privData, err := os.ReadFile(filepath.Join(dir, privateKeyFile))
	if err != nil {
		return KeyPair{}, fmt.Errorf("read private key: %w", err)
	}
	privBlock, _ := pem.Decode(privData)
	if privBlock == nil {
		return KeyPair{}, fmt.Errorf("decode private key PEM")
	}
	privKey, err := x509.ParsePKCS8PrivateKey(privBlock.Bytes)
	if err != nil {
		return KeyPair{}, fmt.Errorf("parse private key: %w", err)
	}
	edPriv, ok := privKey.(ed25519.PrivateKey)
	if !ok {
		return KeyPair{}, fmt.Errorf("private key is not ed25519")
	}

	pubData, err := os.ReadFile(filepath.Join(dir, publicKeyFile))
	if err != nil {
		return KeyPair{}, fmt.Errorf("read public key: %w", err)
	}
	pubBlock, _ := pem.Decode(pubData)
	if pubBlock == nil {
		return KeyPair{}, fmt.Errorf("decode public key PEM")
	}
	pubKey, err := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if err != nil {
		return KeyPair{}, fmt.Errorf("parse public key: %w", err)
	}
	edPub, ok := pubKey.(ed25519.PublicKey)
	if !ok {
		return KeyPair{}, fmt.Errorf("public key is not ed25519")
	}

	return KeyPair{PrivateKey: edPriv, PublicKey: edPub}, nil
}

func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("decode public key PEM")
	}
	pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	edPub, ok := pubKey.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not ed25519")
	}
	return edPub, nil
}

func SavePublicKey(pub ed25519.PublicKey, path string) error {
	pubBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return fmt.Errorf("marshal public key: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	return os.WriteFile(path, pubPEM, 0644)
}

func SaveToken(token string, dir string) error {
	return os.WriteFile(filepath.Join(dir, tokenFile), []byte(token), 0600)
}

func LoadToken(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, tokenFile))
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func SaveRootToken(token string, dir string) error {
	return os.WriteFile(filepath.Join(dir, rootTokenFile), []byte(token), 0600)
}

func LoadRootToken(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, rootTokenFile))
	if err != nil {
		return "", fmt.Errorf("read root token: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
