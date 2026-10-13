# Runbook: a transfer session, start to finish

The receiver runs drop and the sender uses a browser. These steps are for the receiver. Steps marked **(exposes)** make this computer reachable from other machines; do them deliberately, and undo them in Teardown.

## Before the day

1. **Pick the network path.** A port forward is fastest; a VPN exposes nothing. See the README's comparison, and the [port forwarding guide](./port-forwarding.md) for the router steps and the CGNAT check.
2. **Make the inbox** on a drive with room for the file plus 10 GiB (see the README's quick start). drop won't create it for you.
3. **Estimate the time.** It's mostly the sender's upload speed: 200 GB takes about 4.5 h at 100 Mbps, 1.5 h at 300 Mbps, or 22 h at 20 Mbps. Then add the read-back check, about 20 minutes per 200 GB on a hard drive. Resume makes it fine to span several evenings.
4. **Optional: save your settings** in `drop.json` (see the README), so each session is just `./drop`.
5. **On a VPN:** both join one private network. Leave shared or public VPN networks you don't need: their members could reach your address too, though they'd still need the link and the key.

## The session

1. **(exposes)** Open the path: add the port-forward rule, or connect the VPN.
2. **(exposes)** Start drop:
   ```sh
   ./drop -addr :8443 -host <your public IP>    # port forward
   ./drop -addr <your VPN address>:8443         # VPN
   ```
   If the firewall asks whether drop may accept connections, allow it. On Windows, allow it on **Public** networks if that's what your network is set to: Windows marks most home networks, and most VPN adapters, as Public.
3. **Check the banner:**
   - The first line says who can reach drop: `every adapter ... the internet if a port is forwarded`, or `<VPN name> only`.
   - `Saving to:` is the drive you expect, with enough free space.
   - Any `Unfinished:` lines are earlier partial uploads that will resume.
4. **(exposes)** Send the **lobby link** by one channel (chat) and the **key** by another (a call, or a different app).
5. **Open the lobby yourself** with the "On this PC" link or the lobby link, plus the key and your name. You'll see your friend arrive, their latency, and the transfer as it goes.
6. **The sender** opens the link, clicks past the certificate warning (Advanced, then Proceed), types a name and the key, and drops the file in. They keep the tab open and the computer awake, and plugged in if it's a laptop.
7. **When the lobby shows done** with a SHA-256, the file is in the inbox. drop keeps running for more files until you stop it. After the last file, the sender's tab goes quiet: no more uploads and nothing to send is what "done" looks like, not a stall.

**What the log shows on an open port.** Lines like `rejected 2 request(s) without the link, latest from 203.0.113.50` are internet scanners bouncing off; a real session saw about one or two new ones an hour (see [security.md](./security.md#field-record-2026-10-13)). They need nothing from you. **`WARNING: ... with the link but a wrong key`** is different: if it isn't your friend mistyping, someone else has the link. Press Ctrl+C. The next run has a new link and key, and the upload resumes.

## Teardown

1. Press **Ctrl+C** in drop's terminal.
2. **Delete the port-forward rule** if you made one. A forward to a port nothing listens on is harmless today, but anything that listens on that port later would be on the internet without you noticing.
3. **Optional: remove drop's firewall rule.** On Windows this needs an **administrator** terminal (Win+X, then Terminal (Admin)); a normal one fails with "Access is denied":
   ```powershell
   Get-NetFirewallApplicationFilter | Where-Object Program -like '*\drop.exe' | Get-NetFirewallRule | Remove-NetFirewallRule
   ```
   Or use the Windows Defender Firewall settings (`wf.msc`), Inbound Rules, and delete the `drop.exe` entries. The firewall asks again next time.
4. **Optional, the independent check:** the sender hashes their original file and compares it with the SHA-256 in the lobby. Windows: `Get-FileHash "<file>"` (or `certutil -hashfile "<file>" SHA256`). macOS: `shasum -a 256 "<file>"`. Linux: `sha256sum "<file>"`.

## If something goes wrong

| Symptom | Likely cause |
|---|---|
| `drop: choose where to listen with -addr` | No `-addr` on the command line or in `drop.json`. Pick one from the list it prints. |
| `drop.json: unknown setting "..."` | A misspelled key. The keys are the flag names: `addr`, `host`, `inbox`, `chunk`, `parallel`, `max`, `keep-free`. |
| `listen tcp <VPN address>:8443: bind: ... not valid` | The VPN isn't connected, so that address doesn't exist yet. Connect the VPN first. |
| `bind: Only one usage of each socket address` / `address already in use` | Something else (maybe another drop) is on that port. Stop it, or choose another port, and forward that one instead. |
| `drop: inbox "inbox" does not exist` | Create the folder, recreate the junction or symlink, or pass `-inbox`. |
| `cannot write to the inbox` | The drive is missing, full, or read-only. |
| Friend can't open the link (port forward) | The forward is missing or points at the wrong computer or port, the firewall prompt was denied, `-host` isn't your public IP, or your provider uses CGNAT. See the [port forwarding guide](./port-forwarding.md#if-it-doesnt-connect). |
| Friend can't open the link (VPN) | They aren't in your VPN network, the VPN isn't connected on one side, or the firewall prompt was denied. Try pinging your VPN address from their computer. |
| Slow, with high latency in the lobby | A VPN relaying instead of connecting directly, or Wi-Fi on either end. Check the VPN app for a "direct" or "relay" status. If a VPN stays slow while both connections are fast, try a port forward. |
| Slow, with normal latency | Usually the sender's upload speed. Check it with a speed test on their side. |
| Browser shows no "Proceed" option | Some managed or work browsers forbid it. Use another browser. |
| Friend's page says the key is wrong | A typo, or a key from an earlier run: each run makes a new one. |
| `refused ... kept free` | The file wouldn't leave `keep-free` GiB on the drive. Free up space, or lower `keep-free`. |
| drop was stopped mid-transfer | Run it again and send the new link and key. The sender opens it, joins, and picks the same file. It resumes, and the banner shows it under `Unfinished:`. |
| `unfinished upload ... cannot be resumed` | Its progress file is damaged (very rare). The message names the two files in `inbox/.incoming/` to delete; then the sender starts that file over. |

## Removing a Windows inbox junction

Use `cmd /c rmdir inbox`. It removes only the link. Don't use `Remove-Item -Recurse` in Windows PowerShell 5.1: it can follow the junction and delete the files it points to.
