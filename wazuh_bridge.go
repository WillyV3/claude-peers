package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"
)

// --- Wazuh alert types ---

type WazuhAlert struct {
	Timestamp    string         `json:"timestamp"`
	Rule         WazuhRule      `json:"rule"`
	Agent        WazuhAgent     `json:"agent"`
	Manager      WazuhManager   `json:"manager"`
	ID           string         `json:"id"`
	FullLog      string         `json:"full_log"`
	Location     string         `json:"location"`
	SyscheckData *SyscheckEvent `json:"syscheck,omitempty"`
}

type WazuhRule struct {
	Level       int      `json:"level"`
	Description string   `json:"description"`
	ID          string   `json:"id"`
	Groups      []string `json:"groups"`
}

type WazuhAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	IP   string `json:"ip"`
}

type WazuhManager struct {
	Name string `json:"name"`
}

type SyscheckEvent struct {
	Path  string `json:"path"`
	Event string `json:"event"`
}

// --- Security event type (published to NATS) ---

type SecurityEvent struct {
	Type        string   `json:"type"`
	Severity    string   `json:"severity"`
	Level       int      `json:"level"`
	Machine     string   `json:"machine"`
	AgentID     string   `json:"agent_id"`
	RuleID      string   `json:"rule_id"`
	Description string   `json:"description"`
	Timestamp   string   `json:"timestamp"`
	Groups      []string `json:"groups,omitempty"`
	FilePath    string   `json:"file_path,omitempty"`
	SourceIP    string   `json:"source_ip,omitempty"`
}

// runWazuhBridge tails the Wazuh alerts.json file and publishes SecurityEvents to NATS.
func runWazuhBridge(ctx context.Context) error {
	alertsPath := cfg.WazuhAlertsPath
	if v := os.Getenv("WAZUH_ALERTS_PATH"); v != "" {
		alertsPath = v
	}
	if alertsPath == "" {
		alertsPath = "/opt/wazuh-data/logs/alerts/alerts.json"
	}

	log.Printf("[wazuh-bridge] tailing %s", alertsPath)

	pub := newNATSPublisher()
	if pub == nil {
		return errNoNATS
	}
	defer pub.close()

	alerts := make(chan WazuhAlert, 64)
	go tailAlerts(ctx, alertsPath, alerts)

	for {
		select {
		case <-ctx.Done():
			return nil
		case alert := <-alerts:
			subject, _ := classifyAlert(alert)
			event := alertToSecurityEvent(alert)

			data, err := json.Marshal(event)
			if err != nil {
				log.Printf("[wazuh-bridge] marshal error: %v", err)
				continue
			}

			if pub.js != nil {
				if _, err := pub.js.Publish(subject, data); err != nil {
					log.Printf("[wazuh-bridge] publish %s failed: %v", subject, err)
				}
			}

			log.Printf("[wazuh-bridge] %s level=%d agent=%s rule=%s: %s",
				event.Type, event.Level, event.Machine, event.RuleID, event.Description)
		}
	}
}

var errNoNATS = errStr("wazuh-bridge requires NATS connection")

type errStr string

func (e errStr) Error() string { return string(e) }

// tailAlerts opens the alerts file, seeks to the end, and streams new WazuhAlert entries.
// Uses raw Read + line splitting instead of bufio.Scanner to handle continuous append.
func tailAlerts(ctx context.Context, path string, out chan<- WazuhAlert) {
	f, err := os.Open(path)
	if err != nil {
		log.Printf("[wazuh-bridge] open %s: %v (waiting for file...)", path, err)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				f, err = os.Open(path)
				if err == nil {
					goto opened
				}
			}
		}
	}
opened:
	defer f.Close()

	// Seek to end -- we only want new alerts.
	f.Seek(0, 2)

	var currentInode uint64
	if info, err := f.Stat(); err == nil {
		currentInode = inodeFromInfo(info)
	}

	buf := make([]byte, 0, 256*1024)
	readBuf := make([]byte, 64*1024)
	checkRotation := time.NewTicker(10 * time.Second)
	defer checkRotation.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, _ := f.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
			// Process complete lines.
			for {
				idx := -1
				for i, b := range buf {
					if b == '\n' {
						idx = i
						break
					}
				}
				if idx < 0 {
					break
				}
				line := strings.TrimSpace(string(buf[:idx]))
				buf = buf[idx+1:]
				if line == "" {
					continue
				}
				var alert WazuhAlert
				if err := json.Unmarshal([]byte(line), &alert); err != nil {
					continue
				}
				select {
				case out <- alert:
				case <-ctx.Done():
					return
				}
			}
			continue
		}

		// No new data -- check for rotation or sleep.
		select {
		case <-ctx.Done():
			return
		case <-checkRotation.C:
			newInfo, err := os.Stat(path)
			if err != nil {
				continue
			}
			newInode := inodeFromInfo(newInfo)
			if newInode != currentInode && currentInode != 0 {
				log.Printf("[wazuh-bridge] file rotated, reopening")
				f.Close()
				f, err = os.Open(path)
				if err != nil {
					log.Printf("[wazuh-bridge] reopen failed: %v", err)
					return
				}
				if info, err := f.Stat(); err == nil {
					currentInode = inodeFromInfo(info)
				}
				buf = buf[:0]
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// classifyAlert determines the NATS subject and security event type from a Wazuh alert.
func classifyAlert(alert WazuhAlert) (subject string, secType string) {
	for _, g := range alert.Rule.Groups {
		gl := strings.ToLower(g)
		if strings.Contains(gl, "quarantine") {
			return "fleet.security.quarantine", "quarantine"
		}
	}
	for _, g := range alert.Rule.Groups {
		gl := strings.ToLower(g)
		if strings.Contains(gl, "syscheck") || strings.Contains(gl, "fim") {
			return "fleet.security.fim", "fim"
		}
	}
	for _, g := range alert.Rule.Groups {
		gl := strings.ToLower(g)
		if strings.Contains(gl, "authentication") || strings.Contains(gl, "sshd") ||
			strings.Contains(gl, "pam") || strings.Contains(gl, "sudo") {
			return "fleet.security.auth", "auth"
		}
	}
	for _, g := range alert.Rule.Groups {
		gl := strings.ToLower(g)
		if strings.Contains(gl, "process") || strings.Contains(gl, "new_port") {
			return "fleet.security.process", "process"
		}
	}
	for _, g := range alert.Rule.Groups {
		gl := strings.ToLower(g)
		if strings.Contains(gl, "network") || strings.Contains(gl, "non_tailscale") {
			return "fleet.security.network", "network"
		}
	}
	return "fleet.security.alert", "general"
}

// severityFromLevel maps Wazuh rule levels (1-15) to severity strings.
func severityFromLevel(level int) string {
	switch {
	case level <= 5:
		return "info"
	case level <= 9:
		return "warning"
	case level <= 12:
		return "critical"
	default:
		return "quarantine"
	}
}

// normalizeAgentName maps Wazuh agent names to fleet machine names.
func normalizeAgentName(name string) string {
	// The manager's own alerts use "wazuh.manager" -- map to actual hostname.
	if name == "wazuh.manager" {
		return cfg.MachineName
	}
	// Strip .local suffix from macOS agents.
	return strings.TrimSuffix(name, ".local")
}

// alertToSecurityEvent converts a WazuhAlert into a SecurityEvent.
func alertToSecurityEvent(alert WazuhAlert) SecurityEvent {
	_, secType := classifyAlert(alert)
	event := SecurityEvent{
		Type:        secType,
		Severity:    severityFromLevel(alert.Rule.Level),
		Level:       alert.Rule.Level,
		Machine:     normalizeAgentName(alert.Agent.Name),
		AgentID:     alert.Agent.ID,
		RuleID:      alert.Rule.ID,
		Description: alert.Rule.Description,
		Timestamp:   alert.Timestamp,
		Groups:      alert.Rule.Groups,
	}
	if alert.SyscheckData != nil {
		event.FilePath = alert.SyscheckData.Path
	}
	if alert.Agent.IP != "" {
		event.SourceIP = alert.Agent.IP
	}
	return event
}
