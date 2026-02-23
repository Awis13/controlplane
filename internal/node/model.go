package node

import (
	"time"
)

type Node struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	TailscaleIP       string    `json:"tailscale_ip"`
	ProxmoxURL        string    `json:"proxmox_url"`
	APITokenEncrypted string    `json:"-"`
	TotalRAMMB        int       `json:"total_ram_mb"`
	AllocatedRAMMB    int       `json:"allocated_ram_mb"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type CreateNodeRequest struct {
	Name        string `json:"name"`
	TailscaleIP string `json:"tailscale_ip"`
	ProxmoxURL  string `json:"proxmox_url"`
	APIToken    string `json:"api_token"`
	TotalRAMMB  int    `json:"total_ram_mb"`
}
