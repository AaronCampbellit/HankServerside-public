package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"unicode"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const driverName = "pgx"

type dbTx struct {
	tx *sql.Tx
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, rebindPlaceholders(query), args...)
}

func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, rebindPlaceholders(query), args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, rebindPlaceholders(query), args...)
}

func (s *Store) beginTx(ctx context.Context, opts *sql.TxOptions) (*dbTx, error) {
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &dbTx{tx: tx}, nil
}

func (tx *dbTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.tx.ExecContext(ctx, rebindPlaceholders(query), args...)
}

func (tx *dbTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.tx.QueryContext(ctx, rebindPlaceholders(query), args...)
}

func (tx *dbTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.tx.QueryRowContext(ctx, rebindPlaceholders(query), args...)
}

func (tx *dbTx) Commit() error {
	return tx.tx.Commit()
}

func (tx *dbTx) Rollback() error {
	return tx.tx.Rollback()
}

func rebindPlaceholders(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}

	var builder strings.Builder
	builder.Grow(len(query) + 8)

	index := 1
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch ch {
		case '\'':
			i = copySingleQuotedSQL(&builder, query, i)
			continue
		case '"':
			i = copyDelimitedSQL(&builder, query, i, '"')
			continue
		case '-':
			if i+1 < len(query) && query[i+1] == '-' {
				i = copyLineCommentSQL(&builder, query, i)
				continue
			}
		case '/':
			if i+1 < len(query) && query[i+1] == '*' {
				i = copyBlockCommentSQL(&builder, query, i)
				continue
			}
		case '$':
			if tag, ok := sqlDollarQuoteTag(query, i); ok {
				i = copyDollarQuotedSQL(&builder, query, i, tag)
				continue
			}
		case '?':
			builder.WriteByte('$')
			builder.WriteString(strconv.Itoa(index))
			index++
			continue
		}
		builder.WriteByte(ch)
	}

	return builder.String()
}

func copySingleQuotedSQL(builder *strings.Builder, query string, start int) int {
	builder.WriteByte(query[start])
	for i := start + 1; i < len(query); i++ {
		builder.WriteByte(query[i])
		if query[i] == '\'' {
			if i+1 < len(query) && query[i+1] == '\'' {
				i++
				builder.WriteByte(query[i])
				continue
			}
			return i
		}
	}
	return len(query) - 1
}

func copyDelimitedSQL(builder *strings.Builder, query string, start int, delimiter byte) int {
	builder.WriteByte(query[start])
	for i := start + 1; i < len(query); i++ {
		builder.WriteByte(query[i])
		if query[i] == delimiter {
			if i+1 < len(query) && query[i+1] == delimiter {
				i++
				builder.WriteByte(query[i])
				continue
			}
			return i
		}
	}
	return len(query) - 1
}

func copyLineCommentSQL(builder *strings.Builder, query string, start int) int {
	for i := start; i < len(query); i++ {
		builder.WriteByte(query[i])
		if query[i] == '\n' {
			return i
		}
	}
	return len(query) - 1
}

func copyBlockCommentSQL(builder *strings.Builder, query string, start int) int {
	builder.WriteString("/*")
	for i := start + 2; i < len(query); i++ {
		builder.WriteByte(query[i])
		if query[i] == '*' && i+1 < len(query) && query[i+1] == '/' {
			i++
			builder.WriteByte(query[i])
			return i
		}
	}
	return len(query) - 1
}

func copyDollarQuotedSQL(builder *strings.Builder, query string, start int, tag string) int {
	builder.WriteString(tag)
	end := strings.Index(query[start+len(tag):], tag)
	if end < 0 {
		builder.WriteString(query[start+len(tag):])
		return len(query) - 1
	}
	contentEnd := start + len(tag) + end
	builder.WriteString(query[start+len(tag) : contentEnd+len(tag)])
	return contentEnd + len(tag) - 1
}

func sqlDollarQuoteTag(query string, start int) (string, bool) {
	end := start + 1
	for end < len(query) && query[end] != '$' {
		ch := rune(query[end])
		if !(ch == '_' || unicode.IsLetter(ch) || unicode.IsDigit(ch)) {
			return "", false
		}
		end++
	}
	if end >= len(query) || query[end] != '$' {
		return "", false
	}
	return query[start : end+1], true
}
