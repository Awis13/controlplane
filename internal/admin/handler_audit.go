package admin

import (
	"log/slog"
	"net/http"

	"controlplane/internal/audit"
	"controlplane/internal/response"
)

func (h *Handler) auditPage(w http.ResponseWriter, r *http.Request) {
	params := response.ParseListParams(r)
	entityType := r.URL.Query().Get("entity_type")
	action := r.URL.Query().Get("action")

	var entries []audit.Entry
	var total int
	var err error

	if h.auditStore != nil {
		entries, total, err = h.auditStore.List(r.Context(), params.Limit, params.Offset, entityType, action)
		if err != nil {
			slog.Error("admin: list audit", "error", err)
			http.Error(w, "internal error", 500)
			return
		}
	}

	if entries == nil {
		entries = []audit.Entry{}
	}

	data := struct {
		pageData
		Entries          []audit.Entry
		Total            int
		Limit            int
		Offset           int
		HasMore          bool
		FilterEntityType string
		FilterAction     string
	}{
		pageData: newPage(r, "Audit Log", "audit", []breadcrumb{
			{Label: "Audit Log"},
		}),
		Entries:          entries,
		Total:            total,
		Limit:            params.Limit,
		Offset:           params.Offset,
		HasMore:          params.Offset+len(entries) < total,
		FilterEntityType: entityType,
		FilterAction:     action,
	}

	if err := h.tmpl.RenderPage(w, "audit", data); err != nil {
		slog.Error("admin: render audit", "error", err)
	}
}
