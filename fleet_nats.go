package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	natsDefaultURL = "nats://127.0.0.1:4222"
	streamName     = "FLEET"
)

// Fleet event subjects. All events go under fleet.>
// fleet.peer.joined, fleet.peer.left, fleet.commit, fleet.summary, fleet.message, etc.
var fleetSubjects = []string{"fleet.>"}

func natsURL() string {
	if cfg.NatsURL != "" {
		return cfg.NatsURL
	}
	if cfg.BrokerURL != "" {
		host := cfg.BrokerURL
		for _, prefix := range []string{"http://", "https://"} {
			if len(host) > len(prefix) && host[:len(prefix)] == prefix {
				host = host[len(prefix):]
			}
		}
		for i, c := range host {
			if c == ':' {
				host = host[:i]
				break
			}
		}
		return "nats://" + host + ":4222"
	}
	return natsDefaultURL
}

// FleetEvent is published to NATS when something happens in the fleet.
type FleetEvent struct {
	Type      string `json:"type"`
	PeerID    string `json:"peer_id,omitempty"`
	Machine   string `json:"machine,omitempty"`
	Summary   string `json:"summary,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	Data      string `json:"data,omitempty"`
	Timestamp string `json:"timestamp"`
}

// NATSPublisher connects to NATS and publishes fleet events.
// Used by the broker to dual-write events to both SQLite and NATS.
type NATSPublisher struct {
	nc *nats.Conn
	js nats.JetStreamContext
}

func newNATSPublisher() *NATSPublisher {
	opts := natsConnectOptions("claude-peers-broker")
	nc, err := nats.Connect(natsURL(), opts...)
	if err != nil {
		log.Printf("[nats] connect failed (non-fatal, events will be SQLite-only): %v", err)
		return nil
	}

	js, err := nc.JetStream()
	if err != nil {
		log.Printf("[nats] jetstream init failed: %v", err)
		nc.Close()
		return nil
	}

	// Ensure the FLEET stream exists -- create if missing, update if config changed.
	streamCfg := &nats.StreamConfig{
		Name:              streamName,
		Subjects:          fleetSubjects,
		Retention:         nats.LimitsPolicy,
		MaxAge:            24 * time.Hour,
		MaxBytes:          256 * 1024 * 1024, // 256MB
		MaxMsgsPerSubject: 10000,
		Storage:           nats.FileStorage,
		Duplicates:        2 * time.Minute,
		Discard:           nats.DiscardOld,
		MaxMsgSize:        32 * 1024, // 32KB per message cap
	}
	if _, err = js.StreamInfo(streamName); err != nil {
		if _, err = js.AddStream(streamCfg); err != nil {
			log.Printf("[nats] stream create failed: %v", err)
			nc.Close()
			return nil
		}
	} else {
		// Update existing stream to pick up config changes
		if _, err = js.UpdateStream(streamCfg); err != nil {
			log.Printf("[nats] stream update: %v (non-fatal)", err)
		}
	}

	// Clean up stale consumers (inactive > 24h)
	cleanupStaleConsumers(js)

	log.Printf("[nats] connected to %s, stream %s ready", natsURL(), streamName)
	return &NATSPublisher{nc: nc, js: js}
}

func cleanupStaleConsumers(js nats.JetStreamContext) {
	consumers := js.ConsumerNames(streamName)
	for name := range consumers {
		info, err := js.ConsumerInfo(streamName, name)
		if err != nil {
			continue
		}
		// Delete consumers inactive for > 24h
		if info.NumPending == 0 && info.Delivered.Stream > 0 {
			lastActive := info.Delivered.Last
			if lastActive != nil && time.Since(*lastActive) > 24*time.Hour {
				if err := js.DeleteConsumer(streamName, name); err == nil {
					log.Printf("[nats] deleted stale consumer: %s", name)
				}
			}
		}
	}
}

func (p *NATSPublisher) publish(subject string, event FleetEvent) {
	if p == nil {
		return
	}
	event.Timestamp = nowISO()
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	if _, err := p.js.Publish(subject, data); err != nil {
		log.Printf("[nats] publish %s failed: %v", subject, err)
	}
}

func (p *NATSPublisher) close() {
	if p != nil && p.nc != nil {
		p.nc.Drain()
		p.nc.Close()
	}
}

// subscribeFleet subscribes to the FLEET JetStream with a named durable consumer.
// Each caller should use a unique consumerName to avoid conflicts.
func subscribeFleet(consumerName string, handler func(FleetEvent), opts ...nats.SubOpt) (*nats.Conn, error) {
	subOpts := natsConnectOptions("claude-peers-" + consumerName)
	nc, err := nats.Connect(natsURL(), subOpts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}

	// Durable consumer -- default to DeliverNew (only see events published after subscription).
	subOpt := []nats.SubOpt{nats.Durable(consumerName), nats.DeliverNew()}
	if len(opts) > 0 {
		subOpt = append(subOpt[:1], opts...) // keep durable, replace delivery policy
	}
	_, err = js.Subscribe("fleet.>", func(msg *nats.Msg) {
		var event FleetEvent
		if json.Unmarshal(msg.Data, &event) == nil {
			handler(event)
		}
		msg.Ack()
	}, subOpt...)

	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("subscribe: %w", err)
	}

	return nc, nil
}
