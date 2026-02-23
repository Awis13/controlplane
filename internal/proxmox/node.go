package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// discoverNodeName fetches the list of nodes and returns the first one.
// For a standalone Proxmox setup, there is exactly 1 node.
// This is called internally by resolveNode and should not be called directly.
func (c *Client) discoverNodeName(ctx context.Context) (string, error) {
	var nodes []nodeListEntry
	if err := c.get(ctx, "nodes", &nodes); err != nil {
		return "", fmt.Errorf("discover node name: %w", err)
	}
	if len(nodes) == 0 {
		return "", fmt.Errorf("discover node name: no nodes found")
	}
	return nodes[0].Node, nil
}

// NodeName returns the Proxmox node name. If not set explicitly via WithNodeName,
// it is discovered lazily via GET /api2/json/nodes and cached.
func (c *Client) NodeName(ctx context.Context) (string, error) {
	return c.resolveNode(ctx)
}

// GetNodeStatus returns CPU, memory, and system info for the configured node.
func (c *Client) GetNodeStatus(ctx context.Context) (*NodeStatus, error) {
	node, err := c.resolveNode(ctx)
	if err != nil {
		return nil, err
	}

	var raw nodeStatusResponse
	if err := c.get(ctx, fmt.Sprintf("nodes/%s/status", node), &raw); err != nil {
		return nil, fmt.Errorf("get node status: %w", err)
	}

	return &NodeStatus{
		CPUUsage:      raw.CPU,
		MemoryTotal:   raw.Memory.Total,
		MemoryUsed:    raw.Memory.Used,
		MemoryFree:    raw.Memory.Free,
		Uptime:        raw.Uptime,
		KernelVersion: raw.Kversion,
		PVEVersion:    raw.PVEVersion,
	}, nil
}

// GetNextID returns the next available VMID from the Proxmox cluster.
// Proxmox returns the value as a string (e.g. "105"), so we handle both
// string and numeric JSON representations.
func (c *Client) GetNextID(ctx context.Context) (int, error) {
	var raw json.RawMessage
	if err := c.get(ctx, "cluster/nextid", &raw); err != nil {
		return 0, fmt.Errorf("get next id: %w", err)
	}

	// Try parsing as integer first.
	var id int
	if err := json.Unmarshal(raw, &id); err == nil {
		return id, nil
	}

	// Fall back to string (Proxmox returns "105" as a string).
	var idStr string
	if err := json.Unmarshal(raw, &idStr); err != nil {
		return 0, fmt.Errorf("get next id: unexpected data format: %s", string(raw))
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return 0, fmt.Errorf("get next id: parse %q: %w", idStr, err)
	}
	return id, nil
}
