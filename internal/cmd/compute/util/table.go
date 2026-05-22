package util

import (
	"io"
	"text/tabwriter"
)

// NewTabWriter returns a *tabwriter.Writer configured for command table output.
// Use tab ('\t') as the column separator in rows. Caller must call Flush().
func NewTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
}
