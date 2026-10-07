package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
)

const (
	subscriptionDiscoveryTimeout = 30 * time.Second
	subscriptionWaitDelay        = 2 * time.Second
	maxSubscriptionOutput        = 1 << 20
)

// Subscription is the non-secret account metadata shown by subscription discovery.
type Subscription struct {
	ID        string
	Name      string
	TenantID  string
	State     string
	IsDefault bool
}

// SubscriptionListArguments returns the read-only Azure CLI discovery arguments.
func SubscriptionListArguments(refresh bool) []string {
	args := []string{
		"account", "list",
		"--query", "[].[id,name,tenantId,state,isDefault]",
		"--output", "json",
		"--only-show-errors",
	}
	if refresh {
		args = append(args, "--refresh")
	}
	return args
}

// DiscoverSubscriptions returns subscriptions visible to the current Azure CLI login.
func DiscoverSubscriptions(ctx context.Context, refresh bool) ([]Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, subscriptionDiscoveryTimeout)
	defer cancel()

	cmd, err := Command(discoveryCtx, SubscriptionListArguments(refresh)...)
	if err != nil {
		return nil, errors.New("cannot run Azure CLI; install it, then run bivrost login or az login and retry")
	}
	var output boundedBuffer
	output.limit = maxSubscriptionOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = subscriptionWaitDelay
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(discoveryCtx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("Azure subscription discovery timed out; check connectivity and retry")
		}
		var executableError *exec.Error
		if errors.As(err, &executableError) {
			return nil, errors.New("cannot run Azure CLI; install it, then run bivrost login or az login and retry")
		}
		return nil, errors.New("Azure subscription discovery failed; run bivrost login or az login, check your access and connectivity, then retry")
	}
	if output.exceeded {
		return nil, errors.New("Azure CLI returned too much subscription data; narrow the signed-in account or retry without --refresh")
	}

	subscriptions, err := decodeSubscriptions(output.data.Bytes())
	if err != nil {
		return nil, errors.New("Azure CLI returned an invalid subscription list; update Azure CLI and retry")
	}
	if len(subscriptions) == 0 {
		return nil, errors.New("no Azure subscriptions are visible; run bivrost login or az login, check your access, and retry with --refresh")
	}
	return subscriptions, nil
}

func decodeSubscriptions(data []byte) ([]Subscription, error) {
	var projected [][]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&projected); err != nil {
		return nil, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, err
	}

	subscriptions := make([]Subscription, 0, len(projected))
	for _, fields := range projected {
		if len(fields) != 5 {
			return nil, errors.New("unexpected subscription field count")
		}
		for _, field := range fields {
			if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
				return nil, errors.New("null subscription field")
			}
		}
		var subscription Subscription
		if json.Unmarshal(fields[0], &subscription.ID) != nil ||
			json.Unmarshal(fields[1], &subscription.Name) != nil ||
			json.Unmarshal(fields[2], &subscription.TenantID) != nil ||
			json.Unmarshal(fields[3], &subscription.State) != nil ||
			json.Unmarshal(fields[4], &subscription.IsDefault) != nil {
			return nil, errors.New("invalid subscription field")
		}
		if subscription.ID == "" || subscription.Name == "" || subscription.TenantID == "" || subscription.State == "" {
			return nil, errors.New("empty subscription field")
		}
		subscriptions = append(subscriptions, subscription)
	}
	return subscriptions, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

type boundedBuffer struct {
	data     bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.data.Len()
	if remaining <= 0 {
		b.exceeded = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.data.Write(p[:remaining])
		b.exceeded = true
		return len(p), nil
	}
	return b.data.Write(p)
}
