package admin

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"controlplane/internal/response"
)

func (h *Handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	dbStatus := "healthy"

	nodes, err := h.nodes.List(r.Context())
	if err != nil {
		slog.Error("admin: settings list nodes", "error", err)
		dbStatus = "error"
	}

	allTenants, err := h.tenants.List(r.Context())
	if err != nil {
		slog.Error("admin: settings list tenants", "error", err)
		dbStatus = "error"
	}

	activeTenants := 0
	for _, t := range allTenants {
		if t.Status == "active" {
			activeTenants++
		}
	}

	var credentials []CredentialInfo
	if h.webauthnStore != nil {
		var credErr error
		credentials, credErr = h.webauthnStore.ListCredentialInfos(r.Context())
		if credErr != nil {
			slog.Error("admin: settings list credentials", "error", credErr)
		}
	}

	data := struct {
		pageData
		DBStatus      string
		NodeCount     int
		ActiveTenants int
		Credentials   []CredentialInfo
	}{
		pageData: newPage(r, "Settings", "settings", []breadcrumb{
			{Label: "Settings"},
		}),
		DBStatus:      dbStatus,
		NodeCount:     len(nodes),
		ActiveTenants: activeTenants,
		Credentials:   credentials,
	}

	if err := h.tmpl.RenderPage(w, "settings", data); err != nil {
		slog.Error("admin: render settings", "error", err)
	}
}

func (h *Handler) deleteCredential(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !response.ValidUUID(id) {
		h.renderFlash(w, "flash_error", "Invalid credential ID")
		return
	}

	// Prevent deleting the last credential (would re-open unauthenticated registration)
	creds, err := h.webauthnStore.ListCredentialInfos(r.Context())
	if err != nil {
		slog.Error("admin: count credentials", "error", err)
		h.renderFlash(w, "flash_error", "Failed to check credentials")
		return
	}
	if len(creds) <= 1 {
		h.renderFlash(w, "flash_error", "Cannot delete the last credential")
		return
	}

	if err := h.webauthnStore.DeleteCredential(r.Context(), id); err != nil {
		slog.Error("admin: delete credential", "error", err)
		h.renderFlash(w, "flash_error", "Failed to delete credential")
		return
	}

	w.Header().Set("HX-Redirect", "/admin/settings")
	w.WriteHeader(http.StatusOK)
}
