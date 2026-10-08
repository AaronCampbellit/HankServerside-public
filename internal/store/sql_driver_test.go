package store

import "testing"

func TestRebindPlaceholdersPassthroughWithoutPlaceholders(t *testing.T) {
	query := `SELECT id FROM users WHERE email = $1`
	if got := rebindPlaceholders(query); got != query {
		t.Fatalf("rebindPlaceholders = %s, want unchanged", got)
	}
}

func TestRebindPlaceholdersAnonymousDollarQuote(t *testing.T) {
	query := `SELECT $$is ? kept$$, ? AS first`
	want := `SELECT $$is ? kept$$, $1 AS first`
	if got := rebindPlaceholders(query); got != want {
		t.Fatalf("rebindPlaceholders = %s, want %s", got, want)
	}
}

func TestRebindPlaceholdersSkipsSQLLiteralsAndComments(t *testing.T) {
	query := `SELECT '?' AS literal, "col?name", $tag$??$tag$, ? AS first
-- ? comment
/* ? block */
WHERE value = ? AND note = 'it''s ? safe'`
	got := rebindPlaceholders(query)
	want := `SELECT '?' AS literal, "col?name", $tag$??$tag$, $1 AS first
-- ? comment
/* ? block */
WHERE value = $2 AND note = 'it''s ? safe'`
	if got != want {
		t.Fatalf("rebindPlaceholders =\n%s\nwant\n%s", got, want)
	}
}
