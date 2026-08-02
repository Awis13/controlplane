package user

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// User represents a registered end user.
type User struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"` // never expose in JSON
	DisplayName  string    `json:"display_name"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// NormalizeEmail canonicalizes an address for storage and lookup: surrounding
// whitespace removed and case folded. Email domains are case-insensitive and
// no mainstream provider treats the local part as case-sensitive, so folding
// the whole address is what users expect. Without it, the unique constraint
// sees two spellings of one address as two accounts.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
