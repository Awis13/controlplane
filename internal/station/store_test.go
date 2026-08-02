package station

import (
	"strings"
	"testing"
)

func TestLikePattern_EscapesMetacharacters(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"plain term", "techno", `%techno%`},
		{"percent is literal", "100%", `%100\%%`},
		{"underscore is literal", "a_b", `%a\_b%`},
		{"backslash is literal", `a\b`, `%a\\b%`},
		{"lone backslash", `\`, `%\\%`},
		{"escape char is not doubled twice", `\%`, `%\\\%%`},
		{"all three together", `50%_off\`, `%50\%\_off\\%`},
		{"empty term", "", `%%`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := likePattern(tt.query); got != tt.want {
				t.Errorf("likePattern(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

// TestBuildListPublicQuery_SearchClause pins that a search term reaches the
// query escaped and that the comparison declares the escape character it uses.
func TestBuildListPublicQuery_SearchClause(t *testing.T) {
	where, _, args := buildListPublicQuery(ListPublicParams{Query: "100%"})

	if !strings.Contains(where, "ILIKE $1") {
		t.Errorf("where = %q, want an ILIKE on $1", where)
	}
	if strings.Count(where, `ESCAPE '\'`) != 3 {
		t.Errorf("where = %q, want ESCAPE on all three comparisons", where)
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want one", args)
	}
	if args[0] != `%100\%%` {
		t.Errorf("arg = %q, want %q", args[0], `%100\%%`)
	}
}

func TestBuildListPublicQuery_NoSearchTermNoClause(t *testing.T) {
	where, _, args := buildListPublicQuery(ListPublicParams{})

	if strings.Contains(where, "ILIKE") {
		t.Errorf("where = %q, want no ILIKE without a search term", where)
	}
	if where != "WHERE is_public = true" {
		t.Errorf("where = %q, want the public filter alone", where)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want none", args)
	}
}

// TestBuildListPublicQuery_GenreArgIndex catches the placeholder numbering
// drifting out of step with the argument list when both filters are present.
func TestBuildListPublicQuery_GenreArgIndex(t *testing.T) {
	where, _, args := buildListPublicQuery(ListPublicParams{Query: "dub", Genre: "techno"})

	if !strings.Contains(where, "genre = $2") {
		t.Errorf("where = %q, want genre on $2", where)
	}
	if len(args) != 2 || args[0] != `%dub%` || args[1] != "techno" {
		t.Errorf("args = %v, want [%%dub%% techno]", args)
	}

	where, _, args = buildListPublicQuery(ListPublicParams{Genre: "techno"})
	if !strings.Contains(where, "genre = $1") {
		t.Errorf("where = %q, want genre on $1 without a search term", where)
	}
	if len(args) != 1 || args[0] != "techno" {
		t.Errorf("args = %v, want [techno]", args)
	}
}

func TestBuildListPublicQuery_OrderBy(t *testing.T) {
	tests := []struct {
		sort string
		want string
	}{
		{"", "ORDER BY name"},
		{"name", "ORDER BY name"},
		{"newest", "ORDER BY created_at DESC"},
		{"online_first", "ORDER BY is_online DESC, name"},
		// The handler sorts and pages this case itself and depends on the scan
		// window being deterministic.
		{"listeners", "ORDER BY name"},
		{"nonsense", "ORDER BY name"},
	}

	for _, tt := range tests {
		t.Run(tt.sort, func(t *testing.T) {
			_, orderBy, _ := buildListPublicQuery(ListPublicParams{Sort: tt.sort})
			if orderBy != tt.want {
				t.Errorf("orderBy for sort=%q = %q, want %q", tt.sort, orderBy, tt.want)
			}
		})
	}
}
