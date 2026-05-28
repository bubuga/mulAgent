package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Chat plan management",
}

var chatPlanCmd = &cobra.Command{
	Use:   "plan",
	Short: "Manage execution plans",
}

var chatPlanSubmitCmd = &cobra.Command{
	Use:   "submit",
	Short: "Submit a one-shot execution plan (JSON from stdin or --file)",
	Args:  cobra.NoArgs,
	RunE:  runChatPlanSubmit,
}

var chatPlanClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Cancel the active execution plan",
	Args:  cobra.NoArgs,
	RunE:  runChatPlanClear,
}

func init() {
	chatPlanSubmitCmd.Flags().String("session", "", "Chat session ID (defaults to MULTICA_CHAT_SESSION_ID env)")
	chatPlanSubmitCmd.Flags().String("file", "", "Read JSON plan from file instead of stdin")
	chatPlanSubmitCmd.Flags().Bool("dry-run", false, "Validate only, do not persist")

	chatPlanClearCmd.Flags().String("session", "", "Chat session ID (defaults to MULTICA_CHAT_SESSION_ID env)")

	chatPlanCmd.AddCommand(chatPlanSubmitCmd)
	chatPlanCmd.AddCommand(chatPlanClearCmd)
	chatCmd.AddCommand(chatPlanCmd)
}

type planStepInput struct {
	AgentID string `json:"agent_id"`
	Prompt  string `json:"prompt"`
}

type planSubmitInput struct {
	Steps []planStepInput `json:"steps"`
}

func resolveSessionID(cmd *cobra.Command) (string, error) {
	sessionID, _ := cmd.Flags().GetString("session")
	if sessionID == "" {
		sessionID = os.Getenv("MULTICA_CHAT_SESSION_ID")
	}
	if sessionID == "" {
		return "", fmt.Errorf("session ID required: use --session flag or set MULTICA_CHAT_SESSION_ID env")
	}
	return sessionID, nil
}

func runChatPlanSubmit(cmd *cobra.Command, args []string) error {
	sessionID, err := resolveSessionID(cmd)
	if err != nil {
		return err
	}

	// Read JSON from file or stdin.
	filePath, _ := cmd.Flags().GetString("file")
	var data []byte
	if filePath != "" {
		data, err = os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}
	} else {
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	}

	var input planSubmitInput
	if err := json.Unmarshal(data, &input); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if len(input.Steps) < 1 {
		return fmt.Errorf("at least one step is required")
	}
	if len(input.Steps) > 8 {
		return fmt.Errorf("maximum 8 steps per plan")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/chat/sessions/%s/plan", sessionID)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if dryRun {
		path += "?dry_run=true"
	}

	var result json.RawMessage
	if err := client.PostJSON(context.Background(), path, input, &result); err != nil {
		return fmt.Errorf("submit plan: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Plan submitted successfully for session %s\n", sessionID)
	fmt.Println(string(result))
	return nil
}

func runChatPlanClear(cmd *cobra.Command, args []string) error {
	sessionID, err := resolveSessionID(cmd)
	if err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/chat/sessions/%s/plan", sessionID)
	if err := client.DeleteJSON(context.Background(), path); err != nil {
		return fmt.Errorf("clear plan: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Plan cancelled for session %s\n", sessionID)
	return nil
}
