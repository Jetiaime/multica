package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newTaskStatusTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "status"}
	cmd.Flags().String("output", "table", "")
	return cmd
}

func newTaskEventAddTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "add"}
	cmd.Flags().String("id", "", "")
	cmd.Flags().String("type", "", "")
	cmd.Flags().String("component", "", "")
	cmd.Flags().String("time", "", "")
	cmd.Flags().String("data", "{}", "")
	cmd.Flags().Int32("schema-version", 1, "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func TestRunTaskStatusPrintsConditionProjectionAsJSON(t *testing.T) {
	const taskID = "7b24bff4-e388-4de7-a7e6-eaf6cab76928"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tasks/"+taskID+"/status" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task_id":     taskID,
			"task_status": "running",
			"conditions": []map[string]any{{
				"type":   "SlotHeld",
				"status": "Unknown",
				"reason": "ReleaseUnobserved",
			}},
		})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := newTaskStatusTestCmd()
	_ = cmd.Flags().Set("output", "json")
	out, err := captureStdout(t, func() error { return runTaskStatus(cmd, []string{taskID}) })
	if err != nil {
		t.Fatalf("runTaskStatus: %v", err)
	}
	if !strings.Contains(out, `"status": "Unknown"`) || !strings.Contains(out, `"reason": "ReleaseUnobserved"`) {
		t.Fatalf("stdout = %q, want lossless Unknown condition", out)
	}
}

func TestRunTaskEventAddSendsTaskScopedIdempotencyContract(t *testing.T) {
	const taskID = "7b24bff4-e388-4de7-a7e6-eaf6cab76928"
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/tasks/"+taskID+"/events" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Task-ID"); got != taskID {
			t.Fatalf("X-Task-ID = %q, want %q", got, taskID)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       "event-row-id",
			"task_id":  taskID,
			"sequence": 4,
			"type":     body["type"],
			"time":     body["time"],
		})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_task-token")
	t.Setenv("MULTICA_TASK_ID", taskID)

	cmd := newTaskEventAddTestCmd()
	_ = cmd.Flags().Set("id", "journal-finish-1")
	_ = cmd.Flags().Set("type", "journal.delivery_acked")
	_ = cmd.Flags().Set("component", "journal")
	_ = cmd.Flags().Set("time", "2026-07-29T02:00:00Z")
	_ = cmd.Flags().Set("data", `{"delivery":"finish"}`)
	out, err := captureStdout(t, func() error { return runTaskEventAdd(cmd, []string{taskID}) })
	if err != nil {
		t.Fatalf("runTaskEventAdd: %v", err)
	}

	if body["id"] != "journal-finish-1" || body["type"] != "journal.delivery_acked" || body["component"] != "journal" {
		t.Fatalf("request body = %#v", body)
	}
	if !strings.Contains(out, `"sequence": 4`) {
		t.Fatalf("stdout = %q, want appended event", out)
	}
}
