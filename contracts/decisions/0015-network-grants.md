# 0015. Network grants: a host name means that host name, enforced by a proxy and recorded on the bus

- **Status:** proposed
- **Date:** 2026-11-06

## Context

`Grant{Kind: "net", Host: "a.example.com"}` recorded a host and enforced nothing about it. Enforcement was binary: a domain either had a network namespace of its own (`--unshare-net`, no network at all) or, once any net grant existed, the host's (`HasNet(m)` dropped the flag and bound `resolv.conf` and `hosts`). Grant one host and the domain could reach anything, including host loopback services and abstract unix sockets. The manifest line `net:a.example.com` read like a control and was not one. This is the class of overclaim 0012, 0013 and 0014 were written to stop, and the adapter's own README listed it as "partial (coarse)".

The reference point is a per-connection firewall that tells the user what is connected to what. What a person needs is to be informed and in control, able to reason about the system, rather than to be asked to click Allow. v1 is two things: enforcement that is honest, and observability. Profiles, inheritance and rule sharing are a later record.

## Decision

### What is now enforced, and by what

- **No grant: unchanged.** The domain has `--unshare-net`. `/proc/net/dev` inside it lists `lo` and nothing else; it has no `resolv.conf`.
- **With a grant: the domain still has `--unshare-net`.** It is now unconditional. The grant no longer shares the host network. Instead `domain run` starts a proxy on the host (`adapter/linux/internal/dom/netproxy.go`), listening on `<SockDir>/net.sock`, which the domain sees at `/run/aurora/net.sock` (the directory already bound for the bus socket). A relay inside the domain (`netfwd.go`, the `domain` binary bound read-only at `/run/aurora-netfwd` and run as the payload's parent) listens on `127.0.0.1:3128` in the domain's own namespace and copies bytes to that socket. It decides nothing. `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` (and lower-case forms) point at it.
- **The decision is made on the host, by name.** For `CONNECT host:port` the host in the request line; for a plain request the host of the absolute URI; for a TLS tunnel opened by a `CONNECT`, the SNI of the client's first record must equal the `CONNECT` host. The policy is the domain's recorded net grants, compared by whole-string equality after lower-casing (`a.example.com` does not match `a.example.com.evil.net`, `evil-a.example.com` or `xa.example.com`; the demo tries all three). A name not granted is answered `403 Forbidden` at once, with a body that names the domain, the host and the reason, and an `X-Aurora-Verdict: refused` header. It is not a hang; the demo measures it at tens of milliseconds, and `curl` reports `CONNECT tunnel failed, response 403`.
- **The grant grammar is closed and fails closed**, as 0014's device classes are. `domain grant --net` accepts a bare host name or an IP literal, nothing else. A wildcard, a port, a scheme, a path or an empty string is refused at grant time (`NormalizeHost`), so no manifest line can claim more or less than it is. A grant already on disk that does not normalise allows nothing. An IP literal grant is a grant for that literal, compared as a string; it is not the set of names that resolve to it.
- **A refusal of a name never resolves it** (see DNS). Only an allowed name is resolved, by the host's resolver, inside the proxy, and the proxy dials the address it resolved and no other.

### Why a proxy that sees the host name, and not IP filtering

An IP set cannot honestly stand for a host name. CDNs and shared hosting put thousands of names on one address and move one name across many; a name's addresses change within hours. A rule that says "a.example.com" and enforces "these eight addresses, resolved at 09:00" is wrong in both directions by afternoon: it refuses the host the user agreed to, and it allows every other tenant behind those addresses. That is a manifest claim that reads like a control and is not one, which is the exact failure this record exists to end. A rule of "allowed IPs" would be honest if it said "allowed IPs"; this grant says a name.

The mechanism that can be honest is one that sees the name the application asked for: `CONNECT` in the request line, the URI host, the TLS SNI. A proxy gets that for free. It is the same fact the user read when they agreed.

The proxy lives on the **host side of a unix socket**, not inside the domain's namespace, and that is a deliberate departure from the brief's first suggestion. The domain's namespace stays empty. The brief proposed a proxy inside the namespace with the network still present, which would have left a route out for any process that did not use the proxy; the policy would then depend on the application's cooperation. With an empty namespace there is no route to cooperate around: raw sockets, hard-coded IPs, QUIC and DNS from inside the domain are not refused by a rule, they have nowhere to go (the demo shows `Network is unreachable`). The only door is the socket, and the code behind it runs outside the domain, where the domain cannot reach the policy or the process.

### DNS

What was done: **the domain cannot resolve anything.** It has no `resolv.conf` and no `hosts`, no route, and a UDP datagram to port 53 fails at send. Name resolution is the proxy's, and it happens only after the name has been allowed. A refused name is never looked up, so `secret-payload.exfil.example` carried in a hostname does not become a query to the zone's authoritative server. The demo proves this from the record: the verdict for such a name is `refused`, not `failed: cannot resolve`, which is what it would be had a lookup been tried.

What was not done:
- A name the domain is **granted** is resolved by the host's resolver, and a resolver is observable by whoever runs the zone. A grant for `a.example.com` lets the domain cause a lookup of `a.example.com`, and only that. That is the point of exact names. This is also why wildcards are refused: `*.example.com` would let the domain look up `<anything>.example.com`, which is a channel.
- The host's own DNS configuration (DNSSEC, which resolver, DoH at the host) is not touched.
- Names that resolve to an address the user would not expect (see the weakest point).

### Events: what is connected to what

Every decision the proxy makes is reported on the bus, carrying the domain, the host, the port, the verdict (`allowed`, `refused`, or `failed` for a granted host that was unreachable) and the reason (`net grant`, `no net grant for this host`, `TLS SNI "x" does not match the CONNECT host`, ...). The demo prints them.

- **Namespace: `net.`**, with `net.conn` (one decision) and `net.summary` (counts). `net.` joins `systemPrefixes` in `bus/server/server.go` next to `provenance.`, `approval.`, `bus.`, `agent.`, `index.` and `supervisor.`. The two existing filters apply unchanged: a domain connection cannot subscribe to a literal `net.` topic (`reserved_topic`), and the delivery filter keeps `net.` events out of a `**` or `*.conn` subscription it was allowed to make. Nothing was routed around the policy.
- **A domain cannot write there either.** The daemon refuses a publish to `net.` from a domain connection. This is new: the existing system prefixes are protected for reading, and `bus.` is also protected for writing; `net.` is protected for both because an event that says "ng-b was refused host X" is a statement about another domain that the domain would otherwise be able to make. (The other prefixes are not made write-protected here; that is the same observation about them and is not changed by this record.)
- **The domain on each event is stamped, not claimed.** Events are published by the daemon from a control verb, `bus.net.report@v1`, which only the session socket may call (a domain is refused `forbidden`). The report names a registered domain identity; the daemon refuses an identity it is not hosting, sets the envelope's `from` to that registered identity, and derives the payload's `domain` field from it. The reporter cannot write a domain name that is not one the daemon holds a live socket for, and the domain being reported on cannot report at all. What this does **not** make true: the reporter is the user's own `domain run` process and is trusted to report the domain it is running. A same-uid process outside a domain can call the verb for any registered domain, as it can call every session-socket verb (0001 and 0007 say the session is one trust zone). The stamp attributes to a domain the daemon hosts; it does not authenticate who is speaking for it.
- **A domain cannot subscribe to another domain's connection events. Proven** in `bus/server/netreport_test.go` and in `demos/network-grants/run.sh`: a domain's explicit subscription to `net.conn`, `net.*`, `net.**` and `net.summary` is refused, a `**` subscription is accepted and then receives none of another domain's `net.` events (a control event on an ordinary topic does arrive, so the subscription is live), and its attempts to publish `net.conn` or `net.summary`, or to call the report verb, deliver nothing.

#### Volume

The supervisor's liveness pings once filled 55% of the append-only hash-chained log with records nobody read. A bus event per connection would repeat that for the first chatty application, and a hostile one could choose the number. So a connection becomes an event only this way (`netreport.go`):

- The first connection of each `(host, verdict, reason)` in a run is a `net.conn` event at once. A new refused host is never held back, because it is the thing a person wants to see.
- Every later one only increments a counter. Counters leave as one `net.summary` per 30 seconds and one when the run ends, however many connections there were.
- A token bucket (burst 20, then 2 per second) bounds even the first-seen events, because a domain asking for a thousand different names would otherwise be a thousand events. What the bucket holds back is counted in the summary's `dropped`, not lost; the summary also carries every host's count.
- The table of distinct hosts is capped at 512; beyond it new keys share `(other)`.

Measured, not estimated: 1000 requests from one run (500 to a granted host, 500 to 500 different refused names) wrote 21 records to the log, and the summary accounted for exactly 500 allowed and 500 refused. The bound is independent of traffic: 1 + 20 + refill over the run's duration + one summary per interval. An event is logged (not ephemeral): these are records of what a domain did, and the point is that they are few.

Two things this does not give. Events exist only when a bus is running and `AURORA_BUS_SOCK` names it; without one the proxy still enforces and reports nothing (the run says so once on stderr when the bus is configured but unreachable). And the log is a record of decisions the proxy made, not of connection attempts that never reached it (see below).

## What this does not stop

A proxy sees names. That is its strength and its edge. This list is the record's weakest points, in order of how much they matter, none of them fixed.

1. **A domain that tunnels over an allowed host.** The proxy allows the name, and then carries bytes. After `CONNECT a.example.com:443` it checks only that a TLS hello, if one is the first thing sent, names `a.example.com`; it does not look inside the stream. A domain that is allowed `a.example.com` can send that host anything and ask it to forward, can run a different protocol over the connection (the demo sends an HTTP request for another `Host` through a `CONNECT` and it is carried), and can encode data in requests to a host that logs them. A grant for a host the user does not control is a grant for whatever that host's operator does. If the host is a general-purpose service (a paste site, a cloud storage API, a CDN fronting arbitrary tenants), the grant is as wide as the service. The SNI check narrows the case of one address serving unrelated names for a TLS client that names a different one; it does not touch tenancy behind the name itself.
2. **A granted name that resolves, or is rebound, to a local address.** The proxy dials the address the name resolves to, whatever it is. A granted `a.example.com` whose DNS is changed to `127.0.0.1` or to a private-network address gives the domain a connection to this machine's loopback services or to the LAN. The demo does this on purpose: `a.test` resolves to a loopback address and the proxy dials it. Before this record a net grant was strictly worse (all of the host network), so this is not a regression, but it is not "egress to the internet only" and must not be read as such. Refusing loopback, link-local and private ranges by default, with an explicit way for a user to say "yes, this one" (a NAS name), is the obvious fix and is deferred because it needs a place for that exception to live (a per-grant flag is a grammar change).
3. **DNS-over-HTTPS and its relatives.** A domain granted a DoH resolver's name can ask it to resolve anything, which is a tunnel in the sense of (1). The proxy has no list of such names and could not tell one from any other HTTPS host.
4. **What the proxy cannot see because the first bytes are not a hello it understands.** A `CONNECT` whose client speaks something other than TLS first is carried unexamined; a client that waits for the server to speak first (more than half a second of silence) is carried unexamined; a TLS hello with no SNI is carried (the destination is still the granted address). A hello the proxy cannot parse, including one split across records to hide its name, is refused. Encrypted ClientHello would hide the real name behind a public one and is not handled; with ECH the check compares the outer name.
5. **Attempts that never reach the proxy are not events.** A raw socket, a hard-coded IP, a UDP datagram and QUIC are stopped by the empty namespace, and they are stopped silently from the point of view of this record: the kernel answers `ENETUNREACH` and there is nothing to log, because there is no component on that path. The enforcement is real and kernel-level; the observability of it is not. A person reading `net.` events sees what the proxy decided, not everything the domain tried.
6. **The relay and the proxy are code.** The proxy parses attacker-controlled HTTP request heads on the host, as the user, outside any sandbox. It is Go (memory safe), the head is read with `net/http` under a 10-second deadline, and it opens no files, but a parser bug there is a bug in the privileged half. It is the same position as 0014's compositor parser, with a smaller parser.
7. **Any process in the domain can use the proxy.** The proxy identifies the domain by which socket the connection came in on. Two processes in one domain are one principal, as they are everywhere else in this design.
8. **All ports of a granted host.** The grant has no port. `a.example.com:22` is as allowed as `:443`. Per-port rules are deferred.
9. **It is a control on where, not on what.** Nothing here inspects what is sent to a granted host.
10. **A same-uid process on the host can reach the proxy socket path** if it can name it (`$XDG_RUNTIME_DIR/aurora/domains/<name>/net.sock`, inside a 0700 directory it can, being the same user). It then gets the domain's policy, which is no more than the domain has.

**One more claim to correct.** TLS libraries inside a domain may fail to initialise for an unrelated reason: this host's `/etc/ssl/openssl.cnf` has `.include = /etc/crypto-policies/back-ends/opensslcnf.config`, and a domain does not have `/etc/crypto-policies` (Python's `ssl` failed with `MODULE_INITIALIZATION_ERROR` here; `curl` did not). The demo works around it for its own client with `OPENSSL_CONF=/dev/null`. That is a missing `/etc` file, an existing limit, not something this record introduced; it means "granted a host" is not "TLS works" for every runtime.

## What is deferred

- **Profiles, inheritance, rule sharing.** A later record. v1 has one flat list of hosts per domain, and no way to say "this app is like that app".
- **Per-port rules.** Not in v1; the grammar refuses a port rather than ignore it.
- **Egress to local networks.** Not filtered (weakest point 2), and no way yet to grant a LAN name explicitly. This is the first thing to do next.
- **A prompt on first connection.** A person is informed (events) but not asked. An interactive "this domain wants b.example.com: allow once, allow always" is a reasonable thing to build on the same events and is not built; the approval route of 0007/0011 is the place for it.
- **Verbs minting net grants.** `device:` capabilities are minted from a verb's `capabilities` by `planDevices` (0014). There is no `net:<host>` equivalent in the controller: the manifest validator accepts `net:<h>` and nothing in `controller/internal/invoke` turns it into a grant. Today a net grant is the operator's `domain grant --net`. Wiring the verb path is the same shape as `planDevices` and was not done here; until then, a manifest's `[capabilities] network` is still a declaration nothing enforces from the controller's side.
- **A display of events.** Nothing in the shell reads `net.conn` yet. The log and `bus sub 'net.**'` are the interfaces.
- **Not proven: IPv6 literal grants through the demo**, the `AURORA_NET_RESOLVE` static table in production (it is a test seam read from the environment of the `domain run` process, which a domain cannot set), proxy behaviour under thousands of simultaneous tunnels.

## Consequences

- `domain grant --net` now means something and refuses what it cannot mean. A domain with a net grant cannot reach a host the user did not name, cannot send a raw packet anywhere, and cannot resolve a name.
- Programs that do not honour `HTTP_PROXY` get no network from a net grant (`curl` honours it, which is what the demo uses; other clients were not tried, and many programs ignore the variable). That is the intended cost of honesty: a program that cannot be seen is not granted.
- Hosts that need `Host`-less or non-HTTP protocols (SSH, SMTP, raw TCP) can use `CONNECT` to a granted host and port if their client speaks HTTP proxies; most do not. Those get nothing.
- `0014`'s fail-closed posture is followed: a bad class there, a bad host here, fails at grant time.
- Behaviour change: a domain that held a net grant before this record could reach loopback services on the host. It cannot now. The existing test that asserted exactly that (`TestNoNetworkByDefault`) was changed to assert the opposite.
- `net.` is a new system namespace; consumers that enumerate `systemPrefixes` see it.
