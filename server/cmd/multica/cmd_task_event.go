package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Inspect task execution state",
}

var taskEventsCmd = &cobra.Command{
	Use:   "events <task-id>",
	Short: "List the durable event history for a task",
	Args:  exactArgs(1),
	RunE:  runTaskEvents,
}

var taskStatusCmd = &cobra.Command{
	Use:   "status <task-id>",
	Short: "Project task, runtime, provider, slot, and stall conditions",
	Args:  exactArgs(1),
	RunE:  runTaskStatus,
}

var taskEventCmd = &cobra.Command{
	Use:   "event",
	Short: "Record task-scoped lifecycle observations",
}

var taskEventAddCmd = &cobra.Command{
	Use:   "add <task-id>",
	Short: "Append an idempotent task-scoped lifecycle observation",
	Args:  exactArgs(1),
	RunE:  runTaskEventAdd,
}

func init() {
	taskCmd.GroupID = groupCore
	taskCmd.AddCommand(taskEventsCmd)
	taskCmd.AddCommand(taskStatusCmd)
	taskCmd.AddCommand(taskEventCmd)
	taskEventCmd.AddCommand(taskEventAddCmd)
	rootCmd.AddCommand(taskCmd)

	taskEventsCmd.Flags().String("output", "table", "Output format: table or json")
	taskEventsCmd.Flags().Int64("since", 0, "Only return events after this sequence")
	taskStatusCmd.Flags().String("output", "table", "Output format: table or json")

	taskEventAddCmd.Flags().String("id", "", "Caller-stable idempotency key (required)")
	taskEventAddCmd.Flags().String("type", "", "Event type: provider.started, provider.exited, wrapper.exited, or journal.delivery_acked (required)")
	taskEventAddCmd.Flags().String("component", "", "Emitter component: provider, wrapper, or journal (required)")
	taskEventAddCmd.Flags().String("time", "", "Occurrence time in RFC3339 format (defaults to server receipt time)")
	taskEventAddCmd.Flags().String("data", "{}", "JSON object with event-specific non-secret facts")
	taskEventAddCmd.Flags().Int32("schema-version", 1, "Positive event payload schema version")
	taskEventAddCmd.Flags().String("output", "json", "Output format: table or json")
}

func runTaskEvents(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	since, _ := cmd.Flags().GetInt64("since")
	if since < 0 {
		return fmt.Errorf("--since must not be negative")
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	path := "/api/tasks/" + url.PathEscape(args[0]) + "/events"
	if since > 0 {
		path += "?since=" + strconv.FormatInt(since, 10)
	}
	var events []map[string]any
	if err := client.GetJSON(ctx, path, &events); err != nil {
		return fmt.Errorf("list task events: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, events)
	}
	rows := make([][]string, 0, len(events))
	for _, event := range events {
		rows = append(rows, []string{
			strVal(event, "sequence"),
			strVal(event, "type"),
			strVal(event, "source"),
			strVal(event, "time"),
		})
	}
	cli.PrintTable(os.Stdout, []string{"SEQ", "TYPE", "SOURCE", "TIME"}, rows)
	return nil
}

func runTaskStatus(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var status map[string]any
	if err := client.GetJSON(ctx, "/api/tasks/"+url.PathEscape(args[0])+"/status", &status); err != nil {
		return fmt.Errorf("get task status: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, status)
	}

	conditions, _ := status["conditions"].([]any)
	rows := make([][]string, 0, len(conditions))
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		rows = append(rows, []string{
			strVal(condition, "type"),
			strVal(condition, "status"),
			strVal(condition, "reason"),
			strVal(condition, "last_transition_time"),
		})
	}
	cli.PrintTable(os.Stdout, []string{"CONDITION", "STATUS", "REASON", "LAST_TRANSITION"}, rows)
	return nil
}

func runTaskEventAdd(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	eventID, _ := cmd.Flags().GetString("id")
	eventType, _ := cmd.Flags().GetString("type")
	component, _ := cmd.Flags().GetString("component")
	if eventID == "" || eventType == "" || component == "" {
		return fmt.Errorf("--id, --type, and --component are required")
	}
	schemaVersion, _ := cmd.Flags().GetInt32("schema-version")
	if schemaVersion < 1 {
		return fmt.Errorf("--schema-version must be positive")
	}

	dataValue, _ := cmd.Flags().GetString("data")
	var data map[string]any
	if err := json.Unmarshal([]byte(dataValue), &data); err != nil {
		return fmt.Errorf("parse --data: %w", err)
	}
	body := map[string]any{
		"id":             eventID,
		"type":           eventType,
		"component":      component,
		"schema_version": schemaVersion,
		"data":           data,
	}
	if timeValue, _ := cmd.Flags().GetString("time"); timeValue != "" {
		parsed, err := time.Parse(time.RFC3339Nano, timeValue)
		if err != nil {
			return fmt.Errorf("parse --time: %w", err)
		}
		body["time"] = parsed.UTC().Format(time.RFC3339Nano)
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var event map[string]any
	if err := client.PostJSON(ctx, "/api/tasks/"+url.PathEscape(args[0])+"/events", body, &event); err != nil {
		return fmt.Errorf("record task event: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		cli.PrintTable(os.Stdout, []string{"SEQ", "TYPE", "TIME"}, [][]string{{
			strVal(event, "sequence"),
			strVal(event, "type"),
			strVal(event, "time"),
		}})
		return nil
	}
	return cli.PrintJSON(os.Stdout, event)
}
