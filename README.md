# drop

Send a huge file straight from one computer to another, through a web browser. No cloud service, no account, no size limit beyond your disk.

It's for when the file is too big for cloud services and the two of you are too far apart to hand over a drive. Think a 200 GB game backup, a video project, or a disk image. The person receiving runs one small program. The person sending needs only a browser.

**What a real transfer looked like:** a 182 GB zip, between two US states several hundred miles apart, at about 22 MB/s on average (peaks near 30 MB/s). It took about 2 hours 20 minutes, plus 19 minutes to verify every byte on disk. During the 9 hours the port was open, internet scanners tried it 32 times and every attempt was turned away ([details](docs/security.md#field-record-2026-10-13)).

## How it works

1. You (the receiver) run `drop`. It prints a secret **link** and a separate **key**.
2. You send the link and the key to your friend through two different channels, such as the link in a chat and the key read out on a call.
3. Your friend opens the link, types a name and the key, and drags the file in.
4. The file streams over HTTPS directly from their computer to your disk, in 32 MiB pieces. Each piece is checked against a SHA-256 their browser computed. At the end, drop reads the whole file back from your disk and checks it again.
5. Press Ctrl+C when you're done. Nothing keeps running.

The page is a small **lobby**. Everyone in it sees who's connected, their latency, and each transfer's progress, speed, and final SHA-256.

Long transfers get interrupted, and that's fine:
- **A dropped connection, a closed tab, a sleeping laptop, or a restart of drop** only costs the piece in flight. Your friend opens the link again, picks the same file, and it continues where it stopped.
- **drop is careful with your disk.** It writes only inside the folder you choose, never overwrites a file, reserves the space up front, and always leaves 10 GiB free. It never opens, unpacks, or runs what it receives.

## Choose how your friend reaches you

Your friend's browser has to reach your computer. There are two ways, and drop works the same over either:

| | **Port forward** | **VPN** (Tailscale, ZeroTier, WireGuard, Radmin, ...) |
|---|---|---|
| Speed | As fast as your two connections allow | Depends on the VPN. Some carry everything through one slow tunnel. |
| Your friend installs | Nothing | The VPN app, and joins your network |
| You change | One rule on your router, for the session | Nothing on your router |
| Exposed to the internet | One port, while drop runs. Protected by HTTPS, the link, and the key. | Nothing |
| Doesn't work when | Your internet provider uses CGNAT (common on 5G, LTE, and satellite home internet) | Rarely; when the VPN can't connect directly, it relays, which is slower |

If you can, use the **port forward** for speed. Use a **VPN** if you'd rather expose nothing, can't change your router, or are behind CGNAT. The [port forwarding guide](docs/port-forwarding.md) walks through router setup, the CGNAT check, and teardown.

Run drop with no options and it lists your computer's addresses, so you can choose:

```
drop: choose where to listen with -addr. This computer's addresses:

  Tailscale                    -addr 100.101.102.103:8443   a VPN: only people on that network can reach it
  Ethernet                     -addr 192.168.1.20:8443
  Loopback Pseudo-Interface 1  -addr 127.0.0.1:8443        this computer only, for testing

For a router port forward, listen on every adapter: -addr :8443 -host <your public IP>
(any port works, as long as the router forwards to the same one).

To skip this next time, save the choice in drop.json: {"addr": ":8443"}
```

## Install

**Download** the file for your computer from [Releases](../../releases), and check it against `SHA256SUMS.txt` if you like:

| Computer | File | First run |
|---|---|---|
| Windows | `drop-<version>-windows-amd64.exe` (`arm64` for ARM laptops) | Windows may say "Windows protected your PC", because the file isn't code-signed. Click **More info**, then **Run anyway**. |
| macOS | `drop-<version>-darwin-arm64.tar.gz` (Apple silicon) or `-darwin-amd64` (Intel) | Unpack it. macOS blocks unsigned downloads: run `xattr -d com.apple.quarantine drop` once, or allow it in System Settings > Privacy & Security. |
| Linux | `drop-<version>-linux-amd64.tar.gz` or `-linux-arm64` | `tar xzf drop-*.tar.gz` |

Rename the file to `drop` (`drop.exe` on Windows) to match the examples below.

**Or build it** with [Go](https://go.dev/dl/) 1.23 or newer: `go build -o drop.exe ./cmd/drop` (`-o drop` on macOS and Linux).

Your friend needs a current Chrome, Edge, or Firefox. Safari should work, but hasn't been tested yet.

## Quick start

**1. Choose where files land.** drop saves into a folder called `inbox` next to where you run it. It refuses to start if that folder doesn't exist, so a big file can't quietly fill the wrong drive. Create it, or link it to a roomy drive:

```powershell
# Windows: link .\inbox to a big drive (remove the link later with: cmd /c rmdir inbox)
New-Item -ItemType Directory -Path D:\drop-inbox
New-Item -ItemType Junction -Path inbox -Target D:\drop-inbox
```

```sh
# macOS / Linux
ln -s /mnt/big/drop-inbox inbox     # or just: mkdir inbox
```

Or point drop at any existing folder with `-inbox`.

**2. Connect.** Set up the port forward ([guide](docs/port-forwarding.md)), or both join the same VPN network.

**3. Run it.**

```sh
./drop.exe -addr :8443 -host 203.0.113.7     # port forward: your public IP as -host
./drop.exe -addr 100.101.102.103:8443        # VPN: your VPN address
```

```
drop v1.0.0 is listening on [::]:8443 (every adapter: your LAN, your VPNs, and the internet if a port is forwarded). Press Ctrl+C to stop.

  Lobby link:     https://203.0.113.7:8443/t/3kU9...Qw/
  Key:            7KQ4-M2XR-9HBT   (send by a different channel than the link)
  On this PC:     https://127.0.0.1:8443/t/3kU9...Qw/
  Certificate:    SHA-256 AB:C7:0A:...
  Saving to:      D:\drop-inbox  (via inbox), 1.78 TB free
  Limits:         32 MiB chunks, 4 in parallel, 512 GiB total, 10 GiB kept free
```

The first time, your firewall may ask whether drop can accept connections. Allow it.

**4. Send the link and the key** by two different channels, and open the lobby yourself (the "On this PC" link) to watch. Your friend clicks past the certificate warning ("Advanced", then "Proceed"; it's expected for a private link like this), types a name and the key, and drops the file in.

**5. When the lobby shows it done** with its SHA-256, the file is in your inbox. Press Ctrl+C, and remove the port forward if you made one.

## Settings

Every option is a flag, and any of them can be saved in a `drop.json` file in the folder you run drop from, so you don't retype them. Flags on the command line win over the file. Copy [`drop.example.json`](drop.example.json) to start:

```json
{
  "addr": ":8443",
  "host": "203.0.113.7",
  "inbox": "D:\\drop-inbox",
  "keep-free": 20
}
```

| Flag / key | Default | Meaning |
|---|---|---|
| `addr` | (required) | Where to listen: `:8443` for every adapter (port forward), a VPN address like `100.101.102.103:8443`, or `127.0.0.1:8443` for this computer only. Any port works. |
| `host` | the `addr` address | The address or name your friend uses, for the printed link and the certificate. Needed with `-addr :PORT`: your public IP. |
| `inbox` | `inbox` | An existing folder (or a junction or symlink to one) that receives files. In `drop.json`, a relative path is relative to the file. |
| `keep-free` | `10` | GiB always left free on the inbox's drive |
| `max` | `512` | Most GiB accepted in one run, across all files |
| `chunk` | `32` | Piece size in MiB, 1–256 |
| `parallel` | `4` | Pieces the browser sends at once, 1–5. Browsers allow 6 connections to one server, and the lobby needs one. |

`-config FILE` reads a settings file from somewhere else, and `-version` prints the version. A misspelled key in the file stops drop with a message instead of being silently ignored. Backslashes in JSON paths are doubled (`"D:\\drop-inbox"`), or use forward slashes (`"D:/drop-inbox"`).

## Speed

drop is rarely the limit: on one machine it moves about 3.8 Gbit/s over HTTPS. What decides your speed:
- **The sender's upload speed**, usually the slowest link. Many home connections upload far slower than they download.
- **The path between you.** A port forward goes direct. A VPN is direct when it can be and relays through its own servers when it can't. The lobby shows each person's latency and each transfer's speed: tens of milliseconds with good speed means direct.
- **Wi-Fi** on either end. A cable helps more than you'd expect.
- **The receiving disk.** A hard drive takes about 90–100 MB/s of drop's writes, faster than most home uploads.

## How it stays private

- **HTTPS only**, with a fresh certificate made in memory each run.
- **Two secrets, new every run.** The link carries a 256-bit token. Without it, every request gets an empty "not found", so scanners learn nothing. Joining and uploading also need a 12-character key. Both stay valid until you press Ctrl+C, and the next run makes new ones.
- **Write-only.** Nothing you receive can be downloaded back through drop. The lobby shows names, sizes, and progress for this run's transfers; never file contents, and never older files.
- **You control the window.** drop runs until Ctrl+C, and the port forward is yours to open and close.

It accepts the same kind of risk as any web server you'd run at home for an afternoon, and adds the token and key in front of everything. The certificate is self-signed, so the first visit trusts it on sight. Your friend can compare the browser's certificate fingerprint with the banner's `Certificate:` line to rule out interception. The threat model, every control and the test that enforces it, and what a real session attracted: [docs/security.md](docs/security.md).

## If something stops

Nothing is lost. Partial files and their progress live in `inbox/.incoming/`. Start drop again and send the new link and key. Your friend opens it, joins, and picks the same file, and it resumes. The banner lists anything unfinished.

More symptoms and fixes: [docs/runbook.md](docs/runbook.md#if-something-goes-wrong).

## Development

```sh
go vet ./... && go test ./...                      # unit and integration tests (about 15 s)
go test -run '^$' -fuzz FuzzSanitizeName ./internal/store/
DROP_BENCH=1 go test -run Loopback -v ./internal/server/   # throughput over HTTPS on loopback
node dev/e2e/run.mjs                               # real-browser checks; see dev/e2e/README.md
```

Code map:
- `cmd/drop`: the command, flags and `drop.json`, the adapter list.
- `internal/store`: everything on disk.
- `internal/server`: HTTPS, the API, the lobby, and the page in `web/`.
- `internal/creds`: token, key, and certificate.
- `internal/console`: terminal output.

drop uses only the Go standard library. The page is plain HTML, CSS, and JavaScript with no build step. Releases are built by pushing a version tag ([release.yml](.github/workflows/release.yml)).

## License

[MIT](LICENSE)
