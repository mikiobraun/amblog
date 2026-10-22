// Package paging implements the ?before= cursor of RFC 5005 paged feeds, as
// used by amfeed and amagg.
package paging

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cursor marks a position in a feed ordered by (time desc, seq desc): the
// last entry of the previous page. Its text form is "<time>,<seq>", e.g.
// "2026-10-21T21:07:11Z,3".
//
// Times are RFC 3339 UTC with second precision, so they compare correctly as
// strings; that is why ParseCursor accepts only that exact format.
type Cursor struct {
	Time string
	Seq  int64
}

func (c Cursor) String() string { return c.Time + "," + strconv.FormatInt(c.Seq, 10) }

func ParseCursor(s string) (Cursor, error) {
	ts, seqStr, ok := strings.Cut(s, ",")
	if !ok {
		return Cursor{}, fmt.Errorf("cursor %q: want <time>,<number>", s)
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil || FormatTime(t) != ts {
		return Cursor{}, fmt.Errorf("cursor %q: time must be an RFC 3339 UTC timestamp like 2026-10-21T21:07:11Z", s)
	}
	seq, err := strconv.ParseInt(seqStr, 10, 64)
	if err != nil {
		return Cursor{}, fmt.Errorf("cursor %q: bad number", s)
	}
	return Cursor{Time: ts, Seq: seq}, nil
}

// FormatTime formats t the way cursors and stored sort keys need it: RFC 3339,
// UTC, second precision.
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }
