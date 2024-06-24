# Rdv: Relay-assisted p2p connectivity

```
./relayp2p -m d
./relayp2p -m a

B:accept  A:dial 

```

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
