package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DaemonConfig defines a daemon from its directory.
type DaemonConfig struct {
	Name       string
	Dir        string
	AgentFile  string
	ConfigFile string
	PolicyFile string
	Schedule   string // "cron:*/15 * * * *" or "event:fleet.peer.joined" or "interval:5m"
}

// DaemonRun tracks a single daemon invocation.
type DaemonRun struct {
	Daemon    string    `json:"daemon"`
	StartedAt time.Time `json:"started_at"`
	Duration  string    `json:"duration"`
	Trigger   string    `json:"trigger"`
	Status    string    `json:"status"` // "running", "complete", "failed"
	Output    string    `json:"output"`
}

// Supervisor manages daemon lifecycle and NATS subscriptions.
type Supervisor struct {
	daemons    []DaemonConfig
	agentBin   string
	mu         sync.Mutex
	running    map[string]bool
	history    []DaemonRun
	maxHistory int
}

func newSupervisor(daemonDir, agentBin string) (*Supervisor, error) {
	daemons, err := discoverDaemons(daemonDir)
	if err != nil {
		return nil, err
	}
	return &Supervisor{
		daemons:    daemons,
		agentBin:   agentBin,
		running:    make(map[string]bool),
		maxHistory: 100,
	}, nil
}

func discoverDaemons(dir string) ([]DaemonConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read daemon dir %s: %w", dir, err)
	}

	var daemons []DaemonConfig
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dDir := filepath.Join(dir, e.Name())

		// Find .agent file
		agentFile := ""
		files, _ := os.ReadDir(dDir)
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".agent") {
				agentFile = filepath.Join(dDir, f.Name())
				break
			}
		}
		if agentFile == "" {
			continue
		}

		// Read schedule from daemon.json if it exists
		schedule := "interval:15m" // default
		djson := filepath.Join(dDir, "daemon.json")
		if data, err := os.ReadFile(djson); err == nil {
			var dc struct {
				Schedule string `json:"schedule"`
			}
			json.Unmarshal(data, &dc)
			if dc.Schedule != "" {
				schedule = dc.Schedule
			}
		}

		configFile := filepath.Join(dDir, "agent.toml")
		if _, err := os.Stat(configFile); err != nil {
			configFile = ""
		}

		policyFile := filepath.Join(dDir, "policy.toml")
		if _, err := os.Stat(policyFile); err != nil {
			policyFile = ""
		}

		daemons = append(daemons, DaemonConfig{
			Name:       e.Name(),
			Dir:        dDir,
			AgentFile:  agentFile,
			ConfigFile: configFile,
			PolicyFile: policyFile,
			Schedule:   schedule,
		})
	}
	return daemons, nil
}

func (s *Supervisor) run(ctx context.Context) {
	log.Printf("[supervisor] discovered %d daemon(s)", len(s.daemons))
	for _, d := range s.daemons {
		log.Printf("[supervisor]   %s (%s)", d.Name, d.Schedule)
	}

	var wg sync.WaitGroup

	for _, d := range s.daemons {
		d := d

		if strings.HasPrefix(d.Schedule, "event:") {
			subject := strings.TrimPrefix(d.Schedule, "event:")
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.watchNATS(ctx, d, subject)
			}()
		} else if strings.HasPrefix(d.Schedule, "interval:") {
			durStr := strings.TrimPrefix(d.Schedule, "interval:")
			dur, err := time.ParseDuration(durStr)
			if err != nil {
				log.Printf("[supervisor] %s: bad interval %q: %v", d.Name, durStr, err)
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.runOnInterval(ctx, d, dur)
			}()
		}
	}

	wg.Wait()
}

func (s *Supervisor) watchNATS(ctx context.Context, d DaemonConfig, subject string) {
	nc, err := subscribeDream(func(event FleetEvent) {
		if !matchSubject(subject, event.Type) {
			return
		}
		s.invoke(d, fmt.Sprintf("nats:%s", event.Type))
	})
	if err != nil {
		log.Printf("[supervisor] %s: NATS subscribe failed: %v, falling back to interval", d.Name, err)
		s.runOnInterval(ctx, d, 15*time.Minute)
		return
	}
	defer nc.Close()
	<-ctx.Done()
}

func matchSubject(pattern, eventType string) bool {
	if pattern == "fleet.>" || pattern == ">" {
		return true
	}
	return strings.Contains(eventType, strings.TrimSuffix(pattern, ".>"))
}

func (s *Supervisor) runOnInterval(ctx context.Context, d DaemonConfig, interval time.Duration) {
	// Run once immediately
	s.invoke(d, "startup")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.invoke(d, "interval")
		}
	}
}

func (s *Supervisor) invoke(d DaemonConfig, trigger string) {
	s.mu.Lock()
	if s.running[d.Name] {
		s.mu.Unlock()
		log.Printf("[supervisor] %s: already running, skipping", d.Name)
		return
	}
	s.running[d.Name] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.running, d.Name)
		s.mu.Unlock()
	}()

	run := DaemonRun{
		Daemon:    d.Name,
		StartedAt: time.Now(),
		Trigger:   trigger,
		Status:    "running",
	}

	log.Printf("[supervisor] %s: invoking (trigger: %s)", d.Name, trigger)

	args := []string{"run", d.AgentFile}
	if d.ConfigFile != "" {
		args = append(args, "--config", d.ConfigFile)
	}
	if d.PolicyFile != "" {
		args = append(args, "--policy", d.PolicyFile)
	}
	args = append(args, "--workspace", filepath.Join(os.TempDir(), "daemon-"+d.Name))

	cmd := exec.Command(s.agentBin, args...)
	cmd.Dir = d.Dir
	cmd.Env = append(os.Environ(),
		"OPENAI_API_KEY="+os.Getenv("OPENAI_API_KEY"),
	)

	output, err := cmd.CombinedOutput()

	run.Duration = time.Since(run.StartedAt).Round(time.Second).String()
	run.Output = string(output)

	if err != nil {
		run.Status = "failed"
		log.Printf("[supervisor] %s: failed after %s: %v", d.Name, run.Duration, err)
	} else {
		run.Status = "complete"
		log.Printf("[supervisor] %s: complete in %s", d.Name, run.Duration)
	}

	// Publish result to NATS
	if cfg.BrokerURL != "" {
		publishDaemonResult(d.Name, run)
	}

	s.mu.Lock()
	s.history = append(s.history, run)
	if len(s.history) > s.maxHistory {
		s.history = s.history[len(s.history)-s.maxHistory:]
	}
	s.mu.Unlock()
}

func publishDaemonResult(name string, run DaemonRun) {
	data, _ := json.Marshal(run)
	cliFetch("/events", nil, nil) // Just to keep broker aware
	_ = data                      // TODO: publish to NATS fleet.daemon.<name>.result
}

func cliSupervisor(ctx context.Context) {
	daemonDir := filepath.Join(findRepoRoot(), "daemons")
	if _, err := os.Stat(daemonDir); err != nil {
		// Try relative to binary
		exe, _ := os.Executable()
		daemonDir = filepath.Join(filepath.Dir(exe), "daemons")
	}
	if envDir := os.Getenv("CLAUDE_PEERS_DAEMONS"); envDir != "" {
		daemonDir = envDir
	}

	agentBin := os.Getenv("AGENT_BIN")
	if agentBin == "" {
		agentBin, _ = exec.LookPath("agent")
	}
	if agentBin == "" {
		home, _ := os.UserHomeDir()
		agentBin = filepath.Join(home, "projects", "vinay-agent", "bin", "agent")
	}

	if _, err := os.Stat(agentBin); err != nil {
		log.Fatalf("[supervisor] agent binary not found at %s", agentBin)
	}

	log.Printf("[supervisor] daemon dir: %s", daemonDir)
	log.Printf("[supervisor] agent bin: %s", agentBin)

	s, err := newSupervisor(daemonDir, agentBin)
	if err != nil {
		log.Fatal(err)
	}

	s.run(ctx)
}

func findRepoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "daemons")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
