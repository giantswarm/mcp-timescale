package sqlguard

import (
	"strings"
)

type tokKind int

const (
	tokWord        tokKind = iota // keyword or unquoted identifier, lowercased
	tokQuotedIdent                // "identifier", text is the unquoted content
	tokString                     // 'literal', E'literal', $tag$literal$tag$
	tokNumber                     // numeric literal
	tokParam                      // $1 positional parameter
	tokPunct                      // single operator or punctuation character
)

type token struct {
	kind tokKind
	text string
	pos  int // byte offset of the token start in the input
}

// tokenize splits sql into tokens, skipping whitespace and comments. String
// literals and quoted identifiers become single tokens so keywords inside
// them are never mistaken for statements. Unterminated strings, quoted
// identifiers or block comments are rejected.
func tokenize(sql string) ([]token, error) {
	var toks []token
	n := len(sql)
	i := 0
	for i < n {
		c := sql[i]
		switch {
		case isSpace(c):
			i++
		case c == '-' && i+1 < n && sql[i+1] == '-':
			// Line comment.
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			end, ok := skipBlockComment(sql, i)
			if !ok {
				return nil, reject("unterminated block comment")
			}
			i = end
		case c == '\'':
			end, ok := skipQuoted(sql, i+1, '\'')
			if !ok {
				return nil, reject("unterminated string literal")
			}
			toks = append(toks, token{kind: tokString, pos: i})
			i = end
		case c == '"':
			end, ok := skipQuoted(sql, i+1, '"')
			if !ok {
				return nil, reject("unterminated quoted identifier")
			}
			toks = append(toks, token{kind: tokQuotedIdent, text: strings.ReplaceAll(sql[i+1:end-1], `""`, `"`), pos: i})
			i = end
		case c == '$':
			end, kind, ok := scanDollar(sql, i)
			if !ok {
				return nil, reject("unterminated dollar-quoted string")
			}
			toks = append(toks, token{kind: kind, pos: i})
			i = end
		case isDigit(c) || (c == '.' && i+1 < n && isDigit(sql[i+1])):
			start := i
			for i < n && (isDigit(sql[i]) || sql[i] == '.' || sql[i] == 'e' || sql[i] == 'E' || sql[i] == '_' ||
				((sql[i] == '+' || sql[i] == '-') && (sql[i-1] == 'e' || sql[i-1] == 'E'))) {
				i++
			}
			toks = append(toks, token{kind: tokNumber, text: sql[start:i], pos: start})
		case isIdentStart(c):
			start := i
			for i < n && isIdentPart(sql[i]) {
				i++
			}
			word := sql[start:i]
			// E'...' (backslash escapes), U&'...', B'...', X'...', N'...'
			// are string literals introduced by a one-letter prefix.
			if i < n && sql[i] == '\'' && len(word) == 1 {
				switch word[0] {
				case 'e', 'E':
					end, ok := skipEscapedString(sql, i+1)
					if !ok {
						return nil, reject("unterminated string literal")
					}
					toks = append(toks, token{kind: tokString, pos: start})
					i = end
					continue
				case 'b', 'B', 'x', 'X', 'n', 'N', 'u', 'U':
					end, ok := skipQuoted(sql, i+1, '\'')
					if !ok {
						return nil, reject("unterminated string literal")
					}
					toks = append(toks, token{kind: tokString, pos: start})
					i = end
					continue
				}
			}
			if i+1 < n && (word == "u" || word == "U") && sql[i] == '&' && (sql[i+1] == '\'' || sql[i+1] == '"') {
				q := sql[i+1]
				end, ok := skipQuoted(sql, i+2, q)
				if !ok {
					return nil, reject("unterminated string literal")
				}
				kind := tokString
				if q == '"' {
					kind = tokQuotedIdent
				}
				toks = append(toks, token{kind: kind, pos: start})
				i = end
				continue
			}
			toks = append(toks, token{kind: tokWord, text: strings.ToLower(word), pos: start})
		default:
			toks = append(toks, token{kind: tokPunct, text: string(c), pos: i})
			i++
		}
	}
	return toks, nil
}

// skipBlockComment returns the offset after the block comment starting at
// start (which points at "/*"). Block comments nest in PostgreSQL.
func skipBlockComment(sql string, start int) (int, bool) {
	depth := 0
	i := start
	for i+1 < len(sql) {
		switch {
		case sql[i] == '/' && sql[i+1] == '*':
			depth++
			i += 2
		case sql[i] == '*' && sql[i+1] == '/':
			depth--
			i += 2
			if depth == 0 {
				return i, true
			}
		default:
			i++
		}
	}
	return 0, false
}

// skipQuoted returns the offset after a quoted run that started at start
// (just after the opening quote). A doubled quote is an escaped quote.
func skipQuoted(sql string, start int, q byte) (int, bool) {
	i := start
	for i < len(sql) {
		if sql[i] != q {
			i++
			continue
		}
		if i+1 < len(sql) && sql[i+1] == q {
			i += 2
			continue
		}
		return i + 1, true
	}
	return 0, false
}

// skipEscapedString handles E'...' literals, where a backslash escapes the
// next character and a doubled quote is an escaped quote.
func skipEscapedString(sql string, start int) (int, bool) {
	i := start
	for i < len(sql) {
		switch sql[i] {
		case '\\':
			i += 2
		case '\'':
			if i+1 < len(sql) && sql[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1, true
		default:
			i++
		}
	}
	return 0, false
}

// scanDollar handles "$1" positional parameters and "$tag$...$tag$"
// dollar-quoted strings starting at start (which points at "$").
func scanDollar(sql string, start int) (int, tokKind, bool) {
	i := start + 1
	if i < len(sql) && isDigit(sql[i]) {
		for i < len(sql) && isDigit(sql[i]) {
			i++
		}
		return i, tokParam, true
	}
	// The tag is an identifier without "$": [A-Za-z_][A-Za-z0-9_]* or empty.
	tagStart := i
	for i < len(sql) && (isIdentStart(sql[i]) || (i > tagStart && isDigit(sql[i]))) {
		i++
	}
	if i >= len(sql) || sql[i] != '$' {
		// A lone "$" or "$name" without a closing "$": treat as punctuation
		// so PostgreSQL reports the syntax error.
		return start + 1, tokPunct, true
	}
	delim := sql[start : i+1] // "$tag$"
	end := strings.Index(sql[i+1:], delim)
	if end < 0 {
		return 0, tokString, false
	}
	return i + 1 + end + len(delim), tokString, true
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || isDigit(c) || c == '$'
}
