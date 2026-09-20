# Draft issue: publish reproducible releases

Create a release pipeline for versioned operating-system binaries and
verifiable checksums.

## Acceptance criteria

- Builds record the source revision, target operating systems, and build inputs.
- Release artifacts are versioned and accompanied by checksums and a documented
  verification command.
- GitHub repository `alcxyz/bivrost` is the canonical publication location;
  the Forgejo copy mirrors it.
- Release and source metadata identify the MIT License and copyright holder.
- Optional signing is documented as a future extension and is not required for
  the first pipeline.
