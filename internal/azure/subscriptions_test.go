package azure

import (
	"os"
	"reflect"
	"testing"
)

func TestSubscriptionListArgumentsAreReadOnlyAndMinimallyProjected(t *testing.T) {
	t.Parallel()
	want := []string{
		"account", "list",
		"--query", "[].[id,name,tenantId,state,isDefault]",
		"--output", "json",
		"--only-show-errors",
	}
	if got := SubscriptionListArguments(false); !reflect.DeepEqual(got, want) {
		t.Fatalf("SubscriptionListArguments(false) = %#v, want %#v", got, want)
	}
	withRefresh := SubscriptionListArguments(true)
	if !reflect.DeepEqual(withRefresh, append(want, "--refresh")) {
		t.Fatalf("SubscriptionListArguments(true) = %#v", withRefresh)
	}
	for _, forbidden := range []string{"set", "login", "show", "get-access-token", "storage", "terraform"} {
		for _, arg := range withRefresh {
			if arg == forbidden {
				t.Fatalf("subscription discovery contains forbidden argument %q: %#v", forbidden, withRefresh)
			}
		}
	}
}

func TestDecodeSubscriptionsFixtureAndRejectMalformedOutput(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/subscriptions.json")
	if err != nil {
		t.Fatal(err)
	}
	subscriptions, err := decodeSubscriptions(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(subscriptions) != 2 || subscriptions[0].Name != "Example Development" || !subscriptions[0].IsDefault || subscriptions[1].State != "Warned" {
		t.Fatalf("decoded subscriptions = %+v", subscriptions)
	}

	for _, malformed := range []string{
		`not-json`,
		`[] trailing`,
		`[["id","name","tenant","Enabled"]]`,
		`[["id","name","tenant","Enabled","yes"]]`,
		`[["id","name",null,"Enabled",false]]`,
		`[["","name","tenant","Enabled",false]]`,
		`[{"id":"extra-shape"}]`,
	} {
		if _, err := decodeSubscriptions([]byte(malformed)); err == nil {
			t.Errorf("decodeSubscriptions(%q) succeeded", malformed)
		}
	}
}

func TestBoundedBufferCapsCapturedOutput(t *testing.T) {
	t.Parallel()
	buffer := boundedBuffer{limit: 4}
	if n, err := buffer.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if got := buffer.data.String(); got != "abcd" || !buffer.exceeded {
		t.Fatalf("bounded output = %q, exceeded=%v", got, buffer.exceeded)
	}
	if n, err := buffer.Write([]byte("more")); err != nil || n != 4 || buffer.data.Len() != 4 {
		t.Fatalf("second Write() = %d, %v; len=%d", n, err, buffer.data.Len())
	}
}
