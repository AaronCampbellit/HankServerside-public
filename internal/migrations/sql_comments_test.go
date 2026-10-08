package migrations

import (
	"strings"
	"testing"
)

func TestSQLCommentsDoNotDiscardMigrationStatements(t *testing.T) {
	statements := splitSQLStatements(`-- Ownership; don't infer it.
ALTER TABLE jobs ADD COLUMN agent_id TEXT;
/* nested /* comment ; ' */ with quotes " */
DO $$ BEGIN
 -- A comment within the function belongs to PostgreSQL.
 RAISE EXCEPTION 'preserve -- ownership; /* data */';
END $$;
SELECT '--literal', '/*literal*/'; -- trailing comment`)
	if len(statements) != 3 {
		t.Fatalf("statements = %#v", statements)
	}
	if !strings.HasPrefix(statements[0], "ALTER TABLE") || !strings.Contains(statements[1], "RAISE EXCEPTION") || !strings.Contains(statements[2], "'--literal'") {
		t.Fatalf("statements = %#v", statements)
	}
}
