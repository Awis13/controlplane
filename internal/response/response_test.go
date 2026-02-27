package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSON(t *testing.T) {
	w := httptest.NewRecorder()
	data := map[string]string{"key": "value"}

	JSON(w, http.StatusOK, data)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["key"] != "value" {
		t.Errorf("body key = %q, want %q", got["key"], "value")
	}
}

func TestError(t *testing.T) {
	w := httptest.NewRecorder()

	Error(w, http.StatusBadRequest, "bad input")

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["error"] != "bad input" {
		t.Errorf("error = %q, want %q", got["error"], "bad input")
	}
}

func TestDecode(t *testing.T) {
	body := `{"name":"test","value":42}`
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))

	var dst struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}
	if err := Decode(r, &dst); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if dst.Name != "test" || dst.Value != 42 {
		t.Errorf("got %+v, want {test 42}", dst)
	}
}

func TestDecodeUnknownFields(t *testing.T) {
	body := `{"name":"test","unknown_field":"bad"}`
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))

	var dst struct {
		Name string `json:"name"`
	}
	if err := Decode(r, &dst); err == nil {
		t.Error("Decode should reject unknown fields")
	}
}

func TestDecodeInvalidJSON(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader("not json"))

	var dst struct{}
	if err := Decode(r, &dst); err == nil {
		t.Error("Decode should reject invalid JSON")
	}
}

func TestValidUUID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"550e8400-e29b-41d4-a716-446655440000", true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"not-a-uuid", false},
		{"", false},
		{"550e8400e29b41d4a716446655440000", true}, // without hyphens
		{"550e8400-e29b-41d4-a716-44665544000", false}, // too short
	}

	for _, tt := range tests {
		got := ValidUUID(tt.input)
		if got != tt.want {
			t.Errorf("ValidUUID(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
