package session

import (
	"context"
	"os/exec"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
)

// doctorPullProbe uses the caller's active Podman session and checks the
// registry even when the image is cached. It neither runs nor removes images.
func doctorPullProbe(ctx context.Context, image string) bool {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "podman", "pull", "--quiet", "--policy=always", image)
	// Nil output streams discard subprocess output, including registry errors
	// that might contain credentials or other sensitive information.
	return cmd.Run() == nil
}

func reportDoctorPull(ctx context.Context, c profile.Profile, ready bool, report func(string, string, string)) {
	switch {
	case c.SkipPullProbe:
		report("NOT VERIFIED", "ACR image pull", "diagnostic pull skipped with --no-pull")
	case c.ACRProbeImage == "":
		report("NOT VERIFIED", "ACR image pull", "no diagnostic image configured; set acr_probe_image to a digest-pinned image in this registry")
	case !ready:
		report("NOT VERIFIED", "ACR image pull", "requires an enabled ACR session with matching Podman settings")
	default:
		if doctorPullProbe(ctx, c.ACRProbeImage) {
			report("OK", "ACR image pull", "diagnostic image pulled through the session engine; proves access to its repository only (cached layers may be reused)")
		} else {
			report("NOT VERIFIED", "ACR image pull", "diagnostic pull failed or timed out; check registry login, access to the diagnostic repository, and network connectivity")
		}
	}
}
