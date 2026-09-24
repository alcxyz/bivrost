package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/alcxyz/bivrost/internal/azure"
)

// WriteSubscriptions renders non-secret Azure subscription metadata as a table.
func WriteSubscriptions(w io.Writer, subscriptions []azure.Subscription) error {
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "DEFAULT\tNAME\tSUBSCRIPTION ID\tTENANT ID\tSTATE"); err != nil {
		return err
	}
	for _, subscription := range subscriptions {
		isDefault := ""
		if subscription.IsDefault {
			isDefault = "yes"
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n",
			isDefault,
			sanitizeTerminalText(subscription.Name),
			sanitizeTerminalText(subscription.ID),
			sanitizeTerminalText(subscription.TenantID),
			sanitizeTerminalText(subscription.State),
		); err != nil {
			return err
		}
	}
	return table.Flush()
}

func sanitizeTerminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Cc, unicode.Cf) {
			return '?'
		}
		return r
	}, value)
}
