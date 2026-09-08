// Package sqlguard classifies a SQL text before it reaches PostgreSQL and
// rejects everything that is not a single read-only statement.
//
// The classifier is a heuristic, not a parser: it tokenizes the text
// outside of string literals, quoted identifiers and comments and looks at
// the keywords in statement position. The real read-only guarantee is the
// READ ONLY transaction plus default_transaction_read_only=on on the
// connection; the classifier exists so that a rejected statement produces
// a clear error before any round trip and so that session state can never
// be changed through SET, RESET, DISCARD or set_config.
package sqlguard

import (
	"errors"
	"fmt"
	"strings"
)

// Kind is the statement class of an accepted statement.
type Kind string

// Accepted statement kinds. The first keyword of a statement decides.
const (
	KindSelect  Kind = "select"
	KindWith    Kind = "with"
	KindExplain Kind = "explain"
	KindShow    Kind = "show"
	KindValues  Kind = "values"
	KindTable   Kind = "table"
)

// Statement is an accepted statement.
type Statement struct {
	// SQL is the statement text with surrounding whitespace and a single
	// trailing semicolon removed. Comments are kept; PostgreSQL ignores
	// them.
	SQL string
	// Kind is the statement class derived from the first keyword.
	Kind Kind
}

// Explainable reports whether the statement can be the target of EXPLAIN
// or a cursor: SELECT, WITH ... SELECT, VALUES and TABLE.
func (s Statement) Explainable() bool {
	switch s.Kind {
	case KindSelect, KindWith, KindValues, KindTable:
		return true
	default:
		return false
	}
}

// ErrRejected is wrapped by every classification error so callers can
// distinguish a rejected statement from other failures with errors.Is.
var ErrRejected = errors.New("statement rejected")

func reject(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRejected, fmt.Sprintf(format, args...))
}

// allowedFirst are the keywords a statement may start with.
var allowedFirst = map[string]Kind{
	"select":  KindSelect,
	"with":    KindWith,
	"explain": KindExplain,
	"show":    KindShow,
	"values":  KindValues,
	"table":   KindTable,
}

// denyKeywords are keywords that start a data-modifying, DDL, transaction
// control or session-state statement. They are rejected in statement
// position (see isStatementPosition).
var denyKeywords = map[string]struct{}{
	"insert": {}, "update": {}, "delete": {}, "merge": {}, "truncate": {},
	"alter": {}, "create": {}, "drop": {}, "grant": {}, "revoke": {},
	"copy": {}, "call": {}, "do": {}, "lock": {}, "refresh": {},
	"reindex": {}, "vacuum": {}, "analyze": {}, "analyse": {}, "cluster": {},
	"set": {}, "reset": {}, "listen": {}, "notify": {}, "unlisten": {},
	"prepare": {}, "execute": {}, "deallocate": {}, "begin": {},
	"commit": {}, "rollback": {}, "start": {}, "end": {}, "abort": {},
	"savepoint": {}, "release": {}, "discard": {}, "security": {},
	"declare": {}, "fetch": {}, "move": {}, "close": {}, "load": {},
	"import": {}, "checkpoint": {}, "comment": {},
}

// denyFunctions are functions with server-side side effects that a plain
// reader could still call inside a SELECT: signalling other backends,
// reading server files, changing session settings, taking advisory locks,
// large-object and dblink access, WAL and backup control.
var denyFunctions = map[string]struct{}{
	"pg_terminate_backend": {}, "pg_cancel_backend": {}, "pg_reload_conf": {},
	"pg_rotate_logfile": {}, "pg_promote": {}, "pg_switch_wal": {},
	"pg_create_restore_point": {}, "pg_backup_start": {}, "pg_backup_stop": {},
	"pg_start_backup": {}, "pg_stop_backup": {},
	"pg_read_file": {}, "pg_read_binary_file": {}, "pg_ls_dir": {}, "pg_stat_file": {},
	"pg_file_write": {}, "pg_file_unlink": {}, "pg_file_rename": {},
	"lo_import": {}, "lo_export": {}, "lo_unlink": {}, "lo_creat": {}, "lo_create": {},
	"lo_from_bytea": {}, "lo_put": {}, "lowrite": {}, "lo_open": {},
	"dblink": {}, "dblink_exec": {}, "dblink_connect": {}, "dblink_connect_u": {},
	"dblink_open": {}, "dblink_fetch": {}, "dblink_send_query": {},
	"pg_advisory_lock": {}, "pg_advisory_lock_shared": {}, "pg_advisory_xact_lock": {},
	"pg_advisory_xact_lock_shared": {}, "pg_try_advisory_lock": {},
	"pg_try_advisory_lock_shared": {}, "pg_try_advisory_xact_lock": {},
	"pg_try_advisory_xact_lock_shared": {}, "pg_advisory_unlock": {},
	"pg_advisory_unlock_all": {}, "pg_advisory_unlock_shared": {},
	"set_config": {}, "pg_notify": {}, "pg_logical_emit_message": {},
	"pg_create_physical_replication_slot": {}, "pg_create_logical_replication_slot": {},
	"pg_drop_replication_slot": {}, "pg_replication_origin_create": {},
	"pg_replication_origin_drop": {}, "pg_replication_origin_advance": {},
	"pg_replication_origin_session_setup": {}, "pg_replication_origin_xact_setup": {},
	"pg_replication_slot_advance": {}, "pg_log_backend_memory_contexts": {},
	"pg_export_snapshot": {}, "txid_current": {}, "pg_current_xact_id": {},
}

// Classify checks that sql is exactly one read-only statement and returns
// it with its kind. It wraps ErrRejected for every refusal.
func Classify(sql string) (Statement, error) {
	toks, err := tokenize(sql)
	if err != nil {
		return Statement{}, err
	}
	if len(toks) == 0 {
		return Statement{}, reject("empty statement")
	}

	// A single trailing semicolon is allowed and removed; any other
	// semicolon means more than one statement.
	end := len(sql)
	if last := toks[len(toks)-1]; last.kind == tokPunct && last.text == ";" {
		end = last.pos
		toks = toks[:len(toks)-1]
		if len(toks) == 0 {
			return Statement{}, reject("empty statement")
		}
	}
	for _, t := range toks {
		if t.kind == tokPunct && t.text == ";" {
			return Statement{}, reject("multiple statements are not allowed; send one statement per call")
		}
	}

	first := toks[0]
	if first.kind != tokWord {
		return Statement{}, reject("statement must start with a keyword (SELECT, WITH, VALUES, TABLE, EXPLAIN or SHOW)")
	}
	kind, ok := allowedFirst[first.text]
	if !ok {
		return Statement{}, reject("only read-only statements are allowed (SELECT, WITH ... SELECT, VALUES, TABLE, EXPLAIN, SHOW); got %s", strings.ToUpper(first.text))
	}

	body := toks[1:]
	if kind == KindExplain {
		body, err = explainBody(body)
		if err != nil {
			return Statement{}, err
		}
	}

	if err := scan(body); err != nil {
		return Statement{}, err
	}

	return Statement{SQL: strings.TrimSpace(sql[:end]), Kind: kind}, nil
}

// explainBody consumes EXPLAIN's option list and legacy ANALYZE/VERBOSE
// words and checks that the explained statement is a SELECT-like one.
// It returns the tokens of the explained statement.
func explainBody(toks []token) ([]token, error) {
	i := 0
	if i < len(toks) && toks[i].kind == tokPunct && toks[i].text == "(" {
		closed := false
		depth := 0
		for !closed && i < len(toks) {
			if toks[i].kind == tokPunct {
				switch toks[i].text {
				case "(":
					depth++
				case ")":
					depth--
					closed = depth == 0
				}
			}
			i++
		}
		if !closed {
			return nil, reject("EXPLAIN option list is not closed")
		}
	}
	for i < len(toks) && toks[i].kind == tokWord && (toks[i].text == "analyze" || toks[i].text == "analyse" || toks[i].text == "verbose") {
		i++
	}
	if i >= len(toks) {
		return nil, reject("EXPLAIN needs a statement to explain")
	}
	stmt := toks[i]
	if stmt.kind != tokWord {
		return nil, reject("EXPLAIN must be followed by SELECT, WITH, VALUES or TABLE")
	}
	switch allowedFirst[stmt.text] {
	case KindSelect, KindWith, KindValues, KindTable:
		return toks[i+1:], nil
	default:
		return nil, reject("EXPLAIN may only explain SELECT, WITH ... SELECT, VALUES or TABLE; got %s", strings.ToUpper(stmt.text))
	}
}

// scan walks the tokens after the first keyword and rejects
//
//   - deny keywords in statement position (after "(" when followed by a
//     word or "(", or after ")" at parenthesis depth 0 -- the two places a
//     CTE body or a WITH main statement can begin),
//   - SELECT ... INTO,
//   - row-locking clauses FOR UPDATE / FOR SHARE / FOR NO KEY UPDATE /
//     FOR KEY SHARE,
//   - calls of side-effect functions, quoted or not.
func scan(toks []token) error {
	depth := 0
	for i, t := range toks {
		if t.kind == tokPunct {
			switch t.text {
			case "(":
				depth++
			case ")":
				depth--
			}
			continue
		}
		var next *token
		if i+1 < len(toks) {
			next = &toks[i+1]
		}
		if next != nil && next.kind == tokPunct && next.text == "(" {
			if t.kind == tokWord || t.kind == tokQuotedIdent {
				if _, deny := denyFunctions[strings.ToLower(t.text)]; deny {
					return reject("function %s() is not allowed", t.text)
				}
			}
		}
		if t.kind != tokWord {
			continue
		}
		if t.text == "into" {
			return reject("SELECT ... INTO creates a table and is not allowed")
		}
		if t.text == "for" && next != nil && next.kind == tokWord {
			switch next.text {
			case "update", "share", "no", "key":
				return reject("row locking (FOR UPDATE / FOR SHARE) is not allowed in a read-only session")
			}
		}
		if _, deny := denyKeywords[t.text]; !deny {
			continue
		}
		prev := token{kind: tokWord} // the statement keyword Classify consumed
		if i > 0 {
			prev = toks[i-1]
		}
		if isStatementPosition(prev, next, depth) {
			return reject("%s is not allowed in a read-only statement (double-quote the word if it is a column name)", strings.ToUpper(t.text))
		}
	}
	return nil
}

// isStatementPosition reports whether a keyword between prev and next can
// begin a statement: directly after "(" when something statement-like
// follows (a CTE body or subquery), or directly after ")" at depth 0 (the
// main statement of WITH ... AS (...) <statement>). A keyword followed by
// "," or ")" or an operator is an identifier, not a statement.
func isStatementPosition(prev token, next *token, depth int) bool {
	if prev.kind != tokPunct {
		return false
	}
	switch prev.text {
	case "(":
		if next == nil {
			return false
		}
		return next.kind == tokWord || next.kind == tokQuotedIdent || (next.kind == tokPunct && next.text == "(")
	case ")":
		return depth == 0
	}
	return false
}
