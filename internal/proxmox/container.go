package proxmox

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// ListContainers returns all LXC containers on the configured node.
func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	node, err := c.resolveNode(ctx)
	if err != nil {
		return nil, err
	}

	var containers []Container
	if err := c.get(ctx, fmt.Sprintf("nodes/%s/lxc", node), &containers); err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	return containers, nil
}

// GetContainerStatus returns detailed status for a specific LXC container.
func (c *Client) GetContainerStatus(ctx context.Context, vmid int) (*ContainerStatus, error) {
	node, err := c.resolveNode(ctx)
	if err != nil {
		return nil, err
	}

	var status ContainerStatus
	if err := c.get(ctx, fmt.Sprintf("nodes/%s/lxc/%d/status/current", node, vmid), &status); err != nil {
		return nil, fmt.Errorf("get container status: %w", err)
	}
	return &status, nil
}

// CloneContainer clones an LXC template/container to create a new container.
// Returns a Task that can be used to wait for the clone operation to complete.
func (c *Client) CloneContainer(ctx context.Context, templateID int, opts CloneOptions) (*Task, error) {
	if opts.NewID < 100 {
		return nil, fmt.Errorf("clone container: NewID must be >= 100, got %d", opts.NewID)
	}

	node, err := c.resolveNode(ctx)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("newid", strconv.Itoa(opts.NewID))
	if opts.Hostname != "" {
		params.Set("hostname", opts.Hostname)
	}
	if opts.Description != "" {
		params.Set("description", opts.Description)
	}
	if opts.Full {
		params.Set("full", "1")
	}
	if opts.Storage != "" {
		params.Set("storage", opts.Storage)
	}
	if opts.Pool != "" {
		params.Set("pool", opts.Pool)
	}

	upid, err := c.post(ctx, fmt.Sprintf("nodes/%s/lxc/%d/clone", node, templateID), params)
	if err != nil {
		return nil, fmt.Errorf("clone container: %w", err)
	}

	return &Task{UPID: upid, node: node, client: c}, nil
}

// StartContainer starts an LXC container.
// Returns a Task that can be used to wait for the start operation to complete.
func (c *Client) StartContainer(ctx context.Context, vmid int) (*Task, error) {
	node, err := c.resolveNode(ctx)
	if err != nil {
		return nil, err
	}

	upid, err := c.post(ctx, fmt.Sprintf("nodes/%s/lxc/%d/status/start", node, vmid), nil)
	if err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}

	return &Task{UPID: upid, node: node, client: c}, nil
}

// StopContainer immediately stops an LXC container (like pulling the power plug).
// Returns a Task that can be used to wait for the stop operation to complete.
func (c *Client) StopContainer(ctx context.Context, vmid int) (*Task, error) {
	node, err := c.resolveNode(ctx)
	if err != nil {
		return nil, err
	}

	upid, err := c.post(ctx, fmt.Sprintf("nodes/%s/lxc/%d/status/stop", node, vmid), nil)
	if err != nil {
		return nil, fmt.Errorf("stop container: %w", err)
	}

	return &Task{UPID: upid, node: node, client: c}, nil
}

// ShutdownContainer gracefully shuts down an LXC container.
// timeout is the number of seconds to wait before forcing a stop.
// Returns a Task that can be used to wait for the shutdown operation to complete.
func (c *Client) ShutdownContainer(ctx context.Context, vmid int, timeout int) (*Task, error) {
	node, err := c.resolveNode(ctx)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	if timeout > 0 {
		params.Set("timeout", strconv.Itoa(timeout))
	}

	upid, err := c.post(ctx, fmt.Sprintf("nodes/%s/lxc/%d/status/shutdown", node, vmid), params)
	if err != nil {
		return nil, fmt.Errorf("shutdown container: %w", err)
	}

	return &Task{UPID: upid, node: node, client: c}, nil
}

// ConfigureNetwork sets the network interface config on a container.
// Should be called on a stopped container before starting it.
// net0Value is the full Proxmox net0 string, e.g. "name=eth0,bridge=vmbr0,ip=10.10.10.5/24,gw=10.10.10.1,firewall=1,type=veth"
func (c *Client) ConfigureNetwork(ctx context.Context, vmid int, net0Value string) error {
	node, err := c.resolveNode(ctx)
	if err != nil {
		return err
	}
	params := url.Values{}
	params.Set("net0", net0Value)
	if err := c.put(ctx, fmt.Sprintf("nodes/%s/lxc/%d/config", node, vmid), params); err != nil {
		return fmt.Errorf("configure network: %w", err)
	}
	return nil
}

// DeleteContainer removes an LXC container. If force is true, the container
// is stopped first if running.
// Returns a Task that can be used to wait for the delete operation to complete.
func (c *Client) DeleteContainer(ctx context.Context, vmid int, force bool) (*Task, error) {
	node, err := c.resolveNode(ctx)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	if force {
		params.Set("force", "1")
	}

	upid, err := c.delete(ctx, fmt.Sprintf("nodes/%s/lxc/%d", node, vmid), params)
	if err != nil {
		return nil, fmt.Errorf("delete container: %w", err)
	}

	return &Task{UPID: upid, node: node, client: c}, nil
}
