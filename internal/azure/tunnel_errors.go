package azure

import (
	"bytes"
	"errors"
	"fmt"
)

// tunnelErrorSampleBytes bounds the Bastion tunnel stderr kept for
// classification. Azure CLI reports a failed tunnel well within this size.
const tunnelErrorSampleBytes = 64 << 10

// TunnelErrors keeps a bounded in-memory sample of Bastion tunnel stderr. The
// sample is used only to classify a failure and is never displayed or logged.
type TunnelErrors struct {
	sample boundedBuffer
}

// NewTunnelErrors returns a writer for a Bastion tunnel's stderr.
func NewTunnelErrors() *TunnelErrors {
	return &TunnelErrors{sample: boundedBuffer{limit: tunnelErrorSampleBytes}}
}

func (t *TunnelErrors) Write(p []byte) (int, error) {
	return t.sample.Write(p)
}

type tunnelFailure struct {
	markers  []string
	guidance string
}

// Ordered from most to least specific; the first match wins. Codes are Azure
// Resource Manager error codes, Microsoft Entra AADSTS codes or Azure CLI
// messages from the Bastion extension.
var tunnelFailures = []tunnelFailure{
	{[]string{"AuthorizationFailed"}, "Azure denied access to this environment's Bastion host or VM (AuthorizationFailed). Check that your PIM activation or group membership covers it; after activating, wait a few minutes and run bivrost login before connecting again"},
	{[]string{"SubscriptionNotFound", "InvalidSubscriptionId"}, "this environment's Azure subscription is not available to your account; check the environment configuration and that you signed in to the right tenant"},
	{[]string{"ResourceGroupNotFound", "ResourceNotFound", "ParentResourceNotFound"}, "Azure could not find this environment's Bastion host or VM; check the environment configuration, or ask its maintainers whether it moved"},
	{[]string{"Native Client must be enabled"}, "this environment's Bastion host does not allow native client tunnels; its maintainers must enable native client support on a Standard or Premium SKU"},
	{[]string{"AADSTS", "az login", "InvalidAuthenticationToken", "ExpiredAuthenticationToken"}, "the Azure CLI sign-in has expired or must be renewed; run bivrost login and connect again"},
	{[]string{"Failed to establish a new connection", "Max retries exceeded", "ConnectionError", "Name or service not known", "nodename nor servname", "getaddrinfo failed"}, "the Bastion tunnel could not reach Azure; check network connectivity and proxy settings, then retry"},
	{[]string{"Defined port is currently unavailable"}, "the local port chosen for the Bastion tunnel was taken by another program; connect again"},
}

// Err explains why a Bastion tunnel exited before it became ready, from the
// recognised errors in its stderr sample.
func (t *TunnelErrors) Err() error {
	data := t.sample.data.Bytes()
	for _, failure := range tunnelFailures {
		for _, marker := range failure.markers {
			if bytes.Contains(data, []byte(marker)) {
				return fmt.Errorf("Azure Bastion tunnel failed: %s", failure.guidance)
			}
		}
	}
	return errors.New("Azure Bastion tunnel exited before it was ready; check your Azure access and PIM activation for this environment, then run bivrost doctor")
}
