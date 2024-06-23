# Rdv: Relay-assisted p2p connectivity

[![Go Reference](https://pkg.go.dev/badge/github.com/betamos/rdv.svg)](https://pkg.go.dev/github.com/betamos/rdv)

Rdv (from rendezvous) is a relay-assisted p2p connectivity library that quickly and reliably
establishes a TCP connection between two peers in any network topology,
with a relay fallback in the rare case where p2p isn't feasible. The library provides:

-   A client for dialing and accepting connections
-   A horizontally scalable http-based server, which acts as a rendezvous point and relay for clients
-   A CLI-tool for testing client and server

Rdv is designed to achieve p2p connectivity in real-world environments, without error-prone
monitoring of the network or using stateful and complex port-mapping protocols (like UPnP).
Clients use a small amount of resources while establishing connections, but after that there are
no idle cost, aside from the TCP connection itself.
[See how it works below](#how-does-it-work).

Rdv is built to support file transfers in [Payload](https://payload.app/).
Note that rdv is experimental and may change at any moment.
Always use immature software responsibly.
Feel free to use the issue tracker for questions and feedback.

## Why?

If you're writing a centralized app, you can get lower latency, higher bandwidth and reduced
operational costs, compared to sending p2p data through your servers.

If you're writing a decentralized or hybrid app, you can increase availability and QoS by having an
optional set of rdv servers, since relays are necessary in some topologies where p2p isn't feasible.
That said, rdv uses TCP, which isn't suitable for massive mesh-like networks with
hundreds of thousands of interconnected nodes.

You can also think of rdv as a <1000 LoC, minimal config alternative to WebRTC, but for non-realtime
use-cases and BYO authentication.

## Quick start

Install the rdv CLI on 2+ clients and the server: `go build -o rdv ./cmd` from the cloned repo.

```sh
# On your server
./rdv serve

# On client A
./rdv dial http://example.com:8080 MY_TOKEN  # Token is an arbitrary string, e.g. a UUID

# On client B
./rdv accept http://example.com:8080 MY_TOKEN  # Both clients need to provide the same token
```

On the clients, you should see something like:

```sh
INFO client: peer connected is_relay=false addr=192.168.1.16:39841 dur=45ms
```

We got a local network TCP connection established in 45ms, great!

The `rdv` command connects stdin of A to stdout of B and vice versa, so you can now chat with your
peer. You can pipe files and stuff in and out of these commands (but you probably shouldn't,
since it's unencrypted):

```sh
./rdv dial MY_TOKEN < some_file.zip
./rdv accept MY_TOKEN > some_file.zip
```

## Server setup

Simply add the rdv server to your exising http stack:

```go
func main() {
    server := &rdv.Server{}
    server.Start()
    defer server.Close()
    http.ListenAndServe(":8080", server)
}
```

You can use TLS, auth tokens, cookies and any middleware you like, since this is just a regular
HTTP endpoint. If you put the rdv server on a sub-path, make sure to strip the prefix:

```go
http.Handle("/rdv/", http.StripPrefix("/rdv/", server))
```

If you need multiple rdv servers, they are entirely independent and scale horizontally.
Just make sure that both peers connect to the same server.

### Beware of reverse proxies

To increase your chances of p2p connectivity, the rdv server needs to know the source
ipv4:port of clients, also known as the _observed address_.
In some environments, this is harder than it should be.

To check whether the rdv server gets the right address, go through the quick start guide above
(with the rdv server deployed to your real server environment),
and check the CLI output:

```sh
# NOTE: This is normal when running locally
WARN client: expected observed to be public ipv4 (check server config)
```

If you see this warning, you need to figure out who is meddling with your traffic, typically
a reverse proxy or a managed cloud provider.
Ask them to kindly
forward _both the source ip and port_ to your http server, by adding http headers such as
`X-Forwarded-For` and `X-Forwarded-Port` to inbound http requests.
Finally, you need to tell the rdv server to use these headers, by overriding the `ObservedAddrFunc`
in the `ServerConfig` struct.

## Client setup

Unlike with most p2p, clients don't need to monitor network conditions continuously,
so they're pretty much stateless and thus easy to use:

```go
client := &rdv.Client{}
token := "abc"

// On the dialing device
conn, _, err := client.Dial("https://example.com/rdv", token)

// On the accepting device
conn, _, err := client.Accept("https://example.com/rdv", token)
```

### Signaling

Both peers need to agree on a server addr and an arbitrary token in order to connect
to each other. Typically, the dialer generates a token for each conn and _signals_
the other peer through an application-specific side-channel. You could, for instance,
share the endpoint details manually or use a websocket API to notify peers, depending
on your application.

### Authentication

Even if you are running rdv server behind TLS, this only secures the client-server data.
Once a p2p connection is established, it is for security purposes equivalent to standard TCP.
You can (and should) authenticate and encrypt rdv conns using e.g. TLS with client
certificates or Noise, depending on your application's identity model.

## How does it work?

Under the hood, rdv repackages a number of highly effective p2p techniques, notably
STUN, TURN and TCP simultaneous open, into a flow based on a single http request,
which doubles as a relay if needed:

```
  Alice                  Server                  Bob
    ┬                      ┬                      ┬
    │                      │                      |
    │            (server_addr, token)             |
    │ <~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~> │  (Signaling)
    │                      │                      |
    │ DIAL /foo           HTTP                    │
    ├────────────────────> │          ACCEPT /foo │  Request
    │                      │ <────────────────────┤
    │                      │                      │
    │           101 Switching Protocols           │
    │ <────────────────────┼────────────────────> │  Response
    │                      │                      │
    │ ACCEPT foo          TCP           DIAL foo  │
    │ <═════════════════════════════════════════> |  Connect
    │                      │                      │
    │ CONTINUE             |                      │
    ├───────────────────── ? ───────────────────> │  Pick
    │                      │                      │
    │ <~~~~~~~~~~~~~~~~~~~ ? ~~~~~~~~~~~~~~~~~~~> │  (Application Data)
    │                      │                      │
    ┴                      ┴                      ┴
```

**Signaling**: Before connecting, both peers must agree on the endpoint. This is
application-specific.

**Request**: Each peer opens an `SO_REUSEPORT` socket, which is used through out the attempt.
They dial the rdv server over ipv4 with a `http/1.1 DIAL /<token>` (or `ACCEPT`) request:

-   `Connection: upgrade`
-   `Upgrade: rdv/1`, for upgrading the http conn to TCP for relaying.
-   `Rdv-Self-Addrs`: A comma-separated list of self-reported ip:port addresses. By default,
    all public and private ipv4 and ipv6 default-route addrs are used.
-   Optional application-defined headers (e.g. auth tokens)

**Response**: Once both peers are present, the server responds with a `101 Switching Protocols`:

-   `Connection: upgrade`
-   `Upgrade: rdv/1`
-   `Rdv-Observed-Addr`: The connecting device's server-observed ipv4:port, for diagnostic purposes.
    This serves the same purpose as [STUN](https://en.wikipedia.org/wiki/STUN).
-   `Rdv-Peer-Addrs`: A comma-separated list of the peer's candidate addresses, consisting of
    both the self-reported and observed addresses.
-   Optional application-defined headers.

The connection remains open to be used as a relay. This serves the same purpose as
[TURN](https://en.wikipedia.org/wiki/Traversal_Using_Relays_around_NAT).

**Connect**: Clients simultenously listen and dial each other on all candidate peer addrs,
which helps open up firewalls and NATs for incoming traffic.
Both peers write an rdv-specific `rdv/1 <METHOD> <TOKEN>\n` header on all opened TCP conns
(except the relay), to detect misdials. Note that some connections may result in
[TCP simultenous open](https://ttcplinux.sourceforge.net/documents/one/tcpstate/tcpstate.html).

**Pick**: The dialing peer picks a connection and writes `CONTINUE\n` to it. By default,
the first available p2p connection is chosen, or the relay is used after one second.
All other conns, and the socket, are closed. As a special case, the command `OTHER <ip:port>\n`
is sent to the rdv server, if a p2p conn was chosen, for server metrics.

**Application Data**: The resulting TCP connection is now ready for use by the application.
Remember to secure these connections (see authentication).

## Limitations

Rdv is arguably very reliable, compared to other p2p technology. However, it's largely untested in
these environments:

-   Client firewall prevents listening on a TCP high-number port
-   Client is using a VPN
-   Client is using an http proxy
-   Client is ipv6-only, or is using ipv4-mapped addresses
-   Client platform is not a major OS supported by https://github.com/libp2p/go-reuseport

Rdv may either work normally, use the relay unnecessarily, or in the worst case, not
work at all. Bug reports should include verbose logs and ideally, as much information
about the local network as possible.

## Future Work

**Non-default routes**: Rdv does not currently uses the default network route, preventing use of
e.g. LTE when WiFi is the default. Alternate routes could help with connectivity and/or allow
spreading load across network paths. It is currently not clear how such features would be best
implemented and exposed, or how they interact with proxies and VPNs (see above).

**Fast start**: Rdv is designed to first detect p2p with a timeout, and then fall back to the relay
if unsuccessful. This imposes a tradeoff between using a better p2p route (longer timeout) and
yielding a usable connection quickly (shorter timeout). Tuning the timeout is also
hard, since network conditions and latencies vary a lot. Instead, we could return a usable relay
conn immediately, and transparently switch over to an available p2p conn later. That would
make this tradeoff (and difficult tuning problem) disappear.
This would require changes to the wire protocol, and probably the client library API.
