# Releasing Sentinel (maintainer guide)

A release is a git tag. Pushing a tag like `v0.2.0` runs `.github/workflows/release.yml`, which runs
the tests and then GoReleaser to publish, on the GitHub release page:

- `sentinel_<version>_<os>_<arch>.tar.gz` for linux and darwin on amd64 and arm64
- `sentinel_<version>_linux_<arch>.deb` and `.rpm`
- `checksums.txt` (SHA-256 of every file)
- a changelog generated from commit messages

`scripts/install.sh` and the installation guide ([INSTALL.md](INSTALL.md)) depend on those exact
archive names. Do not rename them without changing both.

## What is where

| File | Role |
|---|---|
| `.goreleaser.yaml` | what to build, archive, package and publish |
| `.github/workflows/ci.yml` | on every push and PR: vet, tests with `-race` (Linux and macOS), all four targets must compile |
| `.github/workflows/release.yml` | on a `v*` tag: tests, then GoReleaser |
| `scripts/install.sh` | the curl-able installer (checks the checksum before extracting) |
| `scripts/build-dist.sh`, `make dist` | builds the same archives and `checksums.txt` locally, without GoReleaser |
| `Makefile` | `make build`, `test`, `install`, `dist` |
| `cmd/sentinel/main.go` | `version`, `commit`, `date` variables, stamped with `-X main.version=...` |

## One-time setup

1. **Add a license.** There is no `LICENSE` file yet, and without one nobody may legally reuse or
   redistribute the code. Choose one (MIT or Apache-2.0 are typical), add `LICENSE`, and add
   `"LICENSE*"` to `archives.files` in `.goreleaser.yaml` and a `license:` line to the `nfpms` block.
2. **Repository settings:** Actions must be enabled; the release job needs `contents: write`
   (already declared in the workflow). Use a protected `main` and require the `ci` checks.
3. **Validate the config once** with GoReleaser installed (`brew install goreleaser`, or the
   [official install options](https://goreleaser.com/install/)): `goreleaser check`. The files in this
   repo were syntax-checked as YAML but **not** run through GoReleaser, so treat the first
   `--snapshot` run below as the real test.
4. **Optional, Homebrew tap:**
   1. Create a public repo `02david20/homebrew-tap`.
   2. Create a fine-grained token with *Contents: read and write* on that repo only, and save it in
      this repo as the secret `HOMEBREW_TAP_GITHUB_TOKEN`.
   3. Uncomment the `brews:` block in `.goreleaser.yaml` and the `HOMEBREW_TAP_GITHUB_TOKEN` line in
      `release.yml`. Releases then update the formula, and `brew install 02david20/tap/sentinel` works.

## Cutting a release

Versioning: [semver](https://semver.org). While the version starts with `0.`, minor releases may
break things (say so in the notes). A tag with a suffix such as `v0.3.0-rc.1` is published as a
pre-release automatically.

```bash
# 1. be on a clean, up-to-date main with green CI
git checkout main && git pull --ff-only && git status --short        # no output

# 2. dry run: build everything locally, nothing is published
goreleaser release --snapshot --clean         # artifacts in ./dist
#    or, without GoReleaser:
make dist VERSION=0.2.0-rc0

# 3. test the artifact you would ship
SENTINEL_BASE_URL="file://$PWD/dist" SENTINEL_VERSION=v0.2.0-rc0 SENTINEL_PREFIX=/tmp/try sh scripts/install.sh
/tmp/try/bin/sentinel version && /tmp/try/bin/sentinel doctor

# 4. tag and push: this publishes
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

Then watch the **release** workflow run, and check the release page.

### Verify the published release

```bash
curl -fsSL https://raw.githubusercontent.com/02david20/Sentinel/main/scripts/install.sh | SENTINEL_PREFIX=/tmp/verify sh
/tmp/verify/bin/sentinel version          # must print the tag, not "dev"
```

and on a second OS or CPU if you can (`darwin/arm64` and `linux/amd64` at minimum). Then edit the
release notes to lead with what changed for users.

### Checklist

- [ ] CI green on `main`; `go test ./... -race` passes
- [ ] `docs/` and README match the behaviour (commands, environment variables, exit codes)
- [ ] A snapshot build installs and `sentinel doctor` runs
- [ ] The tag matches `vMAJOR.MINOR.PATCH`
- [ ] The release has 4 archives, 2 packages (per arch), `checksums.txt`
- [ ] `install.sh` installs it and `sentinel version` shows the tag

## If a release is bad

- **Do not move or delete a published tag that people may have downloaded.** Publish a fixed
  `v0.2.1` instead and mark the bad release as a pre-release or add a warning to its notes.
- If the workflow failed midway, fix the cause, delete the **draft or partial** release and its tag
  (only if nothing was announced), and push the tag again.
- To pull a release from the installer's reach, delete its GitHub release; `releases/latest` then
  falls back to the previous one.

## Hardening to add later

These are not in place today, and the install guide says so:

- **Signing:** sign `checksums.txt` with cosign (keyless, via GitHub's OIDC) and publish the
  `.sig`/`.pem`; add verification to `install.sh`. Add GitHub build-provenance attestations.
- **macOS:** codesign and notarize the darwin binaries (an Apple Developer ID is required);
  until then users meet Gatekeeper warnings for browser downloads.
- **SBOM:** GoReleaser can attach one (`sboms:`).
- **Windows:** unsupported until the store's file locking has a Windows implementation.
- **Docker image / winget / Scoop / AUR:** only when someone asks for them.

## Renaming the product

"Sentinel" collides with other products (HashiCorp, Microsoft, Redis). If it is renamed, change
together: the module path in `go.mod` and every import, the binary name and `project_name`, the
archive prefix in `.goreleaser.yaml`, `install.sh` and `build-dist.sh`, the environment variable
prefix `SENTINEL_`, the systemd unit prefix, the Homebrew formula, the docs and the demo recordings
(`tools/demo/run.sh`). Do it before the first public release, not after.
