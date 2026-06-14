package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dapr/durabletask-go/api/protos"
	"github.com/dapr/durabletask-go/workflow"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestAddress(t *testing.T) {
	t.Setenv("APP_PORT", "")
	if got := Address(); got != ":7999" {
		t.Errorf("default Address() = %q, want :7999", got)
	}
	t.Setenv("APP_PORT", "9090")
	if got := Address(); got != ":9090" {
		t.Errorf("Address() with APP_PORT=9090 = %q, want :9090", got)
	}
}

func TestNewWorkflowStatus(t *testing.T) {
	created := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	meta := (*workflow.WorkflowMetadata)(&protos.WorkflowMetadata{
		InstanceId:    "abc-123",
		Name:          "PostgresSQLDatabasesPut",
		RuntimeStatus: protos.OrchestrationStatus(workflow.StatusCompleted),
		CreatedAt:     timestamppb.New(created),
		Output:        wrapperspb.String(`{"ok":true}`),
		CustomStatus:  wrapperspb.String("phase-2"),
	})

	got := newWorkflowStatus(meta)
	if got.ID != "abc-123" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.Name != "PostgresSQLDatabasesPut" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.RuntimeStatus != "COMPLETED" {
		t.Errorf("RuntimeStatus = %q, want COMPLETED", got.RuntimeStatus)
	}
	if got.Output != `{"ok":true}` {
		t.Errorf("Output = %q", got.Output)
	}
	if got.CustomStatus != "phase-2" {
		t.Errorf("CustomStatus = %q", got.CustomStatus)
	}
	if got.CreatedAt != "2026-06-14T10:00:00Z" {
		t.Errorf("CreatedAt = %q", got.CreatedAt)
	}
}

func TestNewWorkflowStatusNilFields(t *testing.T) {
	// A freshly-scheduled workflow has nil timestamps/payloads — must not panic.
	meta := (*workflow.WorkflowMetadata)(&protos.WorkflowMetadata{
		InstanceId:    "x",
		RuntimeStatus: protos.OrchestrationStatus(workflow.StatusRunning),
	})
	got := newWorkflowStatus(meta)
	if got.RuntimeStatus != "RUNNING" {
		t.Errorf("RuntimeStatus = %q, want RUNNING", got.RuntimeStatus)
	}
	if got.CreatedAt != "" || got.Output != "" || got.FailureReason != "" {
		t.Errorf("expected empty optional fields, got %+v", got)
	}
}

func TestHealthz(t *testing.T) {
	mux := newMux(context.Background(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want ok", body["status"])
	}
}

func TestPutWorkflowValidation(t *testing.T) {
	// The validation path returns 400 before touching the (nil) workflow client.
	mux := newMux(context.Background(), nil)

	tests := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"missing name", `{"input":{"x":1}}`},
		{"missing input", `{"name":"PostgresSQLDatabasesPut"}`},
		{"malformed json", `{not json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/workflows", strings.NewReader(tt.body))
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			var er ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil {
				t.Fatalf("invalid error JSON: %v", err)
			}
			if er.Error.Code != "Invalid" {
				t.Errorf("error code = %q, want Invalid", er.Error.Code)
			}
		})
	}
}

func TestMustWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	mustWriteJSON(rec, http.StatusCreated, map[string]any{"id": "z"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
}

func TestMustWriteJSONUnserializable(t *testing.T) {
	rec := httptest.NewRecorder()
	// channels can't be JSON-marshaled → triggers the error path (500).
	mustWriteJSON(rec, http.StatusOK, map[string]any{"bad": make(chan int)})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
