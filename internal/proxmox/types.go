package proxmox

import (
	"encoding/json"
	"fmt"
	"strings"
)

// response is the Proxmox API response envelope.
// All Proxmox REST endpoints return {"data": ..., "errors": {...}}.
type response struct {
	Data   json.RawMessage   `json:"data"`
	Errors map[string]string `json:"errors,omitempty"`
}

// APIError represents an error response from the Proxmox API.
type APIError struct {
	StatusCode int               // HTTP status code
	Status     string            // HTTP status text (e.g. "500 Internal Server Error")
	Errors     map[string]string // field-level errors from Proxmox
}

func (e *APIError) Error() string {
	if len(e.Errors) > 0 {
		parts := make([]string, 0, len(e.Errors))
		for k, v := range e.Errors {
			parts = append(parts, fmt.Sprintf("%s: %s", k, v))
		}
		return fmt.Sprintf("proxmox api %s: %s", e.Status, strings.Join(parts, "; "))
	}
	return fmt.Sprintf("proxmox api %s", e.Status)
}

// TaskError is returned when a Proxmox task completes with a non-OK exit status.
type TaskError struct {
	UPID       string
	ExitStatus string
	Type       string
}

func (e *TaskError) Error() string {
	return fmt.Sprintf("proxmox task %s failed: %s (type: %s)", e.UPID, e.ExitStatus, e.Type)
}

// Container represents an LXC container from the list endpoint.
type Container struct {
	VMID     int     `json:"vmid"`
	Name     string  `json:"name"`
	Status   string  `json:"status"` // "running", "stopped"
	CPU      float64 `json:"cpu"`
	Mem      int64   `json:"mem"`      // bytes
	MaxMem   int64   `json:"maxmem"`   // bytes
	Disk     int64   `json:"disk"`     // bytes
	MaxDisk  int64   `json:"maxdisk"`  // bytes
	Uptime   int64   `json:"uptime"`   // seconds
	Template int     `json:"template"` // 1 if template
}

// ContainerStatus represents the detailed status of a single LXC container.
type ContainerStatus struct {
	VMID     int     `json:"vmid"`
	Name     string  `json:"name"`
	Status   string  `json:"status"` // "running", "stopped"
	CPU      float64 `json:"cpu"`
	CPUs     int     `json:"cpus"`
	Mem      int64   `json:"mem"`
	MaxMem   int64   `json:"maxmem"`
	Disk     int64   `json:"disk"`
	MaxDisk  int64   `json:"maxdisk"`
	Swap     int64   `json:"swap"`
	MaxSwap  int64   `json:"maxswap"`
	Uptime   int64   `json:"uptime"`
	PID      int     `json:"pid"`
	Template int     `json:"template"`
	Lock     string  `json:"lock,omitempty"`
	HA       any     `json:"ha,omitempty"`
}

// NodeStatus represents resource usage for a Proxmox node.
type NodeStatus struct {
	CPUUsage      float64 `json:"cpu"`
	MemoryTotal   int64   `json:"memory_total"`
	MemoryUsed    int64   `json:"memory_used"`
	MemoryFree    int64   `json:"memory_free"`
	Uptime        int64   `json:"uptime"`
	KernelVersion string  `json:"kernel_version"`
	PVEVersion    string  `json:"pve_version"`
}

// nodeStatusResponse mirrors the JSON structure from GET /nodes/{node}/status.
type nodeStatusResponse struct {
	CPU    float64 `json:"cpu"`
	Memory struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
		Free  int64 `json:"free"`
	} `json:"memory"`
	Uptime    int64  `json:"uptime"`
	Kversion  string `json:"kversion"`
	PVEVersion string `json:"pveversion"`
}

// nodeListEntry mirrors a single node from GET /nodes.
type nodeListEntry struct {
	Node string `json:"node"`
}

// TaskStatus represents the status of an async Proxmox task.
type TaskStatus struct {
	Status     string `json:"status"`     // "running" or "stopped"
	ExitStatus string `json:"exitstatus"` // "OK" or error message (only when stopped)
	Type       string `json:"type"`       // "vzclone", "vzstart", etc.
	ID         string `json:"id"`         // VMID
	Node       string `json:"node"`
	PID        int    `json:"pid"`
	StartTime  int64  `json:"starttime"`
}

// CloneOptions holds parameters for cloning an LXC container.
type CloneOptions struct {
	NewID       int    // required, target VMID
	Hostname    string // optional
	Description string // optional
	Full        bool   // true = full clone (not linked)
	Storage     string // optional, target storage
	Pool        string // optional, resource pool
}
