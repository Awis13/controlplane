package proxmox

import (
	"errors"
	"strings"
)

// IsContainerNotFound checks if a Proxmox error indicates the container doesn't exist.
// Uses errors.As() to properly unwrap APIError and TaskError types.
func IsContainerNotFound(err error) bool {
	if err == nil {
		return false
	}

	// Check APIError (HTTP-level errors from Proxmox)
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return containsNotFoundPattern(apiErr.Error())
	}

	// Check TaskError (async task failures)
	var taskErr *TaskError
	if errors.As(err, &taskErr) {
		return containsNotFoundPattern(taskErr.Error())
	}

	// Fallback: check raw error string for wrapped errors
	return containsNotFoundPattern(err.Error())
}

func containsNotFoundPattern(msg string) bool {
	msg = strings.ToLower(msg)
	// Proxmox uses "CT {id} does not exist" for missing containers
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "no such container")
}
