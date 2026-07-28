package postgres

import (
	"context"
	"database/sql/driver"
	"regexp"
	"strconv"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/application"
)

// The generated repository queries intentionally use SQLite's '?' placeholder
// because Personal is the first shipping profile. This narrow database/sql
// adapter keeps the query source shared while translating placeholders and the
// two SQLite INSERT OR IGNORE statements to PostgreSQL syntax at the driver
// boundary. It does not hide dialect-specific migrations or RLS policy.
type connector struct{ inner driver.Connector }

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &connWrapper{Conn: conn}, nil
}

func (c connector) Driver() driver.Driver { return c.inner.Driver() }

type connWrapper struct{ driver.Conn }

func (c *connWrapper) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(rewriteSQL(query))
	if err != nil {
		return nil, err
	}
	return &stmtWrapper{Stmt: stmt}, nil
}

func (c *connWrapper) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if preparer, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err := preparer.PrepareContext(ctx, rewriteSQL(query))
		if err != nil {
			return nil, err
		}
		return &stmtWrapper{Stmt: stmt}, nil
	}
	return c.Prepare(query)
}

func (c *connWrapper) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if execer, ok := c.Conn.(driver.ExecerContext); ok {
		if err := c.setSessionContext(ctx, execer); err != nil {
			return nil, err
		}
		return execer.ExecContext(ctx, rewriteSQL(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *connWrapper) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := c.Conn.(driver.QueryerContext); ok {
		if execer, ok := c.Conn.(driver.ExecerContext); ok {
			if err := c.setSessionContext(ctx, execer); err != nil {
				return nil, err
			}
		}
		return queryer.QueryContext(ctx, rewriteSQL(query), args)
	}
	return nil, driver.ErrSkip
}

// database/sql may execute a scoped read without opening an explicit
// transaction. Set the connection-local variables immediately before that
// statement; every subsequent scoped statement overwrites them, and the
// resetter clears them when a pooled connection is returned.
func (c *connWrapper) setSessionContext(ctx context.Context, execer driver.ExecerContext) error {
	scope := scopeFromContext(ctx)
	_, err := execer.ExecContext(ctx,
		"SELECT set_config('app.org_id', $1, false), set_config('app.user_id', $2, false), set_config('app.bootstrap', $3, false)",
		[]driver.NamedValue{
			{Name: "org_id", Value: scope.organizationID},
			{Name: "user_id", Value: scope.userID},
			{Name: "bootstrap", Value: boolString(bootstrapContext(ctx))},
		})
	return err
}

func (c *connWrapper) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if begin, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err := begin.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &txWrapper{Tx: tx, scope: scopeFromContext(ctx), bootstrap: bootstrapContext(ctx)}, nil
	}
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return &txWrapper{Tx: tx, scope: scopeFromContext(ctx), bootstrap: bootstrapContext(ctx)}, nil
}

func (c *connWrapper) Ping(ctx context.Context) error {
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c *connWrapper) ResetSession(ctx context.Context) error {
	if execer, ok := c.Conn.(driver.ExecerContext); ok {
		_, _ = execer.ExecContext(ctx, "SELECT set_config('app.org_id', '', false), set_config('app.user_id', '', false), set_config('app.bootstrap', '0', false)", nil)
	}
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *connWrapper) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

type stmtWrapper struct{ driver.Stmt }

// database/sql exposes a transaction's optional context-aware interfaces
// directly on driver.Tx. Keep the same SQL rewrite guarantees inside
// transactions; otherwise the shared repository would work for reads but fail
// on its first transactional INSERT/UPDATE in PostgreSQL.
type txWrapper struct {
	driver.Tx
	scope       scopeValues
	bootstrap   bool
	initialized bool
}

type scopeValues struct {
	organizationID string
	userID         string
}

func scopeFromContext(ctx context.Context) scopeValues {
	scope, ok := application.ScopeFromContext(ctx)
	if !ok {
		return scopeValues{}
	}
	return scopeValues{organizationID: scope.OrganizationID, userID: scope.UserID}
}

type bootstrapContextKey struct{}

func withBootstrapContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, bootstrapContextKey{}, true)
}

func bootstrapContext(ctx context.Context) bool {
	value, _ := ctx.Value(bootstrapContextKey{}).(bool)
	return value
}

func (t *txWrapper) init(ctx context.Context) error {
	if t.initialized {
		return nil
	}
	t.initialized = true
	execer, ok := t.Tx.(driver.ExecerContext)
	if !ok {
		return nil
	}
	_, err := execer.ExecContext(ctx,
		"SELECT set_config('app.org_id', $1, true), set_config('app.user_id', $2, true), set_config('app.bootstrap', $3, true)",
		[]driver.NamedValue{
			{Name: "org_id", Value: t.scope.organizationID},
			{Name: "user_id", Value: t.scope.userID},
			{Name: "bootstrap", Value: boolString(t.bootstrap)},
		})
	return err
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func (t *txWrapper) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := t.init(ctx); err != nil {
		return nil, err
	}
	if execer, ok := t.Tx.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, rewriteSQL(query), args)
	}
	return nil, driver.ErrSkip
}

func (t *txWrapper) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := t.init(ctx); err != nil {
		return nil, err
	}
	if queryer, ok := t.Tx.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, rewriteSQL(query), args)
	}
	return nil, driver.ErrSkip
}

func (t *txWrapper) Prepare(query string) (driver.Stmt, error) {
	if preparer, ok := t.Tx.(interface {
		Prepare(string) (driver.Stmt, error)
	}); ok {
		stmt, err := preparer.Prepare(rewriteSQL(query))
		if err != nil {
			return nil, err
		}
		return &stmtWrapper{Stmt: stmt}, nil
	}
	return nil, driver.ErrSkip
}

func (s *stmtWrapper) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if execer, ok := s.Stmt.(driver.StmtExecContext); ok {
		return execer.ExecContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for i := range args {
		values[i] = args[i].Value
	}
	return s.Stmt.Exec(values)
}

func (s *stmtWrapper) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := s.Stmt.(driver.StmtQueryContext); ok {
		return queryer.QueryContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for i := range args {
		values[i] = args[i].Value
	}
	return s.Stmt.Query(values)
}

var insertIgnorePattern = regexp.MustCompile(`(?is)^\s*INSERT\s+OR\s+IGNORE\s+INTO\s+`)
var returningPattern = regexp.MustCompile(`(?is)\s+RETURNING\s+`)

func rewriteSQL(query string) string {
	if insertIgnorePattern.MatchString(query) {
		query = insertIgnorePattern.ReplaceAllString(query, "INSERT INTO ")
		if match := returningPattern.FindStringIndex(query); match != nil {
			query = query[:match[0]] + " ON CONFLICT DO NOTHING" + query[match[0]:]
		} else {
			trimmed := strings.TrimSpace(query)
			semi := ""
			if strings.HasSuffix(trimmed, ";") {
				trimmed = strings.TrimSuffix(trimmed, ";")
				semi = ";"
			}
			query = trimmed + " ON CONFLICT DO NOTHING" + semi
		}
	}
	var out strings.Builder
	out.Grow(len(query) + 16)
	placeholder := 1
	var quote byte
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if quote != 0 {
			out.WriteByte(ch)
			if ch == quote {
				if i+1 < len(query) && query[i+1] == quote {
					out.WriteByte(query[i+1])
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			out.WriteByte(ch)
			continue
		}
		if ch == '?' {
			out.WriteString("$")
			out.WriteString(strconv.Itoa(placeholder))
			placeholder++
			continue
		}
		out.WriteByte(ch)
	}
	return out.String()
}
