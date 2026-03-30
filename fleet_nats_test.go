package main

import (
	"testing"
)

func TestNatsURL(t *testing.T) {
	origCfg := cfg
	defer func() { cfg = origCfg }()

	// Explicit NatsURL takes priority.
	cfg.NatsURL = "nats://custom:4222"
	cfg.BrokerURL = "http://10.0.0.1:7899"
	if got := natsURL(); got != "nats://custom:4222" {
		t.Fatalf("expected custom URL, got %s", got)
	}

	// Derives from BrokerURL when NatsURL is empty.
	cfg.NatsURL = ""
	cfg.BrokerURL = "http://10.0.0.1:7899"
	if got := natsURL(); got != "nats://10.0.0.1:4222" {
		t.Fatalf("expected derived URL, got %s", got)
	}

	// HTTPS prefix.
	cfg.NatsURL = ""
	cfg.BrokerURL = "https://broker.example.com:7899"
	if got := natsURL(); got != "nats://broker.example.com:4222" {
		t.Fatalf("expected derived HTTPS URL, got %s", got)
	}

	// Default fallback.
	cfg.NatsURL = ""
	cfg.BrokerURL = ""
	if got := natsURL(); got != "nats://127.0.0.1:4222" {
		t.Fatalf("expected default URL, got %s", got)
	}
}

func TestNilPublisherPublish(t *testing.T) {
	// Nil publisher should not panic.
	var p *NATSPublisher
	p.publish("fleet.test", FleetEvent{Type: "test"})
}

func TestNilPublisherClose(t *testing.T) {
	// Nil publisher close should not panic.
	var p *NATSPublisher
	p.close()
}
