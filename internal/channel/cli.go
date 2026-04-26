package channel

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"go-nanoclaw/internal/brain"
	"go-nanoclaw/internal/gateway"
	mcRuntime "go-nanoclaw/internal/runtime"
)

// CLIChannel provides interactive terminal chat.
type CLIChannel struct {
	Gateway *gateway.Gateway
	AgentID string
	running bool
}

// NewCLIChannel creates a new CLIChannel.
func NewCLIChannel(gw *gateway.Gateway, agentID string) *CLIChannel {
	return &CLIChannel{
		Gateway: gw,
		AgentID: agentID,
	}
}

// Start begins the interactive chat loop.
func (c *CLIChannel) Start(ctx context.Context) error {
	c.running = true

	fmt.Println("┌─────────────────────────────────────────┐")
	fmt.Println("│  NanoClaw - A minimal autonomous agent runtime in Go    │")
	fmt.Printf("│  Agent: %-32s│\n", c.AgentID)
	fmt.Println("│  Type /quit to exit, /plan to plan      │")
	fmt.Println("└─────────────────────────────────────────┘")

	c.Gateway.OnMessage(func(agentID, response string) {
		if agentID != c.AgentID {
			fmt.Printf("\n[%s] %s\n", agentID, response)
		}
	})

	scanner := bufio.NewScanner(os.Stdin)
	for c.running {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		userInput := strings.TrimSpace(scanner.Text())
		if userInput == "" {
			continue
		}

		if strings.HasPrefix(userInput, "/") {
			shouldContinue, err := c.handleCommand(ctx, userInput)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
			}
			if !shouldContinue {
				break
			}
			continue
		}

		fmt.Println("Thinking...")

		response, err := c.Gateway.HandleInputDetailed(ctx, userInput, c.AgentID, c.sessionID(), "cli")
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		fmt.Println()
		fmt.Println(response.Response)
	}

	return nil
}

func (c *CLIChannel) sessionID() string {
	return "cli:" + c.AgentID
}

// Stop stops the CLI channel.
func (c *CLIChannel) Stop() error {
	c.running = false
	fmt.Println("Goodbye!")
	return nil
}

// Send prints a message to the terminal.
func (c *CLIChannel) Send(message string) error {
	fmt.Println()
	fmt.Println(message)
	return nil
}

func (c *CLIChannel) handleCommand(ctx context.Context, command string) (bool, error) {
	parts := strings.Fields(command)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/quit":
		c.Stop()
		return false, nil

	case "/plan":
		return c.handlePlannedCommand(ctx, command, "/plan", mcRuntime.ModePlanExecute)

	case "/verify":
		return c.handlePlannedCommand(ctx, command, "/verify", mcRuntime.ModePlanExecuteVerify)

	case "/skills":
		a, err := c.Gateway.Orchestrator.GetOrCreateAgent(c.AgentID)
		if err != nil {
			return true, err
		}
		skills := a.SkillRegistry.ListSkills()
		if len(skills) > 0 {
			fmt.Println("Workspace Skills:")
			for _, s := range skills {
				fmt.Printf("  - %s: %s\n", s["name"], s["description"])
			}
		} else {
			fmt.Println("No skills loaded.")
		}
		return true, nil

	case "/agents":
		agents := c.Gateway.Orchestrator.ListAgents()
		fmt.Printf("Agents: %s\n", strings.Join(agents, ", "))
		return true, nil

	case "/compact":
		a, err := c.Gateway.Orchestrator.GetOrCreateAgent(c.AgentID)
		if err != nil {
			return true, err
		}
		thinkFn := func(msgs []brain.Message, sysPrompt string) (*brain.BrainResponse, error) {
			return a.Brain.Think(ctx, msgs, sysPrompt, nil)
		}
		summary, err := a.Context.Compact(thinkFn, a.Memory.AssembleBootstrap(false))
		if err != nil {
			return true, fmt.Errorf("compact: %w", err)
		}
		truncated := summary
		if len(truncated) > 200 {
			truncated = truncated[:200]
		}
		fmt.Printf("Compacted. Summary: %s...\n", truncated)
		return true, nil

	case "/clear":
		a, err := c.Gateway.Orchestrator.GetOrCreateAgent(c.AgentID)
		if err != nil {
			return true, err
		}
		a.Context.Clear()
		fmt.Println("Context cleared.")
		return true, nil

	case "/check", "/heartbeat":
		hb := c.Gateway.GetPeriodicCheck(c.AgentID)
		if hb != nil {
			result, err := hb.Tick(ctx)
			if err != nil {
				return true, err
			}
			if result != "" {
				fmt.Println(result)
			} else {
				fmt.Println("Check cycle: nothing to report.")
			}
		} else {
			fmt.Println("No periodic check configured.")
		}
		return true, nil

	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		fmt.Println("Available: /quit /plan /verify /skills /agents /compact /clear /check")
		return true, nil
	}
}

func (c *CLIChannel) handlePlannedCommand(ctx context.Context, command, name string, mode mcRuntime.ExecutionMode) (bool, error) {
	fields := strings.Fields(command)
	task := ""
	if len(fields) > 0 && len(command) > len(fields[0]) {
		task = strings.TrimSpace(command[len(fields[0]):])
	}
	if task == "" {
		fmt.Printf("Usage: %s <task>\n", name)
		return true, nil
	}

	fmt.Println("Thinking...")
	response, err := c.Gateway.HandleInputModeDetailed(ctx, task, c.AgentID, c.sessionID(), "cli", mode)
	if err != nil {
		return true, err
	}

	fmt.Println()
	fmt.Println(response.Response)
	return true, nil
}
