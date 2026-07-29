package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

type lifecycleBackend struct {
	session *agent.Session
}

func (b lifecycleBackend) Execute(context.Context, string, agent.ExecOptions) (*agent.Session, error) {
	return b.session, nil
}

func TestRecordTaskObservationRetriesWithStableIdempotencyKey(t *testing.T) {
	var (
		mu       sync.Mutex
		attempts []TaskObservation
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var observation TaskObservation
		if err := json.NewDecoder(r.Body).Decode(&observation); err != nil {
			t.Errorf("decode observation: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		attempts = append(attempts, observation)
		n := len(attempts)
		mu.Unlock()
		if n == 1 {
			http.Error(w, "transient", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	previousSleep := retrySleep
	retrySleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { retrySleep = previousSleep })

	client := NewClient(srv.URL)
	want := TaskObservation{
		ID:            "stable-event-id",
		Type:          "provider.started",
		Component:     "provider",
		Time:          time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC),
		SchemaVersion: 1,
		Data:          map[string]any{"attempt": float64(1)},
	}
	if err := client.RecordTaskObservation(context.Background(), "task-1", want); err != nil {
		t.Fatalf("RecordTaskObservation: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	if attempts[0].ID != want.ID || attempts[1].ID != want.ID {
		t.Fatalf("retry changed idempotency key: %#v", attempts)
	}
	if !attempts[0].Time.Equal(want.Time) || !attempts[1].Time.Equal(want.Time) {
		t.Fatalf("retry changed occurrence time: %#v", attempts)
	}
}

func TestExecuteAndDrainRecordsExitOnlyAfterProviderResult(t *testing.T) {
	var (
		mu         sync.Mutex
		eventTypes []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/tasks/task-1/events" {
			http.NotFound(w, r)
			return
		}
		var observation TaskObservation
		if err := json.NewDecoder(r.Body).Decode(&observation); err != nil {
			t.Errorf("decode observation: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		eventTypes = append(eventTypes, observation.Type)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	messages := make(chan agent.Message)
	results := make(chan agent.Result, 1)
	close(messages)
	results <- agent.Result{Status: "completed"}
	close(results)

	d := &Daemon{
		client: NewClient(srv.URL),
		cfg:    Config{},
	}
	var seq atomic.Int32
	result, _, err := d.executeAndDrain(
		context.Background(),
		lifecycleBackend{session: &agent.Session{Messages: messages, Result: results}},
		"prompt",
		agent.ExecOptions{},
		slog.Default(),
		"task-1",
		"",
		&seq,
	)
	if err != nil || result.Status != "completed" {
		t.Fatalf("executeAndDrain = %#v, %v", result, err)
	}

	mu.Lock()
	got := append([]string(nil), eventTypes...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "provider.started" || got[1] != "provider.exited" {
		t.Fatalf("event types = %#v, want provider.started then provider.exited", got)
	}
}

func TestExecuteAndDrainDoesNotClaimProviderExitFromCancellation(t *testing.T) {
	var (
		mu         sync.Mutex
		eventTypes []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var observation TaskObservation
		if err := json.NewDecoder(r.Body).Decode(&observation); err != nil {
			t.Errorf("decode observation: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		eventTypes = append(eventTypes, observation.Type)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	messages := make(chan agent.Message)
	close(messages)
	results := make(chan agent.Result)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := &Daemon{client: NewClient(srv.URL), cfg: Config{}}
	var seq atomic.Int32
	result, _, err := d.executeAndDrain(
		ctx,
		lifecycleBackend{session: &agent.Session{Messages: messages, Result: results}},
		"prompt",
		agent.ExecOptions{},
		slog.Default(),
		"task-1",
		"",
		&seq,
	)
	if err != nil || result.Status != "cancelled" {
		t.Fatalf("executeAndDrain = %#v, %v", result, err)
	}

	mu.Lock()
	got := append([]string(nil), eventTypes...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "provider.started" {
		t.Fatalf("event types = %#v, want provider.started without inferred exit", got)
	}
}
