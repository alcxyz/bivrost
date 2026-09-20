# ADR 0008: Tesseract encrypted local configuration

- Status: Accepted future direction
- Date: 2026-09-20
- Scope: Optional follow-up to Heimdal; no implementation is claimed here.

## Context

Users may need deliberately provisioned local connection metadata when a remote
source is unavailable. Encryption can protect that configuration at rest without
making every session persist the metadata it downloads.

## Decision

Tesseract names an optional encrypted local configuration component of Bivrost.
It is explicitly provisioned by the user or deployment, not populated by an
automatic Heimdal response cache. Existing local configuration remains supported;
encryption is not a prerequisite for ordinary Bivrost use.

Use an established encryption format and implementation, with age the initial
candidate. Do not invent encryption based on SSH signing or assume that access
to an SSH agent provides decryption. The supported identity mechanism, key
storage, rotation, recovery and hardware-backed key behavior must be settled
before implementation, including Linux, macOS and Windows support.

Decrypt only when needed and apply the same profile validation as other local
configuration. Plaintext stays in memory where possible; subprocess files must
have restricted permissions and session cleanup. Never place credentials,
private keys or tokens in the configuration payload or diagnostic output.
Persistent encrypted configuration belongs under the user configuration location,
respecting XDG and the existing native-platform fallback conventions.

Fallback must be explicitly configured and report its source and the remote
failure. Missing keys, failed decryption and invalid data must produce actionable
errors without leaking payloads. Local data cannot grant access, enforce client
versions, or hide remote authorization failures. It may be stale; its use must
not silently retarget an active session. No guarantee of remote deletion or
revocation is made for copies already held by a user.

## Consequences

Heimdal can ship first using the existing local fallback. Tesseract adds optional
at-rest protection with key management costs. It does not change ADR 0005's
no-persistent-cache default or replace the user's Azure identity.

## Alternatives considered

- Existing unencrypted local profiles remain the simpler supported baseline.
- Automatically encrypting every downloaded catalogue was rejected because it
  introduces persistent stale state and changes the session-only contract.
- A custom SSH-based encryption scheme was rejected in favor of established
  implementations with explicit key support.
- A separate configuration server is unnecessary for this local capability.
