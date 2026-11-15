package probe

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// query= on a MYSQL or POSTGRES monitor runs one SELECT whose first
// column of the first row is a number: that number is the value the rules
// judge. A check must never change anything, so three things hold: the
// statement is a single SELECT (no second statement, no INTO), the session
// is made read-only before it runs (PostgreSQL: a startup parameter;
// MySQL: SET SESSION TRANSACTION READ ONLY), and the manual says to use an
// account that can only read.

const maxQueryLen = 2000

var intoWord = regexp.MustCompile(`(?i)\binto\b`)

// validateQuery checks the statement without running it.
func validateQuery(q string) error {
	q = strings.TrimSpace(q)
	if q == "" {
		return errors.New("query is empty")
	}
	if len(q) > maxQueryLen {
		return fmt.Errorf("query is longer than %d characters", maxQueryLen)
	}
	if strings.ContainsRune(q, 0) {
		return errors.New("query holds a NUL character")
	}
	body := strings.TrimSpace(strings.TrimSuffix(q, ";"))
	if strings.Contains(body, ";") {
		return errors.New("query must be a single statement (no ; inside it)")
	}
	f := strings.Fields(body)
	if len(f) < 2 || !strings.EqualFold(f[0], "select") {
		return errors.New("query must be a SELECT")
	}
	if intoWord.MatchString(body) {
		return errors.New("query must not use INTO")
	}
	return nil
}

// statement is the query as it is sent: without a closing ;.
func statement(q string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(q), ";"))
}

// queryNumber turns the first column of the first row into the check's
// value; null, text and no rows are failures that say so.
func queryNumber(r Result, o opts, text string, null bool, rows int) Result {
	q := statement(o.str("query", ""))
	short := q
	if len(short) > 60 {
		short = short[:60] + "…"
	}
	switch {
	case rows == 0:
		return failKeep(r, ReasonAssertFailed, "the query returned no rows: %s", short)
	case null:
		return failKeep(r, ReasonAssertFailed, "the query returned NULL: %s", short)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return failKeep(r, ReasonAssertFailed, "the query returned %q, not a number: %s", oneLine(text), short)
	}
	if r.Metrics == nil {
		r.Metrics = map[string]float64{}
	}
	r.Metrics["value"] = v
	r.Detail += fmt.Sprintf(": %s = %s", short, strconv.FormatFloat(v, 'f', -1, 64))
	return r
}
