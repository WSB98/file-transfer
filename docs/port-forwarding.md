# Port forwarding for drop

A port forward tells your router: "when someone on the internet connects to port 8443, send them to this computer." It's the fastest way to use drop, because your friend's browser connects straight to you. It takes about ten minutes the first time and two after that.

What you're setting up:

```
friend's browser  -->  your public IP : 8443  -->  router  -->  this computer : 8443  -->  drop
                       (the -host in drop's link)  (the rule)   (its LAN address)         (-addr :8443)
```

Any port from 1024 to 65535 works; 8443 is just the example. What matters is that **the same port appears in all three places**: the router rule's outside port, its inside port, and drop's `-addr`.

## 1. Check that your connection can take a port forward

Some internet providers put many customers behind one shared address (**CGNAT**). Then no rule on your router can let anyone in, and you need the [VPN path](../README.md#choose-how-your-friend-reaches-you) instead.

1. Find your **public IP**:
   ```powershell
   Invoke-RestMethod https://api.ipify.org      # Windows PowerShell
   ```
   ```sh
   curl https://api.ipify.org                   # macOS, Linux
   ```
2. Find your **router's WAN (or Internet) IP** on the router's status page. Your router's guide below says where.
3. Compare them:
   - **The same:** you have a public IP, so a port forward will work. Continue.
   - **The WAN IP starts with `100.64` to `100.127`:** that's CGNAT ([RFC 6598](https://www.rfc-editor.org/rfc/rfc6598.html)). Use a VPN, or ask your provider for a public IP.
   - **The WAN IP starts with `10.`, `172.16`–`172.31`, or `192.168.`:** there's a second router in front of yours (**double NAT**). For example, an ISP modem-router with your own router behind it. Either put the ISP box in bridge mode, or add the same forward on both devices.

**Providers that don't allow port forwarding** on their standard plans:
- [T-Mobile Home Internet](https://www.t-mobile.com/support/home-internet/connect) says its gateway can't be configured for it.
- Starlink uses CGNAT unless you're on a plan with a public IPv4 and use your own router ([Starlink help](https://starlink.com/support/article/e3e6bb65-2bbc-1c8a-0c42-74b4776a64f8)).
- Other 5G, LTE, and satellite home internet is usually the same.

On these, use the VPN path.

**IPv6:** in principle it sidesteps CGNAT, but it isn't a dependable fallback. Many routers can't open IPv6 ports, and your friend needs working IPv6 too.

## 2. Give this computer a fixed address on your network

The rule points at your computer's **LAN address**. Most routers hand these out automatically and can change them. Set up a **DHCP reservation** (also called an "IP reservation", "address reservation", or "static lease") so the rule keeps pointing at the right machine.

Run `drop` with no options to see your LAN address, such as `Ethernet ... -addr 192.168.1.20:8443`. Or use `ipconfig` on Windows, or `ip addr` / `ifconfig` on Linux and macOS. Reserve that address for this computer in the router; most of the guides below cover it beside the port-forwarding setting.

A wired connection is better than Wi-Fi for a long transfer. It's also steadier for the rule: some Wi-Fi setups give a computer a new hardware address now and then, which breaks reservations.

## 3. Add the rule

Routers name the fields differently, but they all ask for the same things:

| Field (names you might see) | Value |
|---|---|
| Name, Application, Service | `drop` (anything you like) |
| Protocol | **TCP**. drop is HTTPS, which runs over TCP. "TCP/UDP" or "Both" also works, but opens more than needed. |
| Outside port: External, Original, WAN, Public, Start/End port | `8443` |
| Device or inside address: Internal IP, Forward to, Server IP | This computer's LAN address, such as `192.168.1.20` |
| Inside port: Internal, Forward to port, Private port | `8443`, the same as outside |
| Schedule, Enabled | Always, or on |

Where the setting lives on common routers, from each maker's own documentation (checked October 2026):

| Router | Where | Official guide |
|---|---|---|
| Verizon Fios Router CR1000A | `192.168.1.1` > Advanced > Security & Firewall > Port Forwarding | [User guide (PDF), section 5.0e](https://www.verizon.com/content/dam/verizon/support/consumer/documents/internet/verizon-router-guide.pdf) |
| Verizon Fios Router CR1000B | Advanced > Security & Firewall > Port Forwarding | [User guide (PDF)](https://www.verizon.com/content/dam/verizon/support/consumer/documents/internet/verizon-cr1000b-router-guide.pdf) |
| Verizon Fios Router G3100 | Advanced > Firewall > Port Forwarding | [User guide (PDF), section 6.0d](https://www.verizon.com/content/dam/verizon/support/consumer/documents/internet/fios-router-user-guide.pdf) |
| Xfinity gateway | Xfinity app > WiFi > View WiFi equipment > Advanced Settings > Port forwarding | [Xfinity](https://www.xfinity.com/support/articles/port-forwarding-xfinity-wireless-gateway) |
| AT&T BGW320 / BGW210 | `192.168.1.254` > Firewall > NAT/Gaming > Custom Services (asks for the Device Access Code on the gateway's label) | [BGW320](https://www.att.com/support/article/u-verse-high-speed-internet/KM1394673/), [BGW210](https://www.att.com/support/article/u-verse-high-speed-internet/KM1215101/) |
| Spectrum Advanced WiFi | My Spectrum app > Services > Router > Advanced Settings > Port Forwarding & IP Reservations | [Spectrum](https://www.spectrum.net/support/internet/advanced-wifi-advanced-settings) |
| Cox Panoramic WiFi | Panoramic WiFi app > WiFi > View WiFi Equipment > Advanced Settings > Port Forwarding | [Cox](https://www.cox.com/residential/support/set-up-port-forwarding.html) |
| Netgear | `routerlogin.net` > Advanced > Advanced Setup > Port Forwarding/Port Triggering | [Netgear](https://kb.netgear.com/24290/How-do-I-add-a-custom-port-forwarding-service-on-my-NETGEAR-router) |
| Netgear Orbi | `orbilogin.com` > Advanced > Advanced Setup > Port Forwarding/Port Triggering | [Orbi](https://kb.netgear.com/31069/How-do-I-configure-port-forwarding-on-my-Orbi-system) |
| TP-Link | Advanced > NAT Forwarding > Port Forwarding (or Virtual Servers, depending on model) | [TP-Link](https://www.tp-link.com/us/support/faq/1379/) |
| ASUS | `asusrouter.com` > WAN > Virtual Server / Port Forwarding | [ASUS](https://www.asus.com/support/faq/1037906/) |
| Linksys (app) | Advanced Settings > Port Settings > Single Port Forwarding | [Linksys app](https://support.linksys.com/kb/article/328-en/) |
| Linksys (web) | Security > Apps and Gaming > Single Port Forwarding | [Linksys web](https://support.linksys.com/kb/article/318-en/) |
| eero | eero app > Settings > Advanced networking > Reservations & port forwarding | [eero](https://eero.com/support/articles/how-do-i-set-up-port-forwarding) |
| Google Nest Wifi / Google Wifi | Google Home app > Wifi > Settings > Advanced Networking > Port management | [Google](https://support.google.com/googlenest/answer/6274503) |
| Ubiquiti UniFi | Settings > Policy Engine > Port Forwarding (Network 9.4) | [UniFi](https://help.ui.com/hc/en-us/articles/235723207-UniFi-Gateway-Port-Forwarding) |

Menus move between firmware versions. If yours doesn't match, search your router's model number plus "port forwarding" on the maker's own site.

Provider quirks:
- **Xfinity and Cox:** "Advanced Security" can block forwarded connections. If the rule is right and nothing connects, turn it off for the session.
- **AT&T:** if NAT/Gaming shows a warning, port forwarding may need to be enabled on your account first.

## 4. Run drop

```sh
./drop -addr :8443 -host <your public IP>
```

Or save both in `drop.json` once (`{"addr": ":8443", "host": "203.0.113.7"}`) and just run `./drop`. Your public IP can change over time, so check it again if a later session doesn't connect.

The first time, your firewall asks whether drop may accept connections. Allow it. On Windows, choose **Public** networks if that's what your network is set to (the Windows default for most home networks).

## 5. Test it

The real test is your friend opening the link. To test it yourself first, use a phone with **Wi-Fi turned off**, so it comes from outside your network. Open the link and look for the certificate warning. Seeing it means the path works; you don't need to go further.

Opening the public link from a computer on your own network may or may not work. That depends on whether your router supports "hairpin NAT". It isn't a sign the forward is broken. drop's "On this PC" link always works locally.

## 6. Teardown

1. Press Ctrl+C in drop.
2. **Delete the rule**, or switch it off if your router allows that.

Why bother, when nothing is listening anymore? Because the rule outlives drop. If any program on this computer later listens on that port, it would be reachable from the internet without anyone noticing. The DHCP reservation can stay.

## If it doesn't connect

Check these in order:
1. **Is drop running, and listening on `[::]:8443` or `0.0.0.0:8443`?** The banner's first line says "every adapter". `-addr 127.0.0.1:8443` or a VPN address won't accept forwarded connections.
2. **Do the ports match** in the rule (both sides) and in `-addr`?
3. **Does the rule point at this computer's current LAN address?** Run `drop` with no options to see it.
4. **Is the protocol TCP?**
5. **Did the firewall prompt get allowed?** On Windows, check Windows Defender Firewall > Allow an app, for `drop.exe` on the right network type.
6. **Is the link's address your current public IP?** It's the `-host` you passed.
7. **CGNAT or double NAT?** See step 1.
8. **Security features** on the router or the provider's app (Xfinity and Cox "Advanced Security") may block it.
9. **Is the friend's network blocking it?** Some work and school networks block unusual ports. Try another network, or a common port like 443 if nothing on your computer uses it (`-addr :443` and a rule for 443).
