# Browser checks

`run.mjs` drives real browsers against a real `drop` binary on this machine only (everything binds to 127.0.0.1), the way a person would use it.

```sh
go build -o drop.exe ./cmd/drop
node dev/e2e/run.mjs                    # all scenarios
node dev/e2e/run.mjs --only outage      # one scenario
node dev/e2e/run.mjs --shots ./shots    # also save screenshots
```

Requirements: Node 22 or newer (no packages to install), and at least one of Chrome, Edge, or Firefox. A missing browser skips its scenarios.

| Scenario | Browser | What it proves |
|---|---|---|
| `lobby` | Two Chrome or Edge windows | Two people join under names and each sees both, with "you" beside their own name and both latencies measured. A name made of markup (`<img onerror=...>`) stays plain text. One person watches the other's upload partway, attributed to them, then sees it done with the same SHA-256. The join is logged. |
| `resume` | Chrome or Edge | The certificate warning clicks through into a secure context. A wrong key is refused with a clear message, and a loosely typed key works. A reload mid-upload (throttled to 400 Mbps so it really is mid-upload) remembers the key, and re-picking the file resumes partway instead of restarting. Then hashes match end to end, the inbox holds only the file, and the server log has nothing alarming. |
| `restart` | Chrome or Edge | drop is killed abruptly mid-upload, as a crash or power cut would. The data and progress survive in `.incoming`, and the page says the connection is lost. The restarted banner lists the unfinished upload. The new link asks for its new key, and re-picking resumes partway. The hashes match, `.incoming` ends empty, and the log has no warnings. |
| `outage` | Chrome or Edge | Through a proxy capped at 100 Mbps, the upload fills the link. A silent 20 s outage shows on the page, the upload recovers and finishes with matching hashes, and browser memory stays under 512 MB. |
| `firefox` | Firefox | A secure context and a full upload with matching hashes, over WebDriver BiDi. |

`lib/` holds the drivers: Chrome DevTools Protocol, WebDriver BiDi, a throttling TCP proxy that can silently black-hole traffic, and a launcher that reads the link and key from drop's banner. Each test file, inbox, and browser profile lives in a temp folder that is removed afterwards.
