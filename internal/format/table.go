package format

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"usaged/internal/snapshot"
)

// TableHeader is the column header printed above the per-row usage table.
const TableHeader = "PROVIDER   ROW        USED   RESETS   STATUS"

// RenderTable writes the snapshot as the human-readable usage table shared by
// `usaged once` and the /v1/usage.txt endpoint. One line per row using
// "%-10s %-10s %4s%s  %-8s %s" (provider label, row label, pct or "--",
// optional percent suffix, reset text, provider status). A null pct prints
// "--" padded to the same width with no percent sign. Preceded by TableHeader
// and followed by a footer: "rev=<rev> seq=<n> as of <HH:MM>".
func RenderTable(snap snapshot.Snapshot, w io.Writer, loc *time.Location) {
	fmt.Fprintln(w, TableHeader)
	for _, p := range snap.Providers {
		for _, r := range p.Rows {
			pct := "--"
			suffix := ""
			if r.Pct != nil {
				pct = strconv.Itoa(*r.Pct)
				suffix = "%"
			}
			fmt.Fprintf(w, "%-10s %-10s %4s%s  %-8s %s\n", p.Label, r.Label, pct, suffix, r.Txt, p.Status)
		}
	}
	asOf := time.Unix(snap.CheckedAt, 0).In(loc).Format("15:04")
	fmt.Fprintf(w, "rev=%s seq=%d as of %s\n", snap.Rev, snap.Seq, asOf)
}
