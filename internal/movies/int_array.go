package movies

import (
	"fmt"
	"strconv"
	"strings"
)

// IntArray scans a Postgres int[] column. pgx's database/sql adapter hands
// arrays over in their text form ("{18,53}"), which sqlx cannot put into a
// []int on its own. NULL scans to nil. Integer elements are never quoted, so
// the text form needs no general array parser.
type IntArray []int

func (a *IntArray) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("int array: cannot scan %T", src)
	}

	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return fmt.Errorf("int array: malformed %q", s)
	}
	body := s[1 : len(s)-1]
	if body == "" {
		*a = IntArray{}
		return nil
	}

	parts := strings.Split(body, ",")
	out := make(IntArray, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return fmt.Errorf("int array: element %q: %w", p, err)
		}
		out[i] = n
	}
	*a = out
	return nil
}
