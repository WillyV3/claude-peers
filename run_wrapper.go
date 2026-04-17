package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// buildRunArgv computes the argv, env overrides, and skip conditions for
// `claude-peers run <args...>`. Pure function so callers can unit-test the
// flag handling without actually execing claude.
//
// Behavior:
//   - If the user's args contain -p, --print, --print=..., or already contain
//     --dangerously-load-development-channels (with or without =value), the
//     channel flag is NOT injected. -p invocations are daemon/print calls
//     where adding the channel flag has no benefit. An already-present
//     channel flag means the caller knows what they're doing.
//   - Otherwise, --dangerously-load-development-channels server:claude-peers
//     is inserted at the FRONT of the user args (right after argv[0] = "claude")
//     so it sits before any --print or positional args the user may have.
//   - `--as <name>` and `--as=<name>` are parsed out of the user args and
//     become CLAUDE_PEERS_AGENT=<name> in the env override slice. Claude
//     itself doesn't understand --as, so we must strip it before forwarding.
//   - All other args pass through verbatim.
//
// argv[0] is set to "claude" by convention (not the full path) so tools that
// inspect process name (ps, pgrep) show "claude" instead of the resolved
// path. The actual binary to exec is passed separately as execPath.
func buildRunArgv(execPath string, userArgs []string) (argv []string, envOverrides []string) {
	const channelFlag = "--dangerously-load-development-channels"
	const channelValue = "server:claude-peers"

	skipChannel := false
	hasChannelFlag := false
	passthrough := make([]string, 0, len(userArgs))

	i := 0
	for i < len(userArgs) {
		a := userArgs[i]
		switch {
		case a == "-p", a == "--print", strings.HasPrefix(a, "--print="):
			skipChannel = true
			passthrough = append(passthrough, a)
			i++
		case a == channelFlag:
			hasChannelFlag = true
			passthrough = append(passthrough, a)
			if i+1 < len(userArgs) {
				passthrough = append(passthrough, userArgs[i+1])
				i += 2
			} else {
				i++
			}
		case strings.HasPrefix(a, channelFlag+"="):
			hasChannelFlag = true
			passthrough = append(passthrough, a)
			i++
		case a == "--as":
			if i+1 >= len(userArgs) {
				// Preserve the malformed flag so the user gets a clear error
				// from the downstream parser instead of silent swallowing.
				passthrough = append(passthrough, a)
				i++
				continue
			}
			envOverrides = append(envOverrides, "CLAUDE_PEERS_AGENT="+userArgs[i+1])
			i += 2
		case strings.HasPrefix(a, "--as="):
			envOverrides = append(envOverrides, "CLAUDE_PEERS_AGENT="+strings.TrimPrefix(a, "--as="))
			i++
		default:
			passthrough = append(passthrough, a)
			i++
		}
	}

	argv = []string{"claude"}
	if !hasChannelFlag && !skipChannel {
		argv = append(argv, channelFlag, channelValue)
	}
	argv = append(argv, passthrough...)
	// Reference execPath so callers who want the "name as it appears to the
	// process" semantics know argv[0] is decoupled from the exec'd binary.
	_ = execPath
	return argv, envOverrides
}

// runClaudeWrapped is the `claude-peers run` subcommand. Resolves the
// `claude` binary in PATH and replaces the current process with it, injecting
// the dev-channel flag unless the user opted out. On success never returns
// (syscall.Exec replaces the process image). On failure prints to stderr
// and exits non-zero.
//
// Why exec-replace instead of spawn+wait: the wrapper is a thin shim. Leaving
// a parent `claude-peers` process alive adds a PID to ps, intercepts signals,
// and complicates exit-code propagation. Exec-replace is the standard Unix
// wrapper pattern and matches how shell shebang interpreters work.
func runClaudeWrapped(userArgs []string) {
	execPath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude-peers run: 'claude' binary not found in PATH.")
		fmt.Fprintln(os.Stderr, "  Install Claude Code or ensure it's on your PATH, then re-run.")
		os.Exit(1)
	}

	argv, envOverrides := buildRunArgv(execPath, userArgs)

	env := os.Environ()
	env = append(env, envOverrides...)

	if err := syscall.Exec(execPath, argv, env); err != nil {
		fmt.Fprintf(os.Stderr, "claude-peers run: exec %s failed: %v\n", execPath, err)
		os.Exit(1)
	}
}
