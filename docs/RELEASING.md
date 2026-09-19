# Releasing

Releases are automatic. Merging a PR to `main` builds and publishes a new
versioned release; the version bump comes from the PR's label.

## How a release happens

1. A PR is merged to `main`.
2. `.github/workflows/release.yml` reads the PR's label:
   - `major` → (x+1).0.0
   - `minor` → x.(y+1).0
   - `patch` (or no label) → x.y.(z+1)
3. It creates and pushes the next `vX.Y.Z` tag.
4. GoReleaser builds darwin/linux amd64/arm64 binaries, publishes a GitHub
   Release with archives and `checksums.txt`, and updates the Homebrew tap.

Label a PR `major`, `minor`, or `patch` before merging. Unlabeled merges
are treated as a patch.

## One-time setup (required before the first release)

1. Create the labels `major`, `minor`, `patch` in this repo.
2. Create a public repo `ctrl-research/homebrew-tap`.
3. Create a token (a fine-grained PAT or classic token with `contents:write`
   on `homebrew-tap`) and add it to this repo's Actions secrets as
   `HOMEBREW_TAP_TOKEN`. Without it the binary release still publishes; only
   the brew formula update is skipped.

## Testing the config locally

```sh
goreleaser check                     # validate .goreleaser.yaml
goreleaser build --snapshot --clean  # build without publishing
```

## Install paths for users

- Homebrew: `brew install ctrl-research/tap/seaglass`
- Script: `curl -fsSL https://raw.githubusercontent.com/ctrl-research/seaglass/main/install.sh | sh`
