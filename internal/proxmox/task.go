package proxmox

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

const (
	defaultPollInterval = 2 * time.Second
	defaultTimeout      = 5 * time.Minute
)

// Task represents an async Proxmox operation identified by a UPID.
type Task struct {
	UPID   string
	node   string
	client *Client
}

// WaitOption configures the behavior of Task.Wait.
type WaitOption func(*waitConfig)

type waitConfig struct {
	pollInterval time.Duration
	timeout      time.Duration
}

// WithPollInterval sets how frequently the task status is polled.
func WithPollInterval(d time.Duration) WaitOption {
	return func(cfg *waitConfig) {
		cfg.pollInterval = d
	}
}

// WithTimeout sets the maximum time to wait for task completion.
func WithTimeout(d time.Duration) WaitOption {
	return func(cfg *waitConfig) {
		cfg.timeout = d
	}
}

// Wait polls the task status until it completes or the context is cancelled.
// Returns nil if the task completed with exit status "OK".
// Returns a TaskError if the task completed with a non-OK exit status.
func (t *Task) Wait(ctx context.Context, opts ...WaitOption) error {
	cfg := &waitConfig{
		pollInterval: defaultPollInterval,
		timeout:      defaultTimeout,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()

	for {
		status, err := t.getStatus(ctx)
		if err != nil {
			return fmt.Errorf("poll task %s: %w", t.UPID, err)
		}

		if status.Status == "stopped" {
			if status.ExitStatus == "OK" {
				return nil
			}
			return &TaskError{
				UPID:       t.UPID,
				ExitStatus: status.ExitStatus,
				Type:       status.Type,
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for task %s: %w", t.UPID, ctx.Err())
		case <-ticker.C:
		}
	}
}

// getStatus fetches the current status of the task.
func (t *Task) getStatus(ctx context.Context) (*TaskStatus, error) {
	// UPID contains colons that must be URL-encoded in the path.
	encodedUPID := url.PathEscape(t.UPID)
	path := fmt.Sprintf("nodes/%s/tasks/%s/status", t.node, encodedUPID)

	var status TaskStatus
	if err := t.client.get(ctx, path, &status); err != nil {
		return nil, err
	}
	return &status, nil
}
