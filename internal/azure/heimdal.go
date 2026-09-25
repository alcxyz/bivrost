package azure

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	heimdalUploadTimeout  = 60 * time.Second
	heimdalServiceTimeout = "30"
)

type heimdalPublicationStage uint8

const (
	heimdalRevisionStage heimdalPublicationStage = iota
	heimdalPointerStage
)

type heimdalPublicationFailure uint8

const (
	heimdalPublicationFailureUnknown heimdalPublicationFailure = iota
	heimdalPublicationFailureCLIUnavailable
	heimdalPublicationFailureTimeout
	heimdalPublicationFailureCanceled
)

type heimdalPublicationError struct {
	stage   heimdalPublicationStage
	failure heimdalPublicationFailure
	cause   error
}

func (e *heimdalPublicationError) Error() string {
	return HeimdalPublicationGuidance(e)
}

func (e *heimdalPublicationError) Unwrap() error {
	return e.cause
}

var (
	heimdalPrefixSegmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	heimdalRevisionPattern      = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// HeimdalBlobUploadArguments returns a create-only block blob upload using the
// signed-in Azure CLI identity. If-None-Match is a service-side condition; the
// Azure CLI passes it to the block blob upload request and returns an error when
// the named blob already exists. Explicitly disabling overwrite is an
// additional guard against CLI default changes.
func HeimdalBlobUploadArguments(target TerraformBackend, endpoint, blobName, file string) []string {
	return []string{
		"storage", "blob", "upload",
		"--subscription", target.Subscription,
		"--account-name", target.Account,
		"--container-name", target.Container,
		"--name", blobName,
		"--file", file,
		"--blob-endpoint", endpoint,
		"--auth-mode", "login",
		"--type", "block",
		"--overwrite", "false",
		"--if-none-match", "*",
		"--content-type", "application/json",
		"--no-progress",
		"--timeout", heimdalServiceTimeout,
		"--output", "none",
		"--only-show-errors",
	}
}

// PublishInitialHeimdal creates one immutable revision and then its current
// pointer in an existing container. It never provisions Azure resources,
// changes roles, overwrites blobs, or deletes a revision after partial failure.
func PublishInitialHeimdal(ctx context.Context, target TerraformBackend, endpoint, prefix, revision, revisionFile, pointerFile string, environment []string) error {
	if err := ValidateTerraformBackend(target); err != nil {
		return err
	}
	if !validTerraformBlobEndpoint(target.Account, endpoint) {
		return errors.New("invalid Azure Blob endpoint")
	}
	if !validHeimdalPrefix(prefix) {
		return errors.New("invalid Heimdal Blob prefix")
	}
	if !heimdalRevisionPattern.MatchString(revision) {
		return errors.New("invalid Heimdal revision")
	}
	if !regularFile(revisionFile) {
		return errors.New("Heimdal revision source is not a regular file")
	}
	if !regularFile(pointerFile) {
		return errors.New("Heimdal pointer source is not a regular file")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	revisionBlob := prefix + "/revisions/" + revision + ".json"
	if err := uploadHeimdalBlob(ctx, target, endpoint, revisionBlob, revisionFile, environment, heimdalRevisionStage); err != nil {
		return err
	}
	return uploadHeimdalBlob(ctx, target, endpoint, prefix+"/current.json", pointerFile, environment, heimdalPointerStage)
}

func validHeimdalPrefix(prefix string) bool {
	if prefix == "" || len(prefix) > 256 {
		return false
	}
	for _, segment := range strings.Split(prefix, "/") {
		if !heimdalPrefixSegmentPattern.MatchString(segment) {
			return false
		}
	}
	return true
}

func regularFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func uploadHeimdalBlob(ctx context.Context, target TerraformBackend, endpoint, blobName, file string, environment []string, stage heimdalPublicationStage) error {
	uploadCtx, cancel := context.WithTimeout(ctx, heimdalUploadTimeout)
	defer cancel()

	cmd, err := Command(uploadCtx, HeimdalBlobUploadArguments(target, endpoint, blobName, file)...)
	if err != nil {
		return &heimdalPublicationError{stage: stage, failure: heimdalPublicationFailureCLIUnavailable, cause: err}
	}
	cmd.Env = withoutStorageCredentials(environment)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.WaitDelay = subscriptionWaitDelay
	if err := cmd.Run(); err != nil {
		failure := heimdalPublicationFailureUnknown
		cause := err
		if ctx.Err() != nil {
			failure = heimdalPublicationFailureCanceled
			cause = ctx.Err()
		} else if errors.Is(uploadCtx.Err(), context.DeadlineExceeded) {
			failure = heimdalPublicationFailureTimeout
			cause = uploadCtx.Err()
		} else {
			var executableError *exec.Error
			if errors.As(err, &executableError) {
				failure = heimdalPublicationFailureCLIUnavailable
			}
		}
		return &heimdalPublicationError{stage: stage, failure: failure, cause: cause}
	}
	return nil
}

// HeimdalRevisionOrphaned reports whether the immutable revision upload
// succeeded but publication of the current pointer failed. The revision is
// intentionally left in place because deleting it could race another writer.
func HeimdalRevisionOrphaned(err error) bool {
	var publicationError *heimdalPublicationError
	return errors.As(err, &publicationError) && publicationError.stage == heimdalPointerStage
}

// HeimdalPublicationGuidance returns fixed text that never includes Azure CLI
// output, local file contents, or arbitrary error text.
func HeimdalPublicationGuidance(err error) string {
	var publicationError *heimdalPublicationError
	if !errors.As(err, &publicationError) {
		return "Heimdal publication failed; check the target names and local source files"
	}

	reason := "the Azure Blob create request failed; check Azure login, Blob data access, connectivity, and whether the destination blob already exists"
	switch publicationError.failure {
	case heimdalPublicationFailureCLIUnavailable:
		reason = "Azure CLI is unavailable; install it, then retry"
	case heimdalPublicationFailureTimeout:
		reason = "the Azure Blob create request timed out; check connectivity and retry"
	case heimdalPublicationFailureCanceled:
		reason = "Heimdal publication was canceled"
	}
	if publicationError.stage == heimdalPointerStage {
		return reason + "; the immutable revision was created but current pointer creation was not confirmed, so the revision may remain unreferenced and no cleanup was attempted"
	}
	return reason + "; no current pointer was attempted, and an unconfirmed immutable revision may remain unreferenced"
}
