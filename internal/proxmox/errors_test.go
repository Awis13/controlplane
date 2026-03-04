package proxmox

import (
	"fmt"
	"testing"
)

func TestIsContainerNotFound(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"does not exist", fmt.Errorf("CT 105 does not exist"), true},
		{"no such container", fmt.Errorf("no such container 105"), true},
		{"proxmox api error with does not exist", &APIError{
			StatusCode: 500,
			Status:     "500 Internal Server Error",
			Errors:     map[string]string{"vmid": "CT 105 does not exist"},
		}, true},
		{"task error with does not exist", &TaskError{
			UPID:       "UPID:node:001",
			ExitStatus: "ERROR: CT 105 does not exist",
			Type:       "vzdestroy",
		}, true},
		{"wrapped api error", fmt.Errorf("delete failed: %w", &APIError{
			StatusCode: 500,
			Status:     "500 Internal Server Error",
			Errors:     map[string]string{"vmid": "CT 200 does not exist"},
		}), true},
		{"wrapped task error", fmt.Errorf("task wait: %w", &TaskError{
			UPID:       "UPID:node:002",
			ExitStatus: "ERROR: CT 200 does not exist",
			Type:       "vzdestroy",
		}), true},
		{"unrelated error", fmt.Errorf("connection refused"), false},
		{"proxmox timeout", &APIError{
			StatusCode: 504,
			Status:     "504 Gateway Timeout",
		}, false},
		{"node not found (should not match)", fmt.Errorf("node not found"), false},
		{"storage does not exist (matches pattern)", fmt.Errorf("storage does not exist"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsContainerNotFound(tt.err)
			if got != tt.expected {
				t.Errorf("IsContainerNotFound(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}
