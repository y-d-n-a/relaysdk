
# relaysdk

Go SDK for backends that communicate with the relay server over NATS.

The relay holds HTTP connections open and translates them into NATS messages.
Your backend never touches HTTP directly. It subscribes to NATS subjects,
receives structured message types, and writes responses or SSE events back
through a `Conn` handle.

---

## Architecture overview

```
Browser                 Relay (HTTP ↔ NATS)          Your Backend (this SDK)
  |                           |                               |
  |-- GET /sse/dashboard ----->|                               |
  |                           |-- ClientMessage{connected} --->|
  |                           |                               | HandleSSE handler called
  |                           |<-- SSEEvent{patch_elements} ---|  conn.PatchElements(html)
  |<-- SSE data frame ---------|                               |
  |                           |                               |
  |-- POST /sse/dashboard/msg->|                               |
  |                           |-- ClientMessage{action} ------>|
  |<-- 204 No Content ---------|                               | handler called again
  |                           |<-- SSEEvent{patch_elements} ---|  conn.PatchElements(html)
  |<-- SSE data frame ---------|                               |
  |                           |                               |
  |-- [tab closed] ----------->|                               |
  |                           |-- ClientMessage{disconnected}->|
  |                           |                               | handler called, ctx cancelled
```

The relay is the broker. It holds HTTP connections open and translates NATS
messages into SSE frames. Your backend never sees HTTP. The browser never
sees NATS.

---

## Installation

```bash
go get github.com/y-d-n-a/relaysdk
```

---

## Quick start

```go
package main

import (
    "context"
    "encoding/json"
    "log"

    "github.com/y-d-n-a/relaysdk"
)

func main() {
    client, err := relaysdk.New(relaysdk.Config{
        NatsURLs:    []string{"nats://127.0.0.1:4222"},
        WebsiteName: "mysite",
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    // Standard HTTP endpoint — publish one response and return.
    client.Handle("/api/login", func(ctx context.Context, req *relaysdk.Request, conn *relaysdk.Conn) {
        body, _ := json.Marshal(map[string]string{"status": "ok", "uid": req.UID})
        conn.Respond(200, map[string][]string{"Content-Type": {"application/json"}}, body)
    }, nil)

    // SSE page endpoint — one call handles connected, action, and disconnected.
    client.HandleSSE("/sse/dashboard", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
        switch msg.Type {
        case relaysdk.ClientMessageConnected:
            conn.PatchElements(`<div id="status">connected</div>`)
        case relaysdk.ClientMessageAction:
            switch msg.Action {
            case "refresh":
                conn.PatchElements(`<div id="data">fresh data</div>`)
            }
        case relaysdk.ClientMessageDisconnected:
            log.Printf("uid=%s disconnected", msg.UID)
        }
    })

    select {} // block; use signal handling in production
}
```

---

## Concepts

### Standard endpoints (`Handle`)

The browser sends an HTTP request (GET, POST, etc.) to a path registered in
the relay config with `is_sse: false`. The relay publishes a `Request` to
NATS. Your handler receives it, calls `conn.Respond(...)` exactly once, and
returns. The relay writes the HTTP response to the browser.

### SSE endpoints (`HandleSSE`)

The browser opens a persistent SSE connection to a path registered with
`is_sse: true`. The relay holds this connection open for its lifetime.

Three types of events arrive on a single `HandleSSE` subscription:

| `msg.Type`                   | When                                                  |
|------------------------------|-------------------------------------------------------|
| `ClientMessageConnected`     | Browser opened the SSE connection                     |
| `ClientMessageAction`        | Browser POSTed to `/sse/{page}/message`               |
| `ClientMessageDisconnected`  | Browser closed the tab or navigated away              |

The relay automatically injects `connUUID` into the browser's Datastar signal
store when the SSE connection opens. The browser includes it in every POST to
`/sse/{page}/message` so the relay can route the action to the correct
connection.

### Per-page isolation

Each SSE endpoint path (`/sse/dashboard`, `/sse/battle`, `/sse/inventory`) has
its own NATS subject namespace. A `HandleSSE("/sse/dashboard", ...)` call only
receives messages from users on the dashboard page — never from users on the
battle or inventory pages. NATS subject matching enforces this at the
infrastructure level; no path inspection is needed inside your handler.

### Context cancellation

The `context.Context` passed to the `ClientMessageConnected` handler invocation
is cancelled automatically when a `ClientMessageDisconnected` message arrives
for the same `conn_uuid`. Use `<-ctx.Done()` to block until disconnect or to
clean up resources.

---

## API reference

### `relaysdk.New(cfg Config) (*Client, error)`

Connects to NATS and returns a ready `Client`. Tries each URL in `NatsURLs` in
order and uses the first that succeeds. Reconnects automatically on drop.

```go
type Config struct {
    // NatsURLs is the list of NATS server URLs to try in order.
    // At least one is required.
    NatsURLs []string

    // WebsiteName must match the relay's website_name for this site.
    WebsiteName string
}
```

---

### `(*Client).Handle(path string, h Handler, onDisconnect DisconnectHandler) error`

Subscribes to all standard requests arriving on `path`. One call covers all
users (all UIDs) hitting that path.

- `h` is called in a new goroutine for every incoming request.
- `onDisconnect` is called when an SSE client that was initiated via `Handle`
  disconnects. Pass `nil` for standard endpoints or when you do not need the
  notification.

```go
// Handler is called for every incoming standard request.
// Call conn.Respond(...) exactly once and return.
type Handler func(ctx context.Context, req *Request, conn *Conn)

// DisconnectHandler is called when an SSE client disconnects (legacy Handle path).
type DisconnectHandler func(note *DisconnectNotification)
```

`Handle` returns an error only if the NATS subscription itself fails at
startup. Delivery errors after startup are logged internally.

---

### `(*Client).HandleSSE(path string, h SSEHandler) error`

Subscribes to all `ClientMessage` events for the given SSE path. The handler
is called for `connected`, `action`, and `disconnected` messages.

```go
// SSEHandler is called for every ClientMessage on the subscribed SSE path.
// msg.Type distinguishes lifecycle events from browser actions.
// msg.Action (populated only for ClientMessageAction) is a free-form string
// defined by the application, e.g. "login", "attack", "buy_item".
// The context passed to the connected invocation is cancelled when a
// disconnected message arrives for the same conn_uuid.
type SSEHandler func(ctx context.Context, msg *ClientMessage, conn *Conn)
```

`HandleSSE` returns an error only if the NATS subscription itself fails at
startup.

---

### `(*Client).Close()`

Drains all subscriptions and closes the NATS connection. Call this on
shutdown.

```go
sigs := make(chan os.Signal, 1)
signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
<-sigs
client.Close()
```

---

## Message types

### `Request` — received by `Handle` handlers

```go
type Request struct {
    UUID        string              // relay request UUID
    ConnUUID    string              // set only for SSE connections via Handle (legacy)
    Website     string              // website_name from relay config
    UID         string              // Firebase UID; empty when require_auth is false
    Method      string              // HTTP method
    Path        string              // HTTP path
    Headers     map[string][]string
    Body        json.RawMessage
    QueryParams map[string][]string
}
```

`req.IsSSE()` returns true when `ConnUUID` is non-empty (legacy SSE via
`Handle`).

---

### `ClientMessage` — received by `HandleSSE` handlers

```go
type ClientMessage struct {
    ConnUUID    string              // identifies the SSE connection
    MessageID   string              // unique ID for this message
    Type        ClientMessageType   // "connected" | "action" | "disconnected"
    Website     string              // website_name from relay config
    UID         string              // Firebase UID; empty when require_auth is false
    Path        string              // HTTP path of the SSE endpoint
    Method      string              // HTTP method (populated for connected only)
    Headers     map[string][]string // HTTP headers (populated for connected only)
    QueryParams map[string][]string // URL query params (populated for connected only)
    Action      string              // application-defined action name (action only)
    Payload     json.RawMessage     // arbitrary JSON from browser POST (action only)
}
```

```go
const (
    ClientMessageConnected    ClientMessageType = "connected"
    ClientMessageAction       ClientMessageType = "action"
    ClientMessageDisconnected ClientMessageType = "disconnected"
)
```

---

### `DisconnectNotification` — received by `Handle` disconnect handlers (legacy)

```go
type DisconnectNotification struct {
    ConnUUID string
    Website  string
    UID      string
    Path     string
}
```

---

## `Conn` — standard endpoint methods

### `conn.Respond(status int, headers map[string][]string, body json.RawMessage) error`

Publishes an HTTP response back to the relay. Call exactly once per standard
request. `body` must be valid JSON; pass `nil` to send `null`.

```go
body, _ := json.Marshal(map[string]string{"token": "abc"})
conn.Respond(200, map[string][]string{"Content-Type": {"application/json"}}, body)
```

### `conn.RespondError(status int, errMsg string) error`

Shorthand for error responses. The relay writes `errMsg` as the HTTP response
body with `status` as the status code.

```go
conn.RespondError(401, "unauthorized")
```

---

## `Conn` — SSE push methods

All methods publish a Datastar event to the relay, which writes it to the open
SSE connection. These work identically whether called from a `Handle` handler
or a `HandleSSE` handler.

### `conn.PatchElements(html string) error`

Merges an HTML fragment into the browser DOM using Datastar's morphing
algorithm.

```go
conn.PatchElements(`<div id="score">42</div>`)
```

### `conn.RemoveElement(selector string) error`

Removes the DOM element matching `selector`.

```go
conn.RemoveElement("#score")
```

### `conn.PatchSignals(signals map[string]any) error`

Merges key-value pairs into the Datastar signal store on the browser.

```go
conn.PatchSignals(map[string]any{"count": 5, "active": true})
```

### `conn.ExecuteScript(script string) error`

Runs a JavaScript snippet in the browser.

```go
conn.ExecuteScript("console.log('hello from backend')")
```

### `conn.Redirect(url string) error`

Navigates the browser to `url`.

```go
conn.Redirect("/login")
```

---

## `Conn` — SSE push to explicit connection (legacy, advanced)

These methods push to a specific `conn_uuid` rather than the one associated
with the current handler invocation. Useful when one user's action should
trigger an update on another user's open connection (e.g. multiplayer).

```go
conn.PatchElementsTo(uid, connUUID, html)
conn.RemoveElementTo(uid, connUUID, selector)
conn.PatchSignalsTo(uid, connUUID, signals)
conn.ExecuteScriptTo(uid, connUUID, script)
conn.RedirectTo(uid, connUUID, url)
```

---

## Patterns

### Sending initial state on connect

```go
client.HandleSSE("/sse/dashboard", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    switch msg.Type {
    case relaysdk.ClientMessageConnected:
        stats := fetchStats(msg.UID)
        conn.PatchElements(renderStats(stats))
    }
})
```

### Dispatching browser actions

```go
client.HandleSSE("/sse/battle", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    switch msg.Type {
    case relaysdk.ClientMessageAction:
        switch msg.Action {
        case "attack":
            var p struct {
                TargetID string `json:"targetId"`
            }
            json.Unmarshal(msg.Payload, &p)
            result := processAttack(msg.UID, p.TargetID)
            conn.PatchElements(renderBattleResult(result))

        case "retreat":
            conn.Redirect("/sse/dashboard")
        }
    }
})
```

### Blocking until disconnect

```go
client.HandleSSE("/sse/live", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    switch msg.Type {
    case relaysdk.ClientMessageConnected:
        markOnline(msg.UID)
        <-ctx.Done() // blocks until browser closes the connection
        markOffline(msg.UID)
    }
})
```

### Sending periodic updates

```go
client.HandleSSE("/sse/ticker", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    if msg.Type != relaysdk.ClientMessageConnected {
        return
    }
    ticker := time.NewTicker(time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case t := <-ticker.C:
            conn.PatchElements(fmt.Sprintf(`<span id="time">%s</span>`, t.Format(time.RFC3339)))
        }
    }
})
```

### Tracking active connections

```go
var active sync.Map // conn_uuid → *relaysdk.Conn

client.HandleSSE("/sse/live", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    switch msg.Type {
    case relaysdk.ClientMessageConnected:
        active.Store(msg.ConnUUID, conn)
    case relaysdk.ClientMessageDisconnected:
        active.Delete(msg.ConnUUID)
    }
})
```

### Standard endpoint with query params and body

```go
client.Handle("/api/search", func(ctx context.Context, req *relaysdk.Request, conn *relaysdk.Conn) {
    q := req.QueryParams["q"]
    var payload map[string]any
    json.Unmarshal(req.Body, &payload)

    result, _ := json.Marshal(map[string]any{"query": q, "payload": payload})
    conn.Respond(200, map[string][]string{"Content-Type": {"application/json"}}, result)
}, nil)
```

### Standard endpoint error response

```go
client.Handle("/api/protected", func(ctx context.Context, req *relaysdk.Request, conn *relaysdk.Conn) {
    if req.UID == "" {
        conn.RespondError(401, "unauthorized")
        return
    }
    body, _ := json.Marshal(map[string]string{"uid": req.UID})
    conn.Respond(200, nil, body)
}, nil)
```

---

## Frontend integration

The relay injects `connUUID` into the Datastar signal store when the SSE
connection opens. Every page form includes it in its POST body automatically
via `data-bind="connUUID"`.

```html
<!-- dashboard.html -->
<div data-signals="{connUUID: ''}">
  <div data-on-load="@get('/sse/dashboard')"></div>
</div>

<!-- Action form — connUUID is bound automatically from the signal store -->
<form data-on-submit="@post('/sse/dashboard/message')">
  <input type="hidden" data-bind="connUUID">
  <input type="hidden" name="action" value="refresh">
  <button type="submit">Refresh</button>
</form>

<!-- Action with payload -->
<form data-on-submit="@post('/sse/battle/message')">
  <input type="hidden" data-bind="connUUID">
  <input type="hidden" name="action" value="attack">
  <input data-bind="targetId" placeholder="Target ID">
  <button type="submit">Attack</button>
</form>
```

Each page posts to its own message endpoint (`/sse/{page}/message`). The relay
validates that the `conn_uuid` was opened on the matching SSE path and rejects
cross-page reuse with `410 Gone`.

---

## NATS subject layout

The SDK constructs subjects that mirror the relay's internal layout. You do not
need to build subjects manually, but the pattern is documented here for
debugging.

### Standard endpoints

| Direction           | Subject pattern                                              |
|---------------------|--------------------------------------------------------------|
| Relay → backend     | `{website}.{uid}.{path_segments}.request.{uuid}`            |
| Backend → relay     | `{website}.{uid}.{path_segments}.response.{uuid}`           |

### SSE endpoints

| Direction           | Subject pattern                                              |
|---------------------|--------------------------------------------------------------|
| Relay → backend     | `{website}.{uid}.{path_segments}.{conn_uuid}.message`       |
| Backend → relay     | `{website}.{uid}.sse.{conn_uuid}.event`                     |

When `uid` is empty (unauthenticated endpoint) the `{uid}` segment is omitted.

### SDK subscription wildcards

For `HandleSSE("/sse/dashboard", ...)` the SDK creates two subscriptions:

```
battlefrontier.*.sse.dashboard.*.message   ← authenticated (uid present)
battlefrontier.sse.dashboard.*.message     ← unauthenticated (uid absent)
```

These match only dashboard connections. Battle page connections arrive on
different subjects and are never delivered to the dashboard handler.

---

## Error handling

All `Conn` methods return an `error`. NATS publish errors are uncommon in
normal operation but should be logged in production.

```go
if err := conn.PatchElements(html); err != nil {
    log.Printf("patch failed: %v", err)
}
```

`Handle` and `HandleSSE` return an error only if the NATS subscription itself
fails (connection down at startup). After startup, message delivery errors are
logged internally and do not surface to the handler.

---

## Relay configuration reference

Each SSE page endpoint requires an entry in the relay's website config with
`is_sse: true`. The relay automatically registers a companion POST endpoint at
`{path}/message` — no additional config is needed.

```json
{
  "website_name": "mysite",
  "request_timeout_seconds": 30,
  "firebase": {
    "credentials_file": "config/firebase-configs/mysite-key.json"
  },
  "endpoints": [
    { "path": "/sse/dashboard", "allowed_methods": ["GET"], "require_auth": true,  "is_sse": true },
    { "path": "/sse/battle",    "allowed_methods": ["GET"], "require_auth": true,  "is_sse": true },
    { "path": "/sse/login",     "allowed_methods": ["GET"], "require_auth": false, "is_sse": true },
    { "path": "/api/search",    "allowed_methods": ["POST"],"require_auth": true,  "is_sse": false }
  ]
}
```

| Field             | Description                                                       |
|-------------------|-------------------------------------------------------------------|
| `path`            | HTTP path, e.g. `"/sse/dashboard"`                               |
| `allowed_methods` | HTTP methods accepted. Empty = all methods.                       |
| `require_auth`    | Enforce Firebase JWT. Sets `msg.UID` when true.                   |
| `is_sse`          | `true` → SSE endpoint. Use `HandleSSE`. `false` → use `Handle`.  |
| `timeout_seconds` | Per-endpoint timeout override for standard endpoints. 0 = default.|
