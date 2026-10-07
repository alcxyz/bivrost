package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode"

	"github.com/alcxyz/bivrost/internal/azure"
)

func TestWriteSubscriptionsRendersSafeTable(t *testing.T) {
	t.Parallel()
	subscriptions := []azure.Subscription{
		{ID: "11111111-1111-4111-8111-111111111111", Name: "Example\x1b[31m\nDevelopment", TenantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\u202e", State: "Enabled\r", IsDefault: true},
		{ID: "22222222-2222-4222-8222-222222222222", Name: "Example Production", TenantID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", State: "Warned"},
	}
	var output bytes.Buffer
	if err := WriteSubscriptions(&output, subscriptions); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"DEFAULT", "SUBSCRIPTION ID", "Example?[31m?Development", "yes", "Warned"} {
		if !strings.Contains(text, want) {
			t.Errorf("table does not contain %q:\n%s", want, text)
		}
	}
	for _, r := range strings.TrimSuffix(text, "\n") {
		if r != '\n' && unicode.In(r, unicode.Cc, unicode.Cf) {
			t.Fatalf("table contains terminal control character %U: %q", r, text)
		}
	}
}

func TestWriteSubscriptionsReturnsWriterError(t *testing.T) {
	t.Parallel()
	err := WriteSubscriptions(errorWriter{}, []azure.Subscription{{Name: "Example"}})
	if err == nil {
		t.Fatal("WriteSubscriptions() ignored writer error")
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
