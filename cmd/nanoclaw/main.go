// NanoClaw CLI entry point.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"go-nanoclaw/internal/agent"
	"go-nanoclaw/internal/brain"
	"go-nanoclaw/internal/channel"
	"go-nanoclaw/internal/config"
	"go-nanoclaw/internal/gateway"
	mclog "go-nanoclaw/internal/log"
	"go-nanoclaw/internal/memory"
)

var (
	verbose   int
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "nanoclaw",
		Short: "NanoClaw - A minimal autonomous agent runtime implementation in Go",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			mclog.SetVerbosity(verbose)
		},
	}
	rootCmd.PersistentFlags().CountVarP(&verbose, "verbose", "v",
		"Increase verbosity (-v for flow logs, -vv for full HTTP traces)")

	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(chatCmd())
	rootCmd.AddCommand(demoCmd())
	rootCmd.AddCommand(checkCmd())
	rootCmd.AddCommand(discordCmd())
	rootCmd.AddCommand(serveCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize NanoClaw configuration and workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.NewConfig()
			configPath, err := cfg.SaveDefault()
			if err != nil {
				return err
			}
			fmt.Printf("Config: %s\n", configPath)

			workspace := config.ExpandHome(cfg.Workspace)
			os.MkdirAll(workspace, 0755)
			mem := memory.New(workspace, cfg.BootstrapMaxChars)
			mem.CreateDefaults()

			// Create example skill
			skillsDir := filepath.Join(workspace, "skills")
			exampleSkill := filepath.Join(skillsDir, "example.md")
			if _, err := os.Stat(exampleSkill); os.IsNotExist(err) {
				os.WriteFile(exampleSkill, []byte(
					"---\n"+
						"name: greeting\n"+
						"description: Responds to greetings with a friendly message\n"+
						"triggers:\n"+
						"  - type: keyword\n    pattern: \"hello\"\n"+
						"  - type: keyword\n    pattern: \"hi\"\n"+
						"  - type: regex\n    pattern: \"^(hey|yo|sup)\"\n"+
						"tools: []\n"+
						"---\n\n"+
						"When the user greets you, answer briefly and identify yourself as NanoClaw.\n"+
						"Mention that you can help operate the local workspace when asked.\n",
				), 0644)
				fmt.Printf("Example skill: %s\n", exampleSkill)
			}

			fmt.Printf("Workspace: %s\n", workspace)
			fmt.Println("\nNext steps:")
			fmt.Println("  1. export ANTHROPIC_API_KEY='<your key>'  (or OPENAI_API_KEY)")
			fmt.Println("  2. Edit workspace/SOUL.md to customize your agent")
			fmt.Println("  3. Run: nanoclaw chat")
			return nil
		},
	}
}

func demoCmd() *cobra.Command {
	var task, readPath, workspace string
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Run a local no-API-key agent loop demo",
		RunE: func(cmd *cobra.Command, args []string) error {
			if verbose == 0 {
				mclog.SetVerbosity(1)
			}
			if workspace == "" {
				var err error
				workspace, err = os.Getwd()
				if err != nil {
					return err
				}
			}
			if task == "" {
				task = "Read a workspace file and summarize what this project does."
			}
			if readPath == "" {
				readPath = "README.md"
			}

			cfg := config.NewConfig()
			cfg.Workspace = workspace
			cfg.MaxToolRounds = 3
			def := cfg.Agents["main"]
			def.Workspace = ""
			def.AllowedTools = []string{"read_workspace_file", "list_workspace"}
			def.ToolPolicies = config.ToolPolicyConfig{
				FileWriteEnabled: false,
				ShellEnabled:     false,
				HTTPEnabled:      false,
			}

			a, err := agent.NewAgent(def, workspace, cfg, 0)
			if err != nil {
				return err
			}
			a.Brain = &brain.ScriptedBrain{Task: task, Path: readPath}

			response, err := a.ProcessMessage(context.Background(), task)
			if err != nil {
				return err
			}
			fmt.Println(response)
			return nil
		},
	}
	cmd.Flags().StringVar(&task, "task", "", "Demo task to send to the agent")
	cmd.Flags().StringVar(&readPath, "read", "README.md", "Workspace-relative file to read during the demo")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Workspace directory; defaults to the current directory")
	return cmd
}

func chatCmd() *cobra.Command {
	var agentID, configPath string
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Start an interactive chat session",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			gw := gateway.NewGateway(cfg)
			if err := gw.Start(ctx); err != nil {
				return err
			}
			defer gw.Stop()

			ch := channel.NewCLIChannel(gw, agentID)
			return ch.Start(ctx)
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "main", "Agent ID to chat with")
	cmd.Flags().StringVar(&configPath, "config", "", "Config file path")
	return cmd
}

func checkCmd() *cobra.Command {
	var agentID string
	cmd := &cobra.Command{
		Use:     "check",
		Aliases: []string{"heartbeat"},
		Short:   "Trigger a manual periodic check",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load("")
			if err != nil {
				return err
			}

			ctx := context.Background()
			gw := gateway.NewGateway(cfg)
			if err := gw.Start(ctx); err != nil {
				return err
			}
			defer gw.Stop()

			hb := gw.GetPeriodicCheck(agentID)
			if hb == nil {
				fmt.Printf("No periodic check configured for agent '%s'\n", agentID)
				return nil
			}

			result, err := hb.Tick(ctx)
			if err != nil {
				return err
			}
			if result != "" {
				fmt.Printf("Alert: %s\n", result)
			} else {
				fmt.Println("HEARTBEAT_OK - nothing to report")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "main", "Agent ID")
	return cmd
}

func discordCmd() *cobra.Command {
	var token, agentID string
	cmd := &cobra.Command{
		Use:   "discord",
		Short: "Start Discord bot channel",
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("DISCORD_BOT_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("set DISCORD_BOT_TOKEN or pass --token")
			}

			cfg, err := config.Load("")
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			gw := gateway.NewGateway(cfg)
			if err := gw.Start(ctx); err != nil {
				return err
			}
			defer gw.Stop()

			ch := channel.NewDiscordChannel(gw, token, agentID)
			return ch.Start(ctx)
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "Discord bot token")
	cmd.Flags().StringVar(&agentID, "agent", "main", "Agent ID")
	return cmd
}

func serveCmd() *cobra.Command {
	var host string
	var port int
	var discordToken, agentID string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run NanoClaw as a long-lived service with HTTP health endpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load("")
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			gw := gateway.NewGateway(cfg)
			if err := gw.Start(ctx); err != nil {
				return err
			}
			defer gw.Stop()

			// Start Discord channel in goroutine if token provided
			if discordToken == "" {
				discordToken = os.Getenv("DISCORD_BOT_TOKEN")
			}
			if discordToken != "" {
				discordCh := channel.NewDiscordChannel(gw, discordToken, agentID)
				go func() {
					if err := discordCh.Start(ctx); err != nil {
						slog.Error("Discord channel error", "error", err)
					}
				}()
				fmt.Println("Discord channel starting...")
			}

			// Start HTTP server (blocks until ctx is canceled)
			httpCh := channel.NewHTTPChannel(gw, agentID, host, port)
			if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
				httpCh.WithTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
			}
			fmt.Println("NanoClaw service running. Press Ctrl+C to stop.")
			return httpCh.Start(ctx)
		},
	}
	cmd.Flags().StringVar(&host, "host", "0.0.0.0", "HTTP bind host")
	cmd.Flags().IntVar(&port, "port", 8765, "HTTP port")
	cmd.Flags().StringVar(&discordToken, "discord-token", "", "Discord bot token (enables Discord channel)")
	cmd.Flags().StringVar(&agentID, "agent", "main", "Default agent ID")
	return cmd
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show NanoClaw status and configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load("")
			if err != nil {
				return err
			}

			fmt.Printf("Config dir: %s\n", cfg.ConfigDir)
			fmt.Printf("Workspace: %s\n", cfg.Workspace)

			agentIDs := make([]string, 0, len(cfg.Agents))
			for id := range cfg.Agents {
				agentIDs = append(agentIDs, id)
			}
			fmt.Printf("Agents: %v\n", agentIDs)

			for agentID, agentDef := range cfg.Agents {
				fmt.Printf("\n  [%s]\n", agentID)
				fmt.Printf("    Provider: %s\n", agentDef.Brain.Provider)
				fmt.Printf("    Model: %s\n", agentDef.Brain.Model)
				checkStatus := "on"
				if !agentDef.Heartbeat.Enabled {
					checkStatus = "off"
				}
				fmt.Printf("    Periodic check: %s\n", checkStatus)
				fmt.Printf("    Spawn depth: %d\n", agentDef.MaxSpawnDepth)
			}
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("nanoclaw %s (commit: %s, built: %s)\n", version, commit, buildTime)
		},
	}
}
