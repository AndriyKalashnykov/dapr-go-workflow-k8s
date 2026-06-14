package server

import (
	"encoding/json"
	"time"

	"github.com/dapr/durabletask-go/workflow"
)

// WorkflowRequest is the body of a PUT /workflows call.
type WorkflowRequest struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	ID    string          `json:"id,omitempty"`
}

// WorkflowStatus is the stable HTTP representation of a workflow instance's
// metadata. It deliberately decouples the API contract from the durabletask
// protobuf type so the response shape is not tied to generated field names.
type WorkflowStatus struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	RuntimeStatus string `json:"runtimeStatus"`
	CreatedAt     string `json:"createdAt,omitempty"`
	LastUpdatedAt string `json:"lastUpdatedAt,omitempty"`
	Output        string `json:"output,omitempty"`
	CustomStatus  string `json:"customStatus,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
}

func newWorkflowStatus(meta *workflow.WorkflowMetadata) WorkflowStatus {
	status := WorkflowStatus{
		ID:            meta.InstanceId,
		Name:          meta.Name,
		RuntimeStatus: meta.String(),
		Output:        meta.Output.GetValue(),
		CustomStatus:  meta.CustomStatus.GetValue(),
	}
	if meta.CreatedAt != nil {
		status.CreatedAt = meta.CreatedAt.AsTime().Format(time.RFC3339)
	}
	if meta.LastUpdatedAt != nil {
		status.LastUpdatedAt = meta.LastUpdatedAt.AsTime().Format(time.RFC3339)
	}
	if meta.FailureDetails != nil {
		status.FailureReason = meta.FailureDetails.GetErrorMessage()
	}
	return status
}
