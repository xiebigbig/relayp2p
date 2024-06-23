# Rdv: Relay-assisted p2p connectivity

## Quick start

Install the rdv CLI on 2+ clients and the server: `go build -o rdv ./cmd` from the cloned repo.

```sh
# On your server
./rdv serve -l ":8686"

# On client A
./rdv dial http://example.com:8686 123456  # Token is an arbitrary string, e.g. a UUID

# On client B
./rdv accept http://example.com:8686 123456  # Both clients need to provide the same token
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
