package response

import (
	"net/http/httptest"
	"testing"
)

func TestParseListParamsDefaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	p := ParseListParams(r)

	if p.Limit != DefaultLimit {
		t.Errorf("Limit = %d, want %d", p.Limit, DefaultLimit)
	}
	if p.Offset != 0 {
		t.Errorf("Offset = %d, want 0", p.Offset)
	}
	if p.Status != "" {
		t.Errorf("Status = %q, want empty", p.Status)
	}
	if p.NodeID != "" {
		t.Errorf("NodeID = %q, want empty", p.NodeID)
	}
	if p.ProjectID != "" {
		t.Errorf("ProjectID = %q, want empty", p.ProjectID)
	}
}

func TestParseListParamsCustomValues(t *testing.T) {
	r := httptest.NewRequest("GET", "/?limit=25&offset=100&status=active&node_id=abc&project_id=def", nil)
	p := ParseListParams(r)

	if p.Limit != 25 {
		t.Errorf("Limit = %d, want 25", p.Limit)
	}
	if p.Offset != 100 {
		t.Errorf("Offset = %d, want 100", p.Offset)
	}
	if p.Status != "active" {
		t.Errorf("Status = %q, want active", p.Status)
	}
	if p.NodeID != "abc" {
		t.Errorf("NodeID = %q, want abc", p.NodeID)
	}
	if p.ProjectID != "def" {
		t.Errorf("ProjectID = %q, want def", p.ProjectID)
	}
}

func TestParseListParamsMaxLimit(t *testing.T) {
	r := httptest.NewRequest("GET", "/?limit=9999", nil)
	p := ParseListParams(r)

	if p.Limit != MaxLimit {
		t.Errorf("Limit = %d, want %d (clamped to max)", p.Limit, MaxLimit)
	}
}

func TestParseListParamsInvalidValues(t *testing.T) {
	tests := []struct {
		url        string
		wantLimit  int
		wantOffset int
	}{
		{"/?limit=abc", DefaultLimit, 0},
		{"/?limit=-5", DefaultLimit, 0},
		{"/?limit=0", DefaultLimit, 0},
		{"/?offset=abc", DefaultLimit, 0},
		{"/?offset=-1", DefaultLimit, 0},
	}

	for _, tt := range tests {
		r := httptest.NewRequest("GET", tt.url, nil)
		p := ParseListParams(r)

		if p.Limit != tt.wantLimit {
			t.Errorf("URL %q: Limit = %d, want %d", tt.url, p.Limit, tt.wantLimit)
		}
		if p.Offset != tt.wantOffset {
			t.Errorf("URL %q: Offset = %d, want %d", tt.url, p.Offset, tt.wantOffset)
		}
	}
}
