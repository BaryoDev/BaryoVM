# Contributing to @@REPO@@

Open an issue before a large change, so the direction is agreed before the work is done.

- Keep a pull request to one change.
- A bug fix comes with a test that failed before the fix.
- CI must pass. Merging needs a review from the code owners.

## Releases

A release is a tag `vX.Y.Z` pushed to `main`. The publish workflow builds, tests and publishes
through trusted publishing, so no registry token is stored in this repository. Run the publish
workflow by hand with `dry_run` ticked to check that trusted publishing is still set up.
