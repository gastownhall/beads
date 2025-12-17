package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

var mailCmd = &cobra.Command{
	Use:   "mail",
	Short: "Send and receive messages via beads",
	Long: `Send and receive messages between agents using beads storage.

Messages are stored as issues with type=message, enabling git-native
inter-agent communication without external services.

Examples:
  bd mail send worker-1 -s "Task complete" -m "Finished bd-xyz"
  bd mail inbox
  bd mail read bd-abc123
  bd mail ack bd-abc123`,
}

var mailSendCmd = &cobra.Command{
	Use:   "send <recipient> -s <subject> -m <body>",
	Short: "Send a message to another agent",
	Long: `Send a message to another agent via beads.

Creates an issue with type=message, sender=your identity, assignee=recipient.
The --urgent flag sets priority=0.

Examples:
  bd mail send worker-1 -s "Task complete" -m "Finished bd-xyz"
  bd mail send worker-1 -s "Help needed" -m "Blocked on auth" --urgent
  bd mail send worker-1 -s "Quick note" -m "FYI" --identity refinery`,
	Args: cobra.ExactArgs(1),
	RunE: runMailSend,
}

var mailInboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "List messages addressed to you",
	Long: `List open messages where assignee matches your identity.

Messages are sorted by priority (urgent first), then by date (newest first).

Examples:
  bd mail inbox
  bd mail inbox --from worker-1
  bd mail inbox --priority 0`,
	RunE: runMailInbox,
}

var mailReadCmd = &cobra.Command{
	Use:   "read <id>",
	Short: "Read a specific message",
	Long: `Display the full content of a message.

Does NOT mark the message as read - use 'bd mail ack' for that.

Example:
  bd mail read bd-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runMailRead,
}

var mailAckCmd = &cobra.Command{
	Use:   "ack <id> [id2...]",
	Short: "Acknowledge (close) messages",
	Long: `Mark messages as read by closing them.

Can acknowledge multiple messages at once.

Examples:
  bd mail ack bd-abc123
  bd mail ack bd-abc123 bd-def456 bd-ghi789`,
	Args: cobra.MinimumNArgs(1),
	RunE: runMailAck,
}

var mailReplyCmd = &cobra.Command{
	Use:   "reply <id> -m <body>",
	Short: "Reply to a message",
	Long: `Reply to an existing message, creating a conversation thread.

Creates a new message with replies_to set to the original message,
and sends it to the original sender.

Examples:
  bd mail reply bd-abc123 -m "Thanks for the update!"
  bd mail reply bd-abc123 -m "Done" --urgent`,
	Args: cobra.ExactArgs(1),
	RunE: runMailReply,
}

// Mail command flags
var (
	mailSubject      string
	mailBody         string
	mailUrgent       bool
	mailIdentity     string
	mailFrom         string
	mailPriorityFlag int
)

func init() {
	rootCmd.AddCommand(mailCmd)
	mailCmd.AddCommand(mailSendCmd)
	mailCmd.AddCommand(mailInboxCmd)
	mailCmd.AddCommand(mailReadCmd)
	mailCmd.AddCommand(mailAckCmd)
	mailCmd.AddCommand(mailReplyCmd)

	// Send command flags
	mailSendCmd.Flags().StringVarP(&mailSubject, "subject", "s", "", "Message subject (required)")
	mailSendCmd.Flags().StringVarP(&mailBody, "body", "m", "", "Message body (required)")
	mailSendCmd.Flags().BoolVar(&mailUrgent, "urgent", false, "Set priority=0 (urgent)")
	mailSendCmd.Flags().StringVar(&mailIdentity, "identity", "", "Override sender identity")
	_ = mailSendCmd.MarkFlagRequired("subject")
	_ = mailSendCmd.MarkFlagRequired("body")

	// Inbox command flags
	mailInboxCmd.Flags().StringVar(&mailFrom, "from", "", "Filter by sender")
	mailInboxCmd.Flags().IntVar(&mailPriorityFlag, "priority", -1, "Filter by priority (0-4)")

	// Read command flags
	mailReadCmd.Flags().StringVar(&mailIdentity, "identity", "", "Override identity for access check")

	// Ack command flags
	mailAckCmd.Flags().StringVar(&mailIdentity, "identity", "", "Override identity")

	// Reply command flags
	mailReplyCmd.Flags().StringVarP(&mailBody, "body", "m", "", "Reply body (required)")
	mailReplyCmd.Flags().BoolVar(&mailUrgent, "urgent", false, "Set priority=0 (urgent)")
	mailReplyCmd.Flags().StringVar(&mailIdentity, "identity", "", "Override sender identity")
	_ = mailReplyCmd.MarkFlagRequired("body")
}

func runMailSend(cmd *cobra.Command, args []string) error {
	CheckReadonly("mail send")

	recipient := args[0]
	sender := config.GetIdentity(mailIdentity)

	// Determine priority
	priority := 2 // default: normal
	if mailUrgent {
		priority = 0
	}

	// If daemon is running, use RPC
	if daemonClient != nil {
		createArgs := &rpc.CreateArgs{
			Title:       mailSubject,
			Description: mailBody,
			IssueType:   string(types.TypeMessage),
			Priority:    priority,
			Assignee:    recipient,
			Sender:      sender,
			Ephemeral:   true, // Messages can be bulk-deleted
		}

		resp, err := daemonClient.Create(createArgs)
		if err != nil {
			return fmt.Errorf("failed to send message: %w", err)
		}

		// Parse response to get issue ID
		var issue types.Issue
		if err := json.Unmarshal(resp.Data, &issue); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		// Run message hook (bd-kwro.8)
		if hookRunner != nil {
			hookRunner.Run(hooks.EventMessage, &issue)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"id":        issue.ID,
				"to":        recipient,
				"from":      sender,
				"subject":   mailSubject,
				"priority":  priority,
				"timestamp": issue.CreatedAt,
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}

		fmt.Printf("Message sent: %s\n", issue.ID)
		fmt.Printf("  To: %s\n", recipient)
		fmt.Printf("  Subject: %s\n", mailSubject)
		if mailUrgent {
			fmt.Printf("  Priority: URGENT\n")
		}
		return nil
	}

	// Direct mode
	now := time.Now()
	issue := &types.Issue{
		Title:       mailSubject,
		Description: mailBody,
		Status:      types.StatusOpen,
		Priority:    priority,
		IssueType:   types.TypeMessage,
		Assignee:    recipient,
		Sender:      sender,
		Ephemeral:   true, // Messages can be bulk-deleted
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := store.CreateIssue(rootCtx, issue, actor); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	// Trigger auto-flush
	if flushManager != nil {
		flushManager.MarkDirty(false)
	}

	// Run message hook (bd-kwro.8)
	if hookRunner != nil {
		hookRunner.Run(hooks.EventMessage, issue)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id":        issue.ID,
			"to":        recipient,
			"from":      sender,
			"subject":   mailSubject,
			"priority":  priority,
			"timestamp": issue.CreatedAt,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("Message sent: %s\n", issue.ID)
	fmt.Printf("  To: %s\n", recipient)
	fmt.Printf("  Subject: %s\n", mailSubject)
	if mailUrgent {
		fmt.Printf("  Priority: URGENT\n")
	}

	return nil
}

func runMailInbox(cmd *cobra.Command, args []string) error {
	identity := config.GetIdentity(mailIdentity)

	// Query for open messages assigned to this identity
	messageType := types.TypeMessage
	openStatus := types.StatusOpen
	filter := types.IssueFilter{
		IssueType: &messageType,
		Status:    &openStatus,
		Assignee:  &identity,
	}

	var issues []*types.Issue
	var err error

	if daemonClient != nil {
		// Daemon mode - use RPC list
		resp, rpcErr := daemonClient.List(&rpc.ListArgs{
			Status:    string(openStatus),
			IssueType: string(messageType),
			Assignee:  identity,
		})
		if rpcErr != nil {
			return fmt.Errorf("failed to fetch inbox: %w", rpcErr)
		}
		if err := json.Unmarshal(resp.Data, &issues); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		// Direct mode
		issues, err = store.SearchIssues(rootCtx, "", filter)
		if err != nil {
			return fmt.Errorf("failed to fetch inbox: %w", err)
		}
	}

	// Filter by sender if specified
	var filtered []*types.Issue
	for _, issue := range issues {
		if mailFrom != "" && issue.Sender != mailFrom {
			continue
		}
		// Filter by priority if specified
		if cmd.Flags().Changed("priority") && mailPriorityFlag >= 0 && issue.Priority != mailPriorityFlag {
			continue
		}
		filtered = append(filtered, issue)
	}

	// Sort by priority (ascending), then by date (descending)
	// Priority 0 is highest priority
	for i := 0; i < len(filtered)-1; i++ {
		for j := i + 1; j < len(filtered); j++ {
			swap := false
			if filtered[i].Priority > filtered[j].Priority {
				swap = true
			} else if filtered[i].Priority == filtered[j].Priority {
				if filtered[i].CreatedAt.Before(filtered[j].CreatedAt) {
					swap = true
				}
			}
			if swap {
				filtered[i], filtered[j] = filtered[j], filtered[i]
			}
		}
	}

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(filtered)
	}

	if len(filtered) == 0 {
		fmt.Printf("No messages for %s\n", identity)
		return nil
	}

	fmt.Printf("Inbox for %s (%d messages):\n\n", identity, len(filtered))
	for _, msg := range filtered {
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

		// Priority indicator
		priorityStr := ""
		if msg.Priority == 0 {
			priorityStr = " [URGENT]"
		} else if msg.Priority == 1 {
			priorityStr = " [HIGH]"
		}

		fmt.Printf("  %s: %s%s\n", msg.ID, msg.Title, priorityStr)
		fmt.Printf("      From: %s (%s)\n", msg.Sender, timeStr)
		if msg.RepliesTo != "" {
			fmt.Printf("      Re: %s\n", msg.RepliesTo)
		}
		fmt.Println()
	}

	return nil
}

func runMailRead(cmd *cobra.Command, args []string) error {
	messageID := args[0]

	var issue *types.Issue

	if daemonClient != nil {
		// Daemon mode - use RPC show
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: messageID})
		if err != nil {
			return fmt.Errorf("failed to read message: %w", err)
		}
		if err := json.Unmarshal(resp.Data, &issue); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		// Direct mode
		var err error
		issue, err = store.GetIssue(rootCtx, messageID)
		if err != nil {
			return fmt.Errorf("failed to read message: %w", err)
		}
	}

	if issue == nil {
		return fmt.Errorf("message not found: %s", messageID)
	}

	if issue.IssueType != types.TypeMessage {
		return fmt.Errorf("%s is not a message (type: %s)", messageID, issue.IssueType)
	}

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(issue)
	}

	// Display message
	fmt.Println(strings.Repeat("─", 66))
	fmt.Printf("ID:      %s\n", issue.ID)
	fmt.Printf("From:    %s\n", issue.Sender)
	fmt.Printf("To:      %s\n", issue.Assignee)
	fmt.Printf("Subject: %s\n", issue.Title)
	fmt.Printf("Time:    %s\n", issue.CreatedAt.Format("2006-01-02 15:04:05"))
	if issue.Priority <= 1 {
		fmt.Printf("Priority: P%d\n", issue.Priority)
	}
	if issue.RepliesTo != "" {
		fmt.Printf("Re:      %s\n", issue.RepliesTo)
	}
	fmt.Printf("Status:  %s\n", issue.Status)
	fmt.Println(strings.Repeat("─", 66))
	fmt.Println()
	fmt.Println(issue.Description)
	fmt.Println()

	return nil
}

func runMailAck(cmd *cobra.Command, args []string) error {
	CheckReadonly("mail ack")

	var acked []string
	var errors []string

	for _, messageID := range args {
		var issue *types.Issue

		if daemonClient != nil {
			// Daemon mode - use RPC
			resp, err := daemonClient.Show(&rpc.ShowArgs{ID: messageID})
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", messageID, err))
				continue
			}
			if err := json.Unmarshal(resp.Data, &issue); err != nil {
				errors = append(errors, fmt.Sprintf("%s: parse error: %v", messageID, err))
				continue
			}
		} else {
			// Direct mode
			var err error
			issue, err = store.GetIssue(rootCtx, messageID)
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", messageID, err))
				continue
			}
		}

		if issue == nil {
			errors = append(errors, fmt.Sprintf("%s: not found", messageID))
			continue
		}

		if issue.IssueType != types.TypeMessage {
			errors = append(errors, fmt.Sprintf("%s: not a message (type: %s)", messageID, issue.IssueType))
			continue
		}

		if issue.Status == types.StatusClosed {
			errors = append(errors, fmt.Sprintf("%s: already acknowledged", messageID))
			continue
		}

		// Close the message
		if daemonClient != nil {
			// Daemon mode - use RPC close
			_, err := daemonClient.CloseIssue(&rpc.CloseArgs{ID: messageID})
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", messageID, err))
				continue
			}
		} else {
			// Direct mode - use CloseIssue for proper close handling
			if err := store.CloseIssue(rootCtx, messageID, "acknowledged", actor); err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", messageID, err))
				continue
			}
		}

		acked = append(acked, messageID)
	}

	// Trigger auto-flush if any messages were acked (direct mode only)
	if len(acked) > 0 && flushManager != nil {
		flushManager.MarkDirty(false)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"acknowledged": acked,
			"errors":       errors,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	for _, id := range acked {
		fmt.Printf("Acknowledged: %s\n", id)
	}
	for _, errMsg := range errors {
		fmt.Fprintf(os.Stderr, "Error: %s\n", errMsg)
	}

	if len(errors) > 0 && len(acked) == 0 {
		return fmt.Errorf("failed to acknowledge any messages")
	}

	return nil
}

func runMailReply(cmd *cobra.Command, args []string) error {
	CheckReadonly("mail reply")

	messageID := args[0]
	sender := config.GetIdentity(mailIdentity)

	// Get the original message
	var originalMsg *types.Issue

	if daemonClient != nil {
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: messageID})
		if err != nil {
			return fmt.Errorf("failed to get original message: %w", err)
		}
		if err := json.Unmarshal(resp.Data, &originalMsg); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		originalMsg, err = store.GetIssue(rootCtx, messageID)
		if err != nil {
			return fmt.Errorf("failed to get original message: %w", err)
		}
	}

	if originalMsg == nil {
		return fmt.Errorf("message not found: %s", messageID)
	}

	if originalMsg.IssueType != types.TypeMessage {
		return fmt.Errorf("%s is not a message (type: %s)", messageID, originalMsg.IssueType)
	}

	// Determine recipient: reply goes to the original sender
	recipient := originalMsg.Sender
	if recipient == "" {
		return fmt.Errorf("original message has no sender, cannot determine reply recipient")
	}

	// Build reply subject
	subject := originalMsg.Title
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	// Determine priority
	priority := 2 // default: normal
	if mailUrgent {
		priority = 0
	}

	// Create the reply message
	now := time.Now()
	reply := &types.Issue{
		Title:       subject,
		Description: mailBody,
		Status:      types.StatusOpen,
		Priority:    priority,
		IssueType:   types.TypeMessage,
		Assignee:    recipient,
		Sender:      sender,
		Ephemeral:   true,
		RepliesTo:   messageID, // Thread link
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if daemonClient != nil {
		// Daemon mode - create reply with all messaging fields
		createArgs := &rpc.CreateArgs{
			Title:       reply.Title,
			Description: reply.Description,
			IssueType:   string(types.TypeMessage),
			Priority:    priority,
			Assignee:    recipient,
			Sender:      sender,
			Ephemeral:   true,
			RepliesTo:   messageID, // Thread link
		}

		resp, err := daemonClient.Create(createArgs)
		if err != nil {
			return fmt.Errorf("failed to send reply: %w", err)
		}

		var createdIssue types.Issue
		if err := json.Unmarshal(resp.Data, &createdIssue); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"id":         createdIssue.ID,
				"to":         recipient,
				"from":       sender,
				"subject":    subject,
				"replies_to": messageID,
				"priority":   priority,
				"timestamp":  createdIssue.CreatedAt,
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}

		fmt.Printf("Reply sent: %s\n", createdIssue.ID)
		fmt.Printf("  To: %s\n", recipient)
		fmt.Printf("  Re: %s\n", messageID)
		if mailUrgent {
			fmt.Printf("  Priority: URGENT\n")
		}
		return nil
	}

	// Direct mode
	if err := store.CreateIssue(rootCtx, reply, actor); err != nil {
		return fmt.Errorf("failed to send reply: %w", err)
	}

	// Trigger auto-flush
	if flushManager != nil {
		flushManager.MarkDirty(false)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id":         reply.ID,
			"to":         recipient,
			"from":       sender,
			"subject":    subject,
			"replies_to": messageID,
			"priority":   priority,
			"timestamp":  reply.CreatedAt,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("Reply sent: %s\n", reply.ID)
	fmt.Printf("  To: %s\n", recipient)
	fmt.Printf("  Re: %s\n", messageID)
	if mailUrgent {
		fmt.Printf("  Priority: URGENT\n")
	}

	return nil
}
