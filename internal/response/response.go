package response

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// JSON writes a JSON response with the given status code.
func JSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

// Error writes a JSON error response.
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"error": message})
}

// Decode reads and decodes JSON from the request body into dst.
// Limits body to 1MB and rejects unknown fields.
func Decode(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20) // 1MB limit
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// ValidUUID returns true if s is a valid UUID string.
func ValidUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}
