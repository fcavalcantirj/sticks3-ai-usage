package format

import (
	"fmt"
	"io"
	"strconv"
	"time"
	"unicode/utf8"

	"usaged/internal/snapshot"
)

// RenderTable writes the snapshot as the human-readable usage table shared by
// `usaged once` and the /v1/usage.txt endpoint. One line per row using
// "%-*s %-*s %*s  %-*s %s" (provider label, row label, pct+"%" or "--",
// reset text, provider status). A null pct prints "--" right-justified to
// the same column width as the widest percentage value, so the RESETS column
// starts at a constant position for every row.
//
// Column widths are sized from the data (ORDER #17): a fixed 10-char provider
// column overflows on "OpenRouter fallback" (19 chars) and misaligns every
// subsequent column. Each width is the max of the header text and all values.
//
// The header line is built from the computed widths so it always lines up with
// the rows. Preceded by the header and followed by a footer:
// "rev=<rev> seq=<n> as of <HH:MM>".
func RenderTable(snap snapshot.Snapshot, w io.Writer, loc *time.Location) {
	// Compute column widths from the data.
	providerW := len("PROVIDER")
	rowW := len("ROW")
	usedW := len("USED")
	resetsW := len("RESETS")

	for _, p := range snap.Providers {
		if l := utf8.RuneCountInString(p.Label); l > providerW {
			providerW = l
		}
		for _, r := range p.Rows {
			if l := utf8.RuneCountInString(r.Label); l > rowW {
				rowW = l
			}
			if l := len(pctString(r.Pct)); l > usedW {
				usedW = l
			}
			if l := utf8.RuneCountInString(r.Txt); l > resetsW {
				resetsW = l
			}
		}
	}

	fmt.Fprintf(w, "%-*s %-*s %*s  %-*s %s\n",
		providerW, "PROVIDER", rowW, "ROW", usedW, "USED", resetsW, "RESETS", "STATUS")

	for _, p := range snap.Providers {
		for _, r := range p.Rows {
			fmt.Fprintf(w, "%-*s %-*s %*s  %-*s %s\n",
				providerW, p.Label, rowW, r.Label, usedW, pctString(r.Pct), resetsW, r.Txt, p.Status)
		}
	}

	asOf := time.Unix(snap.CheckedAt, 0).In(loc).Format("15:04")
	fmt.Fprintf(w, "rev=%s seq=%d as of %s\n", snap.Rev, snap.Seq, asOf)
}

// pctString returns the percentage as "NN%" — or "--" when pct is nil (null).
func pctString(pct *int) string {
	if pct == nil {
		return "--"
	}
	return strconv.Itoa(*pct) + "%"
}
