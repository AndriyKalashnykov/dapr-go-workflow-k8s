package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/dapr/durabletask-go/workflow"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// DefaultPort is the HTTP listen port used when APP_PORT is not set. It must
// match the `--app-port` passed to the Dapr sidecar (see run-dapr.sh) and the
// APP_INTERNAL_PORT build arg in the Dockerfile.
const DefaultPort = "7999"

// Address returns the TCP listen address, honoring the APP_PORT env var.
func Address() string {
	port := os.Getenv("APP_PORT")
	if port == "" {
		port = DefaultPort
	}
	return ":" + port
}

func Start(ctx context.Context, services map[string]context.CancelFunc, wfClient *workflow.Client) error {
	mux := newMux(ctx, wfClient)

	addr := Address()
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
		// Mitigate Slowloris — bound how long a client may take to send headers.
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(l net.Listener) context.Context {
			return ctx
		},
	}

	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("error creating listener: %w", err)
	}

	slog.InfoContext(ctx, "Server is listening", slog.String("address", listener.Addr().String()))

	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			slog.InfoContext(ctx, "server shutdown gracefully")
		} else {
			slog.ErrorContext(ctx, "server error", slog.Any("error", err))
		}
	}()

	services["http"] = func() {
		if err := server.Shutdown(ctx); err != nil {
			slog.ErrorContext(ctx, "server shutdown error", slog.Any("error", err))
		}
	}

	return nil
}

// newMux builds the HTTP routes. Extracted from Start so handlers can be
// exercised with httptest without binding a TCP port.
func newMux(ctx context.Context, wfClient *workflow.Client) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		mustWriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /workflows/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		slog.InfoContext(ctx, "Fetching workflow metadata", slog.String("id", id))

		metadata, err := wfClient.FetchWorkflowMetadata(r.Context(), id, workflow.WithFetchPayloads(true))
		if err != nil {
			mustWriteError(w, http.StatusInternalServerError, "Internal", err)
			return
		}

		mustWriteJSON(w, http.StatusOK, newWorkflowStatus(metadata))
	})

	mux.HandleFunc("PUT /workflows", func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(r.Body)
		defer func() { _ = r.Body.Close() }()

		request := WorkflowRequest{}
		err := decoder.Decode(&request)
		if err != nil {
			mustWriteError(w, http.StatusBadRequest, "Invalid", err)
			return
		}

		if request.Name == "" || len(request.Input) == 0 {
			mustWriteError(w, http.StatusBadRequest, "Invalid", errors.New("name and input are required"))
			return
		}

		slog.InfoContext(ctx, "Starting new workflow", slog.String("name", request.Name))

		opts := []workflow.NewWorkflowOptions{
			workflow.WithRawInput(wrapperspb.String(string(request.Input))),
		}
		if request.ID != "" {
			opts = append(opts, workflow.WithInstanceID(request.ID))
		}

		id, err := wfClient.ScheduleWorkflow(r.Context(), request.Name, opts...)
		if err != nil {
			mustWriteError(w, http.StatusInternalServerError, "Internal", err)
			return
		}

		slog.InfoContext(ctx, "Workflow started", slog.String("id", id), slog.String("name", request.Name))
		mustWriteJSON(w, http.StatusCreated, map[string]any{"id": id})
	})

	return mux
}

func mustWriteJSON(w http.ResponseWriter, code int, v any) {
	bs, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		mustWriteError(w, http.StatusInternalServerError, "Internal", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(bs)
}

func mustWriteError(w http.ResponseWriter, statusCode int, errorCode string, err error) {
	e := ErrorResponse{
		Error: ErrorDetails{
			Code:    errorCode,
			Message: err.Error(),
		},
	}

	// This should never fail.
	bs, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(bs)
}
