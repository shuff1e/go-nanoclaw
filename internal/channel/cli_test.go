package channel

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"go-nanoclaw/internal/brain"
	"go-nanoclaw/internal/config"
	"go-nanoclaw/internal/gateway"
)

func TestCLIPlanCommandPersistsPlanExecuteMode(t *testing.T) {
	gw := newCLITestGateway(t)
	ch := NewCLIChannel(gw, "main")

	captureStdout(t, func() {
		cont, err := ch.handleCommand(context.Background(), "/plan Analyze this. Then implement it.")
		if err != nil {
			t.Fatalf("handle plan command: %v", err)
		}
		if !cont {
			t.Fatal("expected chat loop to continue")
		}
	})

	plans, err := gw.PlanRecords("main", 10)
	if err != nil {
		t.Fatalf("load plans: %v", err)
	}
	if len(plans) == 0 {
		t.Fatal("expected persisted plan")
	}
	last := plans[len(plans)-1]
	if last.Mode != "plan_execute" || last.Source != "cli" || last.SessionID != "cli:main" {
		t.Fatalf("unexpected plan record: %+v", last)
	}
}

func TestCLIVerifyCommandPersistsPlanExecuteVerifyMode(t *testing.T) {
	gw := newCLITestGateway(t)
	ch := NewCLIChannel(gw, "main")

	captureStdout(t, func() {
		cont, err := ch.handleCommand(context.Background(), "/verify Analyze this. Then implement it.")
		if err != nil {
			t.Fatalf("handle verify command: %v", err)
		}
		if !cont {
			t.Fatal("expected chat loop to continue")
		}
	})

	plans, err := gw.PlanRecords("main", 10)
	if err != nil {
		t.Fatalf("load plans: %v", err)
	}
	if len(plans) == 0 {
		t.Fatal("expected persisted plan")
	}
	last := plans[len(plans)-1]
	if last.Mode != "plan_execute_verify" || last.Status != "completed" {
		t.Fatalf("unexpected verified plan record: %+v", last)
	}
}

func TestCLIPlanCommandRequiresTask(t *testing.T) {
	gw := newCLITestGateway(t)
	ch := NewCLIChannel(gw, "main")

	output := captureStdout(t, func() {
		cont, err := ch.handleCommand(context.Background(), "/plan")
		if err != nil {
			t.Fatalf("handle empty plan command: %v", err)
		}
		if !cont {
			t.Fatal("expected chat loop to continue")
		}
	})

	if !strings.Contains(output, "Usage: /plan <task>") {
		t.Fatalf("expected usage output, got %q", output)
	}
	plans, err := gw.PlanRecords("main", 10)
	if err != nil {
		t.Fatalf("load plans: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("expected no plan for empty task, got %+v", plans)
	}
}

func TestCLIUnknownCommandListsPlanCommands(t *testing.T) {
	gw := newCLITestGateway(t)
	ch := NewCLIChannel(gw, "main")

	output := captureStdout(t, func() {
		cont, err := ch.handleCommand(context.Background(), "/nope")
		if err != nil {
			t.Fatalf("handle unknown command: %v", err)
		}
		if !cont {
			t.Fatal("expected chat loop to continue")
		}
	})

	if !strings.Contains(output, "/plan") || !strings.Contains(output, "/verify") {
		t.Fatalf("expected plan commands in help output, got %q", output)
	}
}

func newCLITestGateway(t *testing.T) *gateway.Gateway {
	t.Helper()

	cfg := config.NewConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.Agents["main"].Heartbeat.Enabled = false
	gw := gateway.NewGateway(cfg)

	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	t.Cleanup(gw.Stop)

	agentInstance, err := gw.Orchestrator.GetOrCreateAgent("main")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	agentInstance.Brain = &fakeBrain{
		Response: &brain.BrainResponse{
			Text:       "ok",
			StopReason: "end_turn",
		},
	}
	return gw
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = original
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, reader); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stdout reader: %v", err)
	}
	return buf.String()
}
