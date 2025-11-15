package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var messageCmd = &cobra.Command{
	Use:   "message",
	Short: "Send and receive messages via Agent Mail",
	Long: `Send and receive messages between agents using Agent Mail server.

Requires Agent Mail server running and these environment variables:
  BEADS_AGENT_MAIL_URL - Server URL (e.g., http://127.0.0.1:8765)
  BEADS_AGENT_NAME - Your agent name (e.g., fred-beads-stevey-macbook)
  BEADS_PROJECT_ID - Project identifier (defaults to repo path)

Example:
  bd message send dave-beads-stevey-macbook "Need review on bd-z0yn"
  bd message inbox --unread-only
  bd message read msg-abc123
  bd message ack msg-abc123`,
}

var messageSendCmd = &cobra.Command{
	Use:   "send <to-agent> <message>",
	Short: "Send a message to another agent",
	Long: `Send a message to another agent via Agent Mail.

The message can be plain text or GitHub-flavored Markdown.

Examples:
  bd message send dave-beads-stevey-macbook "Working on bd-z0yn"
  bd message send cino-beads-stevey-macbook "Please review PR #42" --subject "Review Request"
  bd message send emma-beads-stevey-macbook "Found bug in auth" --thread-id bd-123`,
	Args: cobra.ExactArgs(2),
	RunE: runMessageSend,
}

var messageInboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "List inbox messages",
	Long: `List messages in your inbox.

Examples:
  bd message inbox
  bd message inbox --unread-only --limit 10
  bd message inbox --urgent-only`,
	RunE: runMessageInbox,
}

var messageReadCmd = &cobra.Command{
	Use:   "read <message-id>",
	Short: "Read a specific message",
	Long: `Read and display a specific message by ID.

Marks the message as read automatically.

Example:
  bd message read msg-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runMessageRead,
}

var messageAckCmd = &cobra.Command{
	Use:   "ack <message-id>",
	Short: "Acknowledge a message",
	Long: `Acknowledge a message that requires acknowledgement.

Also marks the message as read if not already.

Example:
  bd message ack msg-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runMessageAck,
}

// Message send flags
var (
	messageSubject  string
	messageThreadID string
	messageImportance string
	messageAckRequired bool
)

// Message inbox flags
var (
	messageLimit      int
	messageUnreadOnly bool
	messageUrgentOnly bool
)

func init() {
	// Register message commands
	rootCmd.AddCommand(messageCmd)
	messageCmd.AddCommand(messageSendCmd)
	messageCmd.AddCommand(messageInboxCmd)
	messageCmd.AddCommand(messageReadCmd)
	messageCmd.AddCommand(messageAckCmd)

	// Send command flags
	messageSendCmd.Flags().StringVarP(&messageSubject, "subject", "s", "", "Message subject")
	messageSendCmd.Flags().StringVar(&messageThreadID, "thread-id", "", "Thread ID to group related messages")
	messageSendCmd.Flags().StringVar(&messageImportance, "importance", "normal", "Message importance (low, normal, high, urgent)")
	messageSendCmd.Flags().BoolVar(&messageAckRequired, "ack-required", false, "Require acknowledgement from recipient")

	// Inbox command flags
	messageInboxCmd.Flags().IntVar(&messageLimit, "limit", 20, "Maximum number of messages to show")
	messageInboxCmd.Flags().BoolVar(&messageUnreadOnly, "unread-only", false, "Show only unread messages")
	messageInboxCmd.Flags().BoolVar(&messageUrgentOnly, "urgent-only", false, "Show only urgent messages")
}

// AgentMailConfig holds configuration for Agent Mail server
type AgentMailConfig struct {
	URL       string
	AgentName string
	ProjectID string
}

const agentMailConfigHelp = `Agent Mail not configured. Configure with:
  export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
  export BEADS_AGENT_NAME=your-agent-name
  export BEADS_PROJECT_ID=your-project`

// Message represents an Agent Mail message
type Message struct {
	ID           int       `json:"id"`
	Subject      string    `json:"subject"`
	Body         string    `json:"body,omitempty"`
	FromAgent    string    `json:"from_agent"`
	CreatedAt    time.Time `json:"created_at"`
	Importance   string    `json:"importance"`
	AckRequired  bool      `json:"ack_required"`
	ThreadID     string    `json:"thread_id,omitempty"`
	Read         bool      `json:"read"`
	Acknowledged bool      `json:"acknowledged"`
}

// getAgentMailConfig retrieves Agent Mail configuration from environment
func getAgentMailConfig() (*AgentMailConfig, error) {
	url := os.Getenv("BEADS_AGENT_MAIL_URL")
	if url == "" {
		return nil, fmt.Errorf("BEADS_AGENT_MAIL_URL not set")
	}

	agentName := os.Getenv("BEADS_AGENT_NAME")
	if agentName == "" {
		return nil, fmt.Errorf("BEADS_AGENT_NAME not set")
	}

	projectID := os.Getenv("BEADS_PROJECT_ID")
	if projectID == "" {
		// Default to workspace root path (directory containing .beads/)
		if dbPath != "" {
			beadsDir := filepath.Dir(dbPath)
			projectID = filepath.Dir(beadsDir)
		} else {
			// Fallback to current directory
			cwd, err := os.Getwd()
			if err == nil {
				projectID = cwd
			}
		}
	}

	return &AgentMailConfig{
		URL:       url,
		AgentName: agentName,
		ProjectID: projectID,
	}, nil
}

// sendAgentMailRequest sends a JSON-RPC request to Agent Mail server
func sendAgentMailRequest(config *AgentMailConfig, method string, params interface{}) (json.RawMessage, error) {
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      method,
			"arguments": params,
		},
	}

	reqBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := strings.TrimRight(config.URL, "/") + "/mcp"
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Agent Mail server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Agent Mail server returned error: %s (status %d)", string(body), resp.StatusCode)
	}

	var response struct {
		Result struct {
			Content []struct {
				Type string          `json:"type"`
				Text json.RawMessage `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if response.Error != nil {
		return nil, fmt.Errorf("Agent Mail error: %s (code %d)", response.Error.Message, response.Error.Code)
	}

	if len(response.Result.Content) == 0 {
		return nil, fmt.Errorf("no content in response")
	}

	return response.Result.Content[0].Text, nil
}

func runMessageSend(cmd *cobra.Command, args []string) error {
	// Validate importance flag
	validImportance := map[string]bool{
		"low":    true,
		"normal": true,
		"high":   true,
		"urgent": true,
	}
	if !validImportance[messageImportance] {
		return fmt.Errorf("invalid importance: %s (must be: low, normal, high, urgent)", messageImportance)
	}

	config, err := getAgentMailConfig()
	if err != nil {
		return fmt.Errorf("%w\n\n%s", err, agentMailConfigHelp)
	}

	toAgent := args[0]
	message := args[1]

	// Prepare request parameters
	params := map[string]interface{}{
		"project_key": config.ProjectID,
		"sender_name": config.AgentName,
		"to":          []string{toAgent},
		"body_md":     message,
	}

	if messageSubject != "" {
		params["subject"] = messageSubject
	} else {
		// Generate subject from first line of message
		firstLine := strings.Split(message, "\n")[0]
		if len(firstLine) > 50 {
			firstLine = firstLine[:50] + "..."
		}
		params["subject"] = firstLine
	}

	if messageThreadID != "" {
		params["thread_id"] = messageThreadID
	}

	if messageImportance != "normal" {
		params["importance"] = messageImportance
	}

	if messageAckRequired {
		params["ack_required"] = true
	}

	// Send message via Agent Mail
	result, err := sendAgentMailRequest(config, "send_message", params)
	if err != nil {
		return err
	}

	// Parse result
	var sendResult struct {
		Deliveries []struct {
			Recipient string `json:"recipient"`
			MessageID int    `json:"message_id"`
		} `json:"deliveries"`
		Count int `json:"count"`
	}

	if err := json.Unmarshal(result, &sendResult); err != nil {
		return fmt.Errorf("failed to parse send result: %w", err)
	}

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(sendResult)
	}

	fmt.Printf("Message sent to %s\n", toAgent)
	if sendResult.Count > 0 && len(sendResult.Deliveries) > 0 {
		fmt.Printf("Message ID: %d\n", sendResult.Deliveries[0].MessageID)
	}
	if messageThreadID != "" {
		fmt.Printf("Thread: %s\n", messageThreadID)
	}

	return nil
}

func runMessageInbox(cmd *cobra.Command, args []string) error {
	config, err := getAgentMailConfig()
	if err != nil {
		return fmt.Errorf("%w\n\n%s", err, agentMailConfigHelp)
	}

	// Prepare request parameters
	params := map[string]interface{}{
		"project_key":    config.ProjectID,
		"agent_name":     config.AgentName,
		"limit":          messageLimit,
		"include_bodies": false,
	}

	if messageUnreadOnly {
		params["unread_only"] = true
	}

	if messageUrgentOnly {
		params["urgent_only"] = true
	}

	// Fetch inbox via Agent Mail
	result, err := sendAgentMailRequest(config, "fetch_inbox", params)
	if err != nil {
		return err
	}

	// Parse result
	var messages []Message

	if err := json.Unmarshal(result, &messages); err != nil {
		return fmt.Errorf("failed to parse inbox: %w", err)
	}

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(messages)
	}

	if len(messages) == 0 {
		fmt.Println("No messages in inbox")
		return nil
	}

	fmt.Printf("Inbox for %s (%d messages):\n\n", config.AgentName, len(messages))
	for _, msg := range messages {
		// Format timestamp
		age := time.Since(msg.CreatedAt)
		var timeStr string
		if age < time.Hour {
			timeStr = fmt.Sprintf("%dm ago", int(age.Minutes()))
		} else if age < 24*time.Hour {
			timeStr = fmt.Sprintf("%dh ago", int(age.Hours()))
		} else {
			timeStr = fmt.Sprintf("%dd ago", int(age.Hours()/24))
		}

		// Status indicators
		status := ""
		if !msg.Read {
			status += " [UNREAD]"
		}
		if msg.AckRequired && !msg.Acknowledged {
			status += " [ACK REQUIRED]"
		}
		if msg.Importance == "high" || msg.Importance == "urgent" {
			status += fmt.Sprintf(" [%s]", strings.ToUpper(msg.Importance))
		}

		fmt.Printf("  %d: %s%s\n", msg.ID, msg.Subject, status)
		fmt.Printf("      From: %s (%s)\n", msg.FromAgent, timeStr)
		if msg.ThreadID != "" {
			fmt.Printf("      Thread: %s\n", msg.ThreadID)
		}
		fmt.Println()
	}

	return nil
}

func runMessageRead(cmd *cobra.Command, args []string) error {
	config, err := getAgentMailConfig()
	if err != nil {
		return fmt.Errorf("%w\n\n%s", err, agentMailConfigHelp)
	}

	messageID := args[0]

	// Fetch full message with body
	fetchParams := map[string]interface{}{
		"project_key":    config.ProjectID,
		"agent_name":     config.AgentName,
		"message_id":     messageID,
		"include_bodies": true,
	}

	result, err := sendAgentMailRequest(config, "fetch_inbox", fetchParams)
	if err != nil {
		return fmt.Errorf("failed to fetch message: %w", err)
	}

	// Parse message
	var messages []Message

	if err := json.Unmarshal(result, &messages); err != nil {
		return fmt.Errorf("failed to parse message: %w", err)
	}

	if len(messages) == 0 {
		return fmt.Errorf("message not found: %s", messageID)
	}

	msg := messages[0]

	// Mark as read if not already
	if !msg.Read {
		markParams := map[string]interface{}{
			"project_key": config.ProjectID,
			"agent_name":  config.AgentName,
			"message_id":  messageID,
		}
		_, _ = sendAgentMailRequest(config, "mark_message_read", markParams)
	}

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(msg)
	}

	// Display message
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("From:    %s\n", msg.FromAgent)
	fmt.Printf("Subject: %s\n", msg.Subject)
	fmt.Printf("Time:    %s\n", msg.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	if msg.ThreadID != "" {
		fmt.Printf("Thread:  %s\n", msg.ThreadID)
	}
	if msg.Importance != "" && msg.Importance != "normal" {
		fmt.Printf("Priority: %s\n", strings.ToUpper(msg.Importance))
	}
	if msg.AckRequired {
		status := "Required"
		if msg.Acknowledged {
			status = "Acknowledged"
		}
		fmt.Printf("Acknowledgement: %s\n", status)
	}
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")
	fmt.Println(msg.Body)
	fmt.Println()

	return nil
}

func runMessageAck(cmd *cobra.Command, args []string) error {
	config, err := getAgentMailConfig()
	if err != nil {
		return fmt.Errorf("%w\n\n%s", err, agentMailConfigHelp)
	}

	messageID := args[0]

	// Acknowledge message
	params := map[string]interface{}{
		"project_key": config.ProjectID,
		"agent_name":  config.AgentName,
		"message_id":  messageID,
	}

	result, err := sendAgentMailRequest(config, "acknowledge_message", params)
	if err != nil {
		return err
	}

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		var ackResult map[string]interface{}
		if err := json.Unmarshal(result, &ackResult); err != nil {
			return fmt.Errorf("failed to parse ack result: %w", err)
		}
		return encoder.Encode(ackResult)
	}

	fmt.Printf("Message %s acknowledged\n", messageID)
	return nil
}
