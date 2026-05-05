package channel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"

	"go-nanoclaw/internal/gateway"
)

// DiscordChannel connects NanoClaw to a Discord bot.
type DiscordChannel struct {
	Gateway             *gateway.Gateway
	Token               string
	AgentID             string
	AllowedChannels     []string
	AlertChannelID      string
	session             *discordgo.Session
	mu                  sync.RWMutex
	lastInteractionChID string
}

// NewDiscordChannel creates a new DiscordChannel.
func NewDiscordChannel(gw *gateway.Gateway, token, agentID string) *DiscordChannel {
	return &DiscordChannel{
		Gateway: gw,
		Token:   token,
		AgentID: agentID,
	}
}

// Start connects to Discord and begins listening for messages.
func (d *DiscordChannel) Start(ctx context.Context) error {
	session, err := discordgo.New("Bot " + d.Token)
	if err != nil {
		return fmt.Errorf("create discord session: %w", err)
	}
	d.session = session

	session.ShouldReconnectOnError = true
	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent

	// Track connection state for alerting
	var disconnectCount int64
	session.AddHandler(func(s *discordgo.Session, e *discordgo.Connect) {
		disconnectCount = 0
		slog.Info("Discord connected")
	})
	session.AddHandler(func(s *discordgo.Session, e *discordgo.Disconnect) {
		disconnectCount++
		slog.Warn("Discord disconnected", "attempt", disconnectCount)
		if disconnectCount >= 5 {
			slog.Error("Discord repeated disconnections — connection may be unstable",
				"disconnect_count", disconnectCount)
		}
	})

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == s.State.User.ID {
			return
		}

		shouldRespond := false

		// Respond to DMs
		ch, err := s.State.Channel(m.ChannelID)
		if err == nil && ch.Type == discordgo.ChannelTypeDM {
			shouldRespond = true
		}

		// Respond to mentions
		for _, mention := range m.Mentions {
			if mention.ID == s.State.User.ID {
				shouldRespond = true
				break
			}
		}

		if !shouldRespond {
			return
		}

		text := m.Content
		if s.State.User != nil {
			text = strings.ReplaceAll(text, "<@"+s.State.User.ID+">", "")
			text = strings.TrimSpace(text)
		}

		if text == "" {
			return
		}

		slog.Info("Discord message", "author", m.Author.Username, "text", text[:min(100, len(text))])

		d.setLastInteractionChannel(m.ChannelID)
		s.ChannelTyping(m.ChannelID)

		sessionID := d.sessionIDForMessage(m)
		response, err := d.Gateway.HandleInputDetailed(ctx, text, d.AgentID, sessionID, "discord")
		if err != nil {
			slog.Error("Discord handler error", "error", err)
			s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Error: %v", err))
			return
		}

		for _, chunk := range splitMessage(response.Response, 1900) {
			s.ChannelMessageSend(m.ChannelID, chunk)
		}
	})

	// Register gateway message handler for alerts
	d.Gateway.OnMessage(func(agentID, response string) {
		if d.session == nil {
			return
		}
		channelID := d.AlertChannelID
		if channelID == "" {
			channelID = d.lastInteractionChannel()
		}
		if channelID == "" {
			return
		}
		prefix := fmt.Sprintf("**[%s]**\n", agentID)
		for _, chunk := range splitMessage(prefix+response, 1900) {
			d.session.ChannelMessageSend(channelID, chunk)
		}
	})

	if err := session.Open(); err != nil {
		return fmt.Errorf("open discord session: %w", err)
	}

	slog.Info("Discord bot connected", "user", session.State.User.Username)

	// Wait for context cancellation
	<-ctx.Done()
	return session.Close()
}

// Stop closes the Discord session.
func (d *DiscordChannel) Stop() error {
	if d.session != nil {
		return d.session.Close()
	}
	return nil
}

func (d *DiscordChannel) sessionIDForMessage(m *discordgo.MessageCreate) string {
	if m == nil || m.Author == nil {
		return "discord:" + d.AgentID
	}
	return "discord:" + m.ChannelID + ":" + m.Author.ID
}

func (d *DiscordChannel) setLastInteractionChannel(channelID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastInteractionChID = channelID
}

func (d *DiscordChannel) lastInteractionChannel() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastInteractionChID
}

// Send is a no-op; sending is handled per-channel in the message handler.
func (d *DiscordChannel) Send(message string) error {
	return nil
}

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		splitAt := strings.LastIndex(text[:maxLen], "\n")
		if splitAt == -1 {
			splitAt = maxLen
		}

		chunks = append(chunks, text[:splitAt])
		text = strings.TrimLeft(text[splitAt:], "\n")
	}
	return chunks
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
