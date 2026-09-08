package timescale

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/giantswarm/mcp-timescale/internal/config"
)

const (
	tsWithZone = "2026-09-08T12:34:56.789+02:00"
	typeFloat8 = "float8"
	typeTSTZ   = "timestamptz"
)

func TestEncodeValue(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 9, 8, 12, 34, 56, 789000000, time.FixedZone("CEST", 2*3600))
	num := pgtype.Numeric{Int: big.NewInt(123456), Exp: -3, Valid: true}
	nan := pgtype.Numeric{NaN: true, Valid: true}
	iv := pgtype.Interval{Months: 1, Days: 2, Microseconds: 3 * 3600 * 1e6, Valid: true}
	uuid := [16]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}

	cases := []struct {
		name string
		in   any
		typ  string
		want any
	}{
		{"nil", nil, "text", nil},
		{"string", "x", "text", "x"},
		{"int64", int64(42), "int8", int64(42)},
		{"bool", true, "bool", true},
		{"timestamptz keeps zone", ts, typeTSTZ, tsWithZone},
		{"timestamp has no zone", ts.UTC(), typeTimestamp, "2026-09-08T10:34:56.789"},
		{"date", ts, typeDate, "2026-09-08"},
		{"numeric as string", num, "numeric", "123.456"},
		{"numeric nan", nan, "numeric", "NaN"},
		{"interval text", iv, "interval", "1 mon 2 day 03:00:00"},
		{"bytea base64", []byte{0, 1, 2}, "bytea", "AAEC"},
		{"uuid", uuid, "uuid", "12345678-9abc-def0-1234-56789abcdef0"},
		{"inet", netip.MustParseAddr("10.0.0.1"), "inet", "10.0.0.1"},
		{"cidr", netip.MustParsePrefix("10.0.0.0/8"), "cidr", "10.0.0.0/8"},
		{"float nan", math.NaN(), typeFloat8, "NaN"},
		{"float inf", math.Inf(1), typeFloat8, "Infinity"},
		{"float -inf", math.Inf(-1), typeFloat8, "-Infinity"},
		{"float", 1.5, typeFloat8, 1.5},
		{"jsonb object", map[string]any{"a": 1.0, "b": []any{"x"}}, "jsonb", map[string]any{"a": 1.0, "b": []any{"x"}}},
		{"array of ints", []int32{1, 2}, "_int4", []any{int32(1), int32(2)}},
		{"array of times", []time.Time{ts}, "_timestamptz", []any{tsWithZone}},
		{"hardware addr stringer", net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}, "macaddr", "aa:bb:cc:dd:ee:ff"},
		{"pointer", &ts, typeTSTZ, tsWithZone},
		{"nil pointer", (*time.Time)(nil), typeTSTZ, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := encodeValue(tc.in, tc.typ)
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tc.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("encodeValue(%v, %q) = %s, want %s", tc.in, tc.typ, gotJSON, wantJSON)
			}
		})
	}
}

func TestEncodedRowsMarshal(t *testing.T) {
	t.Parallel()
	row := []any{encodeValue(math.NaN(), typeFloat8), encodeValue([]byte("x"), "bytea"), encodeValue(pgtype.Numeric{Int: big.NewInt(1), Valid: true}, "numeric")}
	if _, err := json.Marshal(row); err != nil {
		t.Fatalf("encoded rows must always be JSON-encodable: %v", err)
	}
}

func TestApplicationName(t *testing.T) {
	t.Parallel()
	if got := applicationName("alice@example.com"); got != "mcp-timescale/alice@example.com" {
		t.Errorf("applicationName = %q", got)
	}
	if got := applicationName(""); got != "mcp-timescale/anonymous" {
		t.Errorf("empty caller = %q", got)
	}
	long := applicationName(strings.Repeat("ä", 100))
	if len(long) > maxApplicationName {
		t.Errorf("len = %d, want <= %d", len(long), maxApplicationName)
	}
	if !strings.HasPrefix(long, "mcp-timescale/") || strings.ContainsRune(long, '�') || !utf8Valid(long) {
		t.Errorf("truncation must keep the prefix and cut on a rune boundary: %q", long)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestClassAndDescribe(t *testing.T) {
	t.Parallel()
	pgErr := &pgconn.PgError{Code: "25006", Message: "cannot execute DELETE in a read-only transaction"}
	cases := []struct {
		name      string
		err       error
		class     string
		mentions  string
		mustNotBe string
	}{
		{"nil", nil, "", "", ""},
		{"deadline", context.DeadlineExceeded, "timeout", "timed out", ""},
		{"canceled", context.Canceled, "canceled", "context canceled", ""},
		{"not found", ErrNotFound, "not_found", "not found", ""},
		{"no timescaledb", ErrNoTimescaleDB, "no_timescaledb", "timescaledb extension", ""},
		{"read-only violation", pgErr, "sql_error:25006", "read-only", ""},
		{"statement timeout", &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}, "timeout", "statement timeout", ""},
		{"wrapped pg error", errWrap(pgErr), "sql_error:25006", "SQLSTATE 25006", ""},
		{"dial error", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, "connect", "connection refused", ""},
		{"other", errors.New("boom"), "internal", "boom", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Class(tc.err); got != tc.class {
				t.Errorf("Class = %q, want %q", got, tc.class)
			}
			msg := Describe("demo", tc.err)
			if tc.mentions != "" && !strings.Contains(msg, tc.mentions) {
				t.Errorf("Describe = %q, want it to mention %q", msg, tc.mentions)
			}
		})
	}
}

func errWrap(err error) error { return errors.Join(errors.New("query"), err) }

func TestRegistryConfigHidesCredentials(t *testing.T) {
	t.Parallel()
	reg, err := NewRegistry([]config.Database{
		{Name: "a", Host: "db.example", Port: 5432, DBName: "x", SSLMode: "require", User: "reader", Password: "s3cret", MaxRows: 500, StatementTimeout: 30 * time.Second, MaxConnections: 2},
		{Name: "b", DSN: "postgres://u:p@localhost:5433/other?sslmode=disable", MaxRows: 10, StatementTimeout: 5 * time.Second, MaxConnections: 1}, // #nosec G101 -- fake test credentials
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if reg.Len() != 2 || strings.Join(reg.Names(), ",") != "a,b" {
		t.Fatalf("names = %v", reg.Names())
	}
	a, _ := reg.Get("a")
	cfg := a.Config()
	if cfg.Password != "" || cfg.User != "" || cfg.DSN != "" {
		t.Errorf("Config() must not carry credentials: %+v", cfg)
	}
	if cfg.Host != "db.example" || cfg.Port != 5432 {
		t.Errorf("Config() lost endpoint: %+v", cfg)
	}
	b, _ := reg.Get("b")
	bc := b.Config()
	if bc.Host != "localhost" || bc.Port != 5433 || bc.DBName != "other" || bc.SSLMode != "disable" || bc.DSN != "" || bc.Password != "" {
		t.Errorf("DSN database Config() = %+v", bc)
	}
	if reg.MaxStatementTimeout() != 30*time.Second {
		t.Errorf("MaxStatementTimeout = %s", reg.MaxStatementTimeout())
	}
	if a.clampRows(0) != 500 || a.clampRows(10) != 10 || a.clampRows(9999) != 500 {
		t.Error("clampRows must bound to [1, maxRows]")
	}
	if a.poolCfg.ConnConfig.RuntimeParams["application_name"] != ApplicationName || a.poolCfg.MaxConns != 2 {
		t.Errorf("pool config: %+v", a.poolCfg.ConnConfig.RuntimeParams)
	}
	reg.Close()
}

func TestRegistryRejectsBadDSN(t *testing.T) {
	t.Parallel()
	if _, err := NewRegistry([]config.Database{{Name: "x", DSN: "postgres://[::1/bad", MaxRows: 1, StatementTimeout: time.Second, MaxConnections: 1}}); err == nil {
		t.Error("unparseable DSN must fail")
	}
}
