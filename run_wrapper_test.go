package main

import (
	"reflect"
	"testing"
)

// T9: `claude-peers run` subcommand wraps claude with the dev-channel flag.
// These tests cover the pure argv/env construction logic; the exec itself
// is untested (it replaces the process, can't assert in-process).

func TestBuildRunArgv_InjectsChannelWhenBare(t *testing.T) {
	argv, env := buildRunArgv("/path/to/claude", nil)
	want := []string{"claude", "--dangerously-load-development-channels", "server:claude-peers"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, want)
	}
	if len(env) != 0 {
		t.Fatalf("expected no env overrides, got %v", env)
	}
}

func TestBuildRunArgv_PassesThroughUserArgs(t *testing.T) {
	argv, _ := buildRunArgv("/path/to/claude", []string{"--continue"})
	want := []string{"claude", "--dangerously-load-development-channels", "server:claude-peers", "--continue"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, want)
	}
}

func TestBuildRunArgv_PrintShortFlagSkipsChannel(t *testing.T) {
	// -p is the daemon/print path. Channel flag has no value there and
	// adds noise, so we skip injection.
	argv, _ := buildRunArgv("/path/to/claude", []string{"-p", "summarize this"})
	want := []string{"claude", "-p", "summarize this"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, want)
	}
}

func TestBuildRunArgv_PrintLongFlagSkipsChannel(t *testing.T) {
	argv, _ := buildRunArgv("/path/to/claude", []string{"--print", "hello"})
	want := []string{"claude", "--print", "hello"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, want)
	}
}

func TestBuildRunArgv_PrintEqualsFormSkipsChannel(t *testing.T) {
	argv, _ := buildRunArgv("/path/to/claude", []string{"--print=hello world"})
	want := []string{"claude", "--print=hello world"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, want)
	}
}

func TestBuildRunArgv_AlreadyHasChannelFlagNoDuplicateInjection(t *testing.T) {
	in := []string{"--dangerously-load-development-channels", "server:somethingelse", "hello"}
	argv, _ := buildRunArgv("/path/to/claude", in)
	want := []string{"claude", "--dangerously-load-development-channels", "server:somethingelse", "hello"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, want)
	}
}

func TestBuildRunArgv_AlreadyHasChannelFlagEqualsForm(t *testing.T) {
	in := []string{"--dangerously-load-development-channels=server:other"}
	argv, _ := buildRunArgv("/path/to/claude", in)
	want := []string{"claude", "--dangerously-load-development-channels=server:other"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, want)
	}
}

func TestBuildRunArgv_AsFlagExtractedToEnv(t *testing.T) {
	argv, env := buildRunArgv("/path/to/claude", []string{"--as", "jim", "--continue"})
	wantArgv := []string{"claude", "--dangerously-load-development-channels", "server:claude-peers", "--continue"}
	if !reflect.DeepEqual(argv, wantArgv) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, wantArgv)
	}
	wantEnv := []string{"CLAUDE_PEERS_AGENT=jim"}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("env:\n got  %v\n want %v", env, wantEnv)
	}
}

func TestBuildRunArgv_AsFlagEqualsForm(t *testing.T) {
	argv, env := buildRunArgv("/path/to/claude", []string{"--as=mark", "--continue"})
	wantArgv := []string{"claude", "--dangerously-load-development-channels", "server:claude-peers", "--continue"}
	if !reflect.DeepEqual(argv, wantArgv) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, wantArgv)
	}
	wantEnv := []string{"CLAUDE_PEERS_AGENT=mark"}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("env:\n got  %v\n want %v", env, wantEnv)
	}
}

func TestBuildRunArgv_AsFlagMissingValuePreservedInArgv(t *testing.T) {
	// --as at end with no value: don't swallow, let downstream error cleanly.
	argv, env := buildRunArgv("/path/to/claude", []string{"--continue", "--as"})
	wantArgv := []string{"claude", "--dangerously-load-development-channels", "server:claude-peers", "--continue", "--as"}
	if !reflect.DeepEqual(argv, wantArgv) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, wantArgv)
	}
	if len(env) != 0 {
		t.Fatalf("malformed --as should set no env overrides, got %v", env)
	}
}

func TestBuildRunArgv_AsWithPrintCombinesCorrectly(t *testing.T) {
	// --as still strips even when -p is present. Channel flag does not
	// inject (because of -p). CLAUDE_PEERS_AGENT still gets set because a
	// user might want a named session for a one-shot print call.
	argv, env := buildRunArgv("/path/to/claude", []string{"--as", "keeper", "-p", "status"})
	wantArgv := []string{"claude", "-p", "status"}
	if !reflect.DeepEqual(argv, wantArgv) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, wantArgv)
	}
	wantEnv := []string{"CLAUDE_PEERS_AGENT=keeper"}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("env:\n got  %v\n want %v", env, wantEnv)
	}
}

func TestBuildRunArgv_PreservesUnknownFlags(t *testing.T) {
	// Anything claude-peers doesn't recognize passes straight through.
	argv, _ := buildRunArgv("/path/to/claude", []string{"--model", "opus-4-7", "--resume", "abc123"})
	want := []string{
		"claude",
		"--dangerously-load-development-channels", "server:claude-peers",
		"--model", "opus-4-7",
		"--resume", "abc123",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv:\n got  %v\n want %v", argv, want)
	}
}
