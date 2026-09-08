package timescale

import (
	"database/sql/driver"
	"encoding/base64"
	"fmt"
	"math"
	"net/netip"
	"reflect"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Type names (as reported by pgx's type map) whose time.Time values carry
// no zone and therefore must not be rendered with one.
const (
	typeTimestamp = "timestamp"
	typeDate      = "date"
)

// encodeValue converts a value returned by pgx's rows.Values() into
// something encoding/json renders faithfully for an LLM client:
//
//   - time.Time -> RFC 3339 (timestamptz), "2006-01-02T15:04:05.999999999"
//     (timestamp without time zone) or "2006-01-02" (date);
//   - pgtype.Numeric and other driver.Valuer types (interval, time, bit
//     strings, ...) -> their PostgreSQL text form as a string, so decimals
//     never lose precision through float64;
//   - []byte -> base64; [16]byte (uuid) -> canonical text;
//   - netip.Addr / netip.Prefix -> text;
//   - NaN / ±Inf floats -> "NaN", "Infinity", "-Infinity" (JSON has no
//     encoding for them);
//   - maps, slices and arrays (json, jsonb, PostgreSQL arrays) -> recursively
//     encoded;
//   - everything else unchanged.
func encodeValue(v any, typeName string) any {
	switch x := v.(type) {
	case nil:
		return nil
	case time.Time:
		switch typeName {
		case typeDate:
			return x.Format("2006-01-02")
		case typeTimestamp:
			return x.Format("2006-01-02T15:04:05.999999999")
		default:
			return x.Format(time.RFC3339Nano)
		}
	case pgtype.Numeric:
		return valuerText(x)
	case []byte:
		return base64.StdEncoding.EncodeToString(x)
	case [16]byte:
		return formatUUID(x)
	case netip.Addr:
		return x.String()
	case netip.Prefix:
		return x.String()
	case float64:
		return encodeFloat(x)
	case float32:
		return encodeFloat(float64(x))
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = encodeValue(e, "")
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = encodeValue(e, "")
		}
		return out
	case driver.Valuer:
		return valuerText(x)
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		return encodeValue(rv.Elem().Interface(), typeName)
	}
	// Named types with a text form (net.HardwareAddr, ...) before the
	// generic slice/map walk.
	if s, ok := v.(fmt.Stringer); ok {
		return s.String()
	}
	switch rv.Kind() { //nolint:exhaustive // every other kind falls through to fmt
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := range rv.Len() {
			out[i] = encodeValue(rv.Index(i).Interface(), "")
		}
		return out
	case reflect.Map:
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[fmt.Sprint(iter.Key().Interface())] = encodeValue(iter.Value().Interface(), "")
		}
		return out
	}
	return fmt.Sprint(v)
}

// valuerText renders a driver.Valuer as text. pgx's Value() methods return
// the PostgreSQL text representation (or nil for NULL).
func valuerText(v driver.Valuer) any {
	dv, err := v.Value()
	if err != nil {
		return fmt.Sprintf("<%T: %v>", v, err)
	}
	switch x := dv.(type) {
	case nil:
		return nil
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return encodeFloat(x)
	case bool:
		return x
	default:
		return fmt.Sprint(x)
	}
}

func encodeFloat(f float64) any {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	default:
		return f
	}
}

func formatUUID(u [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}
