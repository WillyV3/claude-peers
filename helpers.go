package main

import (
	"context"
	"log"
	"os/exec"
	"time"
)

// sendEmail sends an email via resend-email CLI. Non-blocking (runs in goroutine).
func sendEmail(to, subject, body string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "resend-email", "-m", body, to, subject)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[email] send failed: %v: %s", err, string(out))
		}
	}()
}

// sshRun executes a command on a remote machine via SSH. Returns stdout+stderr.
func sshRun(target, command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=no",
		"-o", "BatchMode=yes",
		target, command)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
