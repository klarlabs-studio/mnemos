package relate

import (
	"bufio"
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

// Writes the ids of `contradicts` edges the CURRENT detectors would no longer
// produce. Read-only on the brain; the caller does the deleting.
func TestPruneList(t *testing.T) {
	path := os.Getenv("MNEMOS_EVAL_DB")
	out := os.Getenv("MNEMOS_PRUNE_OUT")
	if path == "" || out == "" {
		t.Skip("set MNEMOS_EVAL_DB and MNEMOS_PRUNE_OUT")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`
		select r.id, a.text, b.text from relationships r
		join claims a on a.id = r.from_claim_id
		join claims b on b.id = r.to_claim_id
		where r.type = 'contradicts'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	w := bufio.NewWriter(f)
	defer func() { _ = w.Flush() }()

	var total, prune int
	for rows.Next() {
		var id, a, b string
		if err := rows.Scan(&id, &a, &b); err != nil {
			t.Fatal(err)
		}
		total++
		aTok, aNeg := contentTokensAndPolarity(a)
		bTok, bNeg := contentTokensAndPolarity(b)
		bothEnum := isEnumeratedItem(a) && isEnumeratedItem(b)

		hit := false
		if rel, ok := inferRelationshipWithContext(aTok, aNeg, bTok, bNeg, bothEnum); ok && string(rel) == "contradicts" {
			hit = true
		}
		if !bothEnum && detectNumericDivergence(a, b, aTok, bTok) {
			hit = true
		}
		if !bothEnum && detectEntityRoleDivergence(a, b, aTok, bTok) {
			hit = true
		}
		if detectTemporalDivergence(a, b, aTok, bTok) {
			hit = true
		}
		if !hit {
			prune++
			_, _ = w.WriteString(id + "\n")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("total=%d prune=%d keep=%d", total, prune, total-prune)
}
