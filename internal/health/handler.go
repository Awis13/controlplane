package health

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"controlplane/internal/response"
)

type Handler struct {
	pool *pgxpool.Pool
}

func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.pool.Ping(r.Context()); err != nil {
		response.Error(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
