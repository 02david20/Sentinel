# Installing Sentinel

Sentinel is one static binary (pure Go, no CGO, no runtime to install).

| | |
|---|---|
| Platforms | Linux and macOS, on amd64 (Intel/AMD) and arm64 (Apple Silicon, Graviton, Raspberry Pi 4+) |
| Not supported | Windows natively (use WSL). The run store relies on Unix file locks |
| Needed to run | nothing |
| Optional | `python3` (the default `sentinel block new` scaffold and the example blocks), `jq` (the `--lang sh` scaffold), a systemd user session (Linux, for `sentinel install` timers), an agent command for escalations |

> **Releases:** the commands below that download a release work once a release has been published
> (a maintainer pushes a version tag; see [RELEASING.md](RELEASING.md)). Until then, build from source.

## 1. Install script (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/02david20/Sentinel/main/scripts/install.sh | sh
```

It detects your OS and CPU, downloads the matching archive **and `checksums.txt`**, verifies the
SHA-256 before extracting anything, and installs to `~/.local/bin/sentinel`. It never uses `sudo`.

Read it first if you prefer (`curl -fsSL <url> | less`), and preview with `--dry-run`:

```bash
curl -fsSL https://raw.githubusercontent.com/02david20/Sentinel/main/scripts/install.sh | sh -s -- --dry-run
```

| Variable | Meaning | Default |
|---|---|---|
| `SENTINEL_VERSION` | a tag such as `v0.2.0` | the latest release |
| `SENTINEL_PREFIX` | installs to `$SENTINEL_PREFIX/bin` | `~/.local` |
| `SENTINEL_REPO` | `owner/name` on GitHub | `02david20/Sentinel` |
| `SENTINEL_BASE_URL` | folder with the archives and `checksums.txt` (mirrors, offline installs) | GitHub release URL |

```bash
# a specific version, system-wide
curl -fsSL .../install.sh | SENTINEL_VERSION=v0.2.0 SENTINEL_PREFIX=/usr/local sh   # may need write access to /usr/local
```

If `~/.local/bin` is not on your `PATH`, the script says so and prints the line to add to your shell profile.

## 2. Download manually and verify

From the [Releases page](https://github.com/02david20/Sentinel/releases), pick
`sentinel_<version>_<os>_<arch>.tar.gz` (`os` is `linux` or `darwin`, `arch` is `amd64` or `arm64`) and
`checksums.txt`.

```bash
sha256sum --check --ignore-missing checksums.txt          # Linux
shasum -a 256 -c checksums.txt 2>&1 | grep -v 'No such'    # macOS: look for "OK" on your archive
tar -xzf sentinel_0.2.0_linux_amd64.tar.gz ./sentinel
install -m 0755 sentinel ~/.local/bin/sentinel
```

The archive also holds `README.md`, `docs/`, `examples/` and `workflows/`. Release signatures
(cosign) are not published yet; the checksum protects against corruption and a tampered mirror, not
against a compromised release.

## 3. Linux packages (.deb / .rpm)

Each release also attaches `sentinel_<version>_linux_<arch>.deb` and `.rpm`:

```bash
sudo dpkg -i sentinel_0.2.0_linux_amd64.deb      # Debian, Ubuntu
sudo rpm -i  sentinel_0.2.0_linux_amd64.rpm      # Fedora, RHEL, openSUSE
```

The binary goes to `/usr/bin/sentinel`; docs to `/usr/share/doc/sentinel/`.

## 4. Homebrew (macOS and Linux): planned

A tap (`02david20/homebrew-tap`) has to be created first (steps in [RELEASING.md](RELEASING.md)).
Once it exists:

```bash
brew install 02david20/tap/sentinel
```

## 5. `go install`

Needs Go 1.24 or newer:

```bash
go install github.com/02david20/Sentinel/cmd/sentinel@latest
```

The binary lands in `$(go env GOPATH)/bin`. Builds made this way report `sentinel dev` from
`sentinel version` because the release stamp is only added by the release pipeline.

## 6. From source

```bash
git clone https://github.com/02david20/Sentinel.git
cd Sentinel
make build          # ./bin/sentinel, stamped with the git version
make install        # copies it to ~/.local/bin   (or: go build -o ~/.local/bin/sentinel ./cmd/sentinel)
make test           # vet + tests with the race detector
```

## After installing

```bash
sentinel version            # sentinel 0.2.0 (commit abc1234, built 2026-...)
sentinel doctor             # state and blocks folders; webhook, agent command, systemd are optional
sentinel run --dry-run workflows/example.yaml     # from a cloned repo or the archive's workflows/ folder
sentinel                    # open the TUI
```

`sentinel doctor` exits 0 when only optional items are missing. Optional items matter only for the
feature named next to them.

### Where things live

| What | Where | Override |
|---|---|---|
| Run history, snapshots, escalation state | `~/.local/state/sentinel/sentinel.db` (`$XDG_STATE_HOME/sentinel/`) | `SENTINEL_STATE_DIR` |
| Plugin blocks and their approvals | `<state dir>/blocks.d/` | `SENTINEL_BLOCKS_DIR`, `SENTINEL_APPROVALS_FILE` |
| Webhook URL (for `Webhook.Post`) | `~/.config/sentinel/webhook` | `SENTINEL_WEBHOOK_URL` |
| Agent started by escalations | not set | `SENTINEL_AGENT_CMD` |

### Running on a schedule

- **Linux (systemd):** `sentinel install workflows/my-watch.yaml` writes a user timer and enables it.
  User timers stop when you log out unless lingering is on: run `loginctl enable-linger $USER` on
  servers and VPSes. After moving or upgrading the binary to a different path, run
  `sentinel install <workflow>` again so the unit points at the new path. Check with `sentinel status`.
- **macOS:** there is no built-in scheduler yet (a `sentinel daemon` is planned in the MVP proposal).
  Use cron or launchd to call `sentinel run`, for example a crontab line:

  ```cron
  */15 * * * * SENTINEL_STATE_DIR=$HOME/.local/state/sentinel $HOME/.local/bin/sentinel run $HOME/watch/my-watch.yaml
  ```

  Scheduled runs do not inherit your shell: set `SENTINEL_WEBHOOK_URL`, `SENTINEL_AGENT_CMD` and any
  secrets a plugin block lists in `env:` in the crontab or the plist.

## Upgrading

Run the install script again (it overwrites the binary), or `brew upgrade`, or install the new
package. The database upgrades itself on first use. Re-run `sentinel install <workflow>` for scheduled
workflows if the binary path changed.

## Uninstalling

```bash
sentinel uninstall <workflow>       # for each scheduled workflow (Linux): stops and removes its timer
rm ~/.local/bin/sentinel            # or: brew uninstall sentinel / dpkg -r sentinel / rpm -e sentinel
rm -rf ~/.local/state/sentinel      # run history and approved blocks. Optional: this deletes your data
rm -rf ~/.config/sentinel           # webhook URL. Optional
```

## Troubleshooting

| Symptom | Fix |
|---|---|
| `sentinel: command not found` | the install folder is not on `PATH`: `export PATH="$HOME/.local/bin:$PATH"` and add it to your shell profile |
| macOS: "cannot be opened because the developer cannot be verified" | the binary is not code-signed or notarized. Installing with the script avoids the quarantine flag. For a browser download: `xattr -d com.apple.quarantine ~/.local/bin/sentinel` |
| `checksum mismatch ... not installing` | the download was corrupted or altered. Retry; if it persists, do not install and report it |
| `could not find a release of ...` | no release has been published yet: build from source (section 6) |
| `sentinel install` fails or `doctor` says systemd is unavailable | systemd user services are Linux-only. On macOS use cron or launchd (above) |
| Scheduled runs stop after logout (Linux) | `loginctl enable-linger $USER` |
| Every command pauses about 5 seconds in some terminals (ssh, tmux, scripted ttys) | a library queries the terminal's background colour at start-up and waits for an answer. Use a terminal that answers, or set `TERM=screen-256color` for that session |
| A workflow reports `unknown block ...` | the block is a plugin that is pending or modified: `sentinel block list`, then review and approve it ([BLOCKS.md](BLOCKS.md)) |
