
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
  |-- GET /v0/sse/dashboard -->|                               |
  |                           |-- ClientMessage{connected} --->|
  |                           |                               | HandleSSE handler called
  |                           |<-- SSEEvent{patch_elements} ---|  conn.PatchElements(html)
  |<-- SSE data frame ---------|                               |
  |                           |                               |
  |-- POST /v0/sse/dashboard/message ->|                       |
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

    // Publish website config so the relay picks up endpoints automatically.
    client.PublishWebsiteConfig(relaysdk.WebsiteConfig{
        WebsiteName: "mysite",
        ApexDomain:  "mysite.io",
        RequestTimeoutSeconds: 30,
        Endpoints: []relaysdk.EndpointConfig{
            {Path: "/v0/api/login",      AllowedMethods: []string{"POST"}, RequireAuth: false, IsSSE: false},
            {Path: "/v0/sse/dashboard",  AllowedMethods: []string{"GET"},  RequireAuth: true,  IsSSE: true},
        },
    })

    // Standard HTTP endpoint — publish one response and return.
    client.Handle("/v0/api/login", func(ctx context.Context, req *relaysdk.Request, conn *relaysdk.Conn) {
        body, _ := json.Marshal(map[string]string{"status": "ok", "uid": req.UID})
        conn.Respond(200, map[string][]string{"Content-Type": {"application/json"}}, body)
    }, nil)

    // SSE page endpoint — one handler covers connected, action, and disconnected.
    client.HandleSSE("/v0/sse/dashboard", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
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
| `ClientMessageAction`        | Browser POSTed to `/{path}/message`                   |
| `ClientMessageDisconnected`  | Browser closed the tab or navigated away              |

The relay automatically injects `connUUID` into the browser's Datastar signal
store when the SSE connection opens. The browser includes it in every POST to
`/{path}/message` so the relay can route the action to the correct connection.

### Per-page isolation

Each SSE endpoint path (`/v0/sse/dashboard`, `/v0/sse/battle`, `/v0/sse/inventory`)
has its own NATS subject namespace. A `HandleSSE("/v0/sse/dashboard", ...)` call
only receives messages from users on the dashboard page — never from users on
the battle or inventory pages. NATS subject matching enforces this at the
infrastructure level; no path inspection is needed inside your handler.

### Context cancellation

The `context.Context` passed to the `ClientMessageConnected` handler invocation
is cancelled automatically when a `ClientMessageDisconnected` message arrives
for the same `conn_uuid`. Use `<-ctx.Done()` to block until disconnect or to
clean up resources.

### Datastar signals

The relay extracts Datastar signal store values from every incoming request and
includes them in `Request.Signals` and `ClientMessage.Signals`. For GET requests
signals come from the `?datastar=` query parameter. For POST/PUT/PATCH/DELETE
signals come from the JSON body. Your handler can read them directly without
parsing the raw body or query string.

---

## SDK function reference

This section documents every callable function and method available to the
backend. These are the only entry points your backend code needs.

---

### `relaysdk.New(cfg Config) (*Client, error)`

**Purpose:** Creates a new SDK client, connects to NATS, and initialises a
JetStream context. This is the first function you call. Returns a `*Client`
that is used for all subsequent operations.

Tries each URL in `NatsURLs` in order and uses the first that succeeds.
Reconnects automatically on drop.

```go
type Config struct {
    NatsURLs    []string // at least one required
    WebsiteName string   // must match relay's website_name
}
```

```go
client, err := relaysdk.New(relaysdk.Config{
    NatsURLs:    []string{"nats://192.168.2.110:4222", "nats://192.168.2.108:4222"},
    WebsiteName: "mysite",
})
```

---

### `(*Client).PublishWebsiteConfig(cfg WebsiteConfig) error`

**Purpose:** Publishes the website's endpoint configuration to the NATS KV
store bucket `relay-website-configs`. The relay watches this bucket and
hot-reloads its endpoint routing whenever a key is created or updated.

Call this **once on backend startup** (and whenever your endpoint config
changes). The relay will automatically register all listed endpoints and begin
routing HTTP traffic to your NATS handlers. You do not need to manually
configure the relay.

The KV bucket is created automatically if it does not exist.

```go
type WebsiteConfig struct {
    WebsiteName           string           // must match relay's website_name
    ApexDomain            string           // e.g. "battlefrontier.io"
    RequestTimeoutSeconds int              // default timeout for standard endpoints (seconds). 0=30s, -1=none
    Endpoints             []EndpointConfig
}

type EndpointConfig struct {
    Path                string   // HTTP path, must start with /v{N}/
    AllowedMethods      []string // e.g. ["GET","POST"]. Empty = all methods.
    RequireAuth         bool     // enforce session auth; sets UID on requests when true
    NatsSubjectOverride string   // override the auto-generated NATS subject base
    TimeoutSeconds      int      // per-endpoint timeout. 0=server default, -1=none, >0=seconds
    IsSSE               bool     // true = SSE endpoint (use HandleSSE). false = standard (use Handle).
}
```

```go
err := client.PublishWebsiteConfig(relaysdk.WebsiteConfig{
    WebsiteName: "mysite",
    ApexDomain:  "mysite.io",
    RequestTimeoutSeconds: 30,
    Endpoints: []relaysdk.EndpointConfig{
        {Path: "/v0/api/login",     AllowedMethods: []string{"POST"}, RequireAuth: false, IsSSE: false},
        {Path: "/v0/sse/dashboard", AllowedMethods: []string{"GET"},  RequireAuth: true,  IsSSE: true, TimeoutSeconds: -1},
    },
})
```

---

### `(*Client).Handle(path string, h Handler, onDisconnect DisconnectHandler) error`

**Purpose:** Subscribes to all standard (non-SSE) HTTP requests arriving on
`path`. One call covers all users (all UIDs) hitting that path. The handler
is called in a new goroutine for every incoming request.

Use this for normal request/response endpoints: login, API calls, form
submissions, etc. Your handler must call `conn.Respond(...)` or
`conn.RespondError(...)` exactly once before returning.

`onDisconnect` is a legacy parameter for the old SSE-via-Handle model. Pass
`nil` for all standard endpoints.

Returns an error only if the NATS subscription fails at startup.

```go
// Handler signature
type Handler func(ctx context.Context, req *Request, conn *Conn)

// DisconnectHandler signature (pass nil for standard endpoints)
type DisconnectHandler func(note *DisconnectNotification)
```

```go
client.Handle("/v0/api/login", func(ctx context.Context, req *relaysdk.Request, conn *relaysdk.Conn) {
    // req contains the full HTTP request details
    body, _ := json.Marshal(map[string]string{"uid": req.UID})
    conn.Respond(200, map[string][]string{"Content-Type": {"application/json"}}, body)
}, nil)
```

---

### `(*Client).HandleSSE(path string, h SSEHandler) error`

**Purpose:** Subscribes to all `ClientMessage` lifecycle events for the given
SSE endpoint path. The handler is called for `connected`, `action`, and
`disconnected` messages — all three arrive on the same subscription.

Use this for every SSE page endpoint in your app. The handler is your main
event loop for a browser page: it receives the initial connection, handles
browser-initiated actions (button clicks, form submissions, etc.), and is
notified when the user closes the tab.

The `context.Context` passed to the `connected` invocation is automatically
cancelled when the corresponding `disconnected` message arrives. This lets you
use `<-ctx.Done()` inside the connected handler to block until the user leaves.

Returns an error only if the NATS subscription fails at startup.

```go
// SSEHandler signature
type SSEHandler func(ctx context.Context, msg *ClientMessage, conn *Conn)
```

```go
client.HandleSSE("/v0/sse/dashboard", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    switch msg.Type {
    case relaysdk.ClientMessageConnected:
        conn.PatchElements(`<div id="status">online</div>`)
    case relaysdk.ClientMessageAction:
        // msg.Action is the free-form action name sent by the browser
        // msg.Payload is the arbitrary JSON payload from the browser POST
        // msg.Signals contains the Datastar signal store values
    case relaysdk.ClientMessageDisconnected:
        // clean up resources for this connection
    }
})
```

---

### `(*Client).Close()`

**Purpose:** Drains all active NATS subscriptions and closes the NATS
connection. Call this on graceful shutdown to ensure all in-flight messages
are processed before the process exits.

```go
sigs := make(chan os.Signal, 1)
signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
<-sigs
client.Close()
```

---

## `Conn` method reference

A `*Conn` is passed to every `Handler` and `SSEHandler` invocation. It is the
handle through which your backend sends data back to the browser.

---

### Standard endpoint methods

#### `conn.Respond(status int, headers map[string][]string, body json.RawMessage) error`

**Purpose:** Publishes an HTTP response back to the relay for a standard
endpoint. The relay writes this response directly to the waiting browser HTTP
connection. Call exactly once per standard request handler invocation. Pass
`nil` for `body` to send a `null` JSON body.

```go
body, _ := json.Marshal(map[string]string{"token": "abc123"})
conn.Respond(200, map[string][]string{"Content-Type": {"application/json"}}, body)
```

#### `conn.RespondError(status int, errMsg string) error`

**Purpose:** Shorthand for publishing an error response for a standard
endpoint. The relay writes `errMsg` as the HTTP response body with `status`
as the HTTP status code. Use this instead of `Respond` when you need to return
an error.

```go
conn.RespondError(401, "unauthorized: invalid credentials")
conn.RespondError(400, "missing required field: email")
conn.RespondError(500, "internal server error")
```

---

### SSE push methods

All SSE push methods publish a Datastar event to the relay, which immediately
writes it as an SSE frame to the open browser connection. These work identically
whether called from a `Handle` handler or a `HandleSSE` handler.

#### `conn.PatchElements(html string) error`

**Purpose:** Merges an HTML fragment into the browser DOM using Datastar's
morphing algorithm. The `html` string must contain one or more elements with
`id` attributes. Datastar matches them against existing DOM elements and
updates only what changed.

Use this to update any part of the page: counters, lists, status indicators,
game state, etc.

```go
conn.PatchElements(`<div id="score">42</div>`)
conn.PatchElements(`<ul id="items"><li>item 1</li><li>item 2</li></ul>`)
```

#### `conn.RemoveElement(selector string) error`

**Purpose:** Removes the DOM element matching the CSS `selector` from the
browser page. Use this to delete UI elements that should no longer be visible.

```go
conn.RemoveElement("#notification-banner")
conn.RemoveElement(".loading-spinner")
```

#### `conn.PatchSignals(signals map[string]any) error`

**Purpose:** Merges key-value pairs into the Datastar signal store on the
browser. The browser's reactive system automatically updates any DOM elements
bound to these signals. Use this to update client-side state without sending
HTML.

```go
conn.PatchSignals(map[string]any{
    "playerHealth": 85,
    "isOnline":     true,
    "username":     "Alice",
})
```

#### `conn.ExecuteScript(script string) error`

**Purpose:** Runs a JavaScript snippet in the browser in the context of the
open SSE connection. Use sparingly for cases where DOM patching or signal
updates are insufficient.

```go
conn.ExecuteScript("document.title = 'New Notification'")
conn.ExecuteScript("window.scrollTo(0, 0)")
```

#### `conn.Redirect(url string) error`

**Purpose:** Navigates the browser to `url`. The relay sends a Datastar
redirect event which causes the browser to perform a client-side navigation.
Use this to send the user to a different page (e.g. after logout, or when
access is revoked).

```go
conn.Redirect("/v0/sse/login")
conn.Redirect("/maintenance")
```

---

### SSE push to explicit connection ("To" variants)

These methods push to a specific `conn_uuid` rather than the connection
associated with the current handler invocation. Use them when one user's action
should trigger an update on a different user's open connection (e.g.
multiplayer games, admin notifications, chat).

#### `conn.PatchElementsTo(uid, connUUID, html string) error`

**Purpose:** Sends an HTML patch to a specific SSE connection identified by
`uid` and `connUUID`. Use when you need to update another user's page.

```go
// Push a score update to a specific opponent's connection
conn.PatchElementsTo(opponentUID, opponentConnUUID, `<div id="score">opponent: 10</div>`)
```

#### `conn.RemoveElementTo(uid, connUUID, selector string) error`

**Purpose:** Removes a DOM element on a specific SSE connection.

```go
conn.RemoveElementTo(targetUID, targetConnUUID, "#pending-invite")
```

#### `conn.PatchSignalsTo(uid, connUUID string, signals map[string]any) error`

**Purpose:** Merges signals into the Datastar store on a specific SSE
connection.

```go
conn.PatchSignalsTo(targetUID, targetConnUUID, map[string]any{"challenged": true})
```

#### `conn.ExecuteScriptTo(uid, connUUID, script string) error`

**Purpose:** Runs a JavaScript snippet on a specific SSE connection.

```go
conn.ExecuteScriptTo(targetUID, targetConnUUID, "playNotificationSound()")
```

#### `conn.RedirectTo(uid, connUUID, url string) error`

**Purpose:** Navigates a specific SSE connection to a new URL.

```go
conn.RedirectTo(targetUID, targetConnUUID, "/v0/sse/battle")
```

---

## Signal helper methods

Both `*Request` and `*ClientMessage` expose typed helper methods for reading
Datastar signal values without manual type assertions.

### On `*Request` (standard endpoints)

#### `req.Signal(key string) any`
Returns the raw signal value for `key`, or `nil` if not present.

#### `req.SignalString(key string) string`
Returns the signal value as a `string`. Returns `""` if absent or not a string.

#### `req.SignalFloat64(key string) float64`
Returns the signal value as a `float64`. JSON numbers unmarshal as float64.
Returns `0` if absent or wrong type.

#### `req.SignalBool(key string) bool`
Returns the signal value as a `bool`. Returns `false` if absent or wrong type.

#### `req.IsSSE() bool`
Returns `true` when this request was made on an SSE connection (legacy Handle
path). Returns `false` for all standard request/response endpoints.

### On `*ClientMessage` (SSE endpoints)

#### `msg.Signal(key string) any`
Returns the raw signal value for `key`, or `nil` if not present.

#### `msg.SignalString(key string) string`
Returns the signal value as a `string`. Returns `""` if absent or not a string.

#### `msg.SignalFloat64(key string) float64`
Returns the signal value as a `float64`. Returns `0` if absent or wrong type.

#### `msg.SignalBool(key string) bool`
Returns the signal value as a `bool`. Returns `false` if absent or wrong type.

---

## Message types

### `Request` — received by `Handle` handlers

```go
type Request struct {
    UUID        string              // relay-assigned request UUID
    ConnUUID    string              // non-empty only for legacy SSE-via-Handle
    Website     string              // website_name from relay config
    UID         string              // session UID; empty when require_auth is false
    Method      string              // HTTP method, e.g. "POST"
    Path        string              // full HTTP path, e.g. "/v0/api/login"
    Headers     map[string][]string // HTTP request headers
    Body        json.RawMessage     // raw JSON request body
    QueryParams map[string][]string // URL query parameters
    Signals     map[string]any      // Datastar signal store values from the request
}
```

### `ClientMessage` — received by `HandleSSE` handlers

```go
type ClientMessage struct {
    ConnUUID    string              // identifies the SSE connection (stable for its lifetime)
    MessageID   string              // unique ID for this specific message
    Type        ClientMessageType   // "connected" | "action" | "disconnected"
    Website     string              // website_name from relay config
    UID         string              // session UID; empty when require_auth is false
    Path        string              // full HTTP path of the SSE endpoint
    Method      string              // HTTP method (populated for connected only)
    Headers     map[string][]string // HTTP headers (populated for connected only)
    QueryParams map[string][]string // URL query params (populated for connected only)
    Action      string              // app-defined action name (populated for action only)
    Payload     json.RawMessage     // arbitrary JSON from browser POST (populated for action only)
    Signals     map[string]any      // Datastar signal store values (connected and action)
}
```

```go
const (
    ClientMessageConnected    ClientMessageType = "connected"
    ClientMessageAction       ClientMessageType = "action"
    ClientMessageDisconnected ClientMessageType = "disconnected"
)
```

### `WebsiteConfig` — passed to `PublishWebsiteConfig`

```go
type WebsiteConfig struct {
    WebsiteName           string           // must match relay's website_name
    ApexDomain            string           // e.g. "battlefrontier.io"
    RequestTimeoutSeconds int              // default timeout in seconds. 0=30s, -1=none
    Endpoints             []EndpointConfig
}
```

### `EndpointConfig` — used inside `WebsiteConfig`

```go
type EndpointConfig struct {
    Path                string   // HTTP path, must start with /v{N}/
    AllowedMethods      []string // e.g. ["GET","POST"]. Empty = all methods.
    RequireAuth         bool     // enforce session auth
    NatsSubjectOverride string   // override auto-generated NATS subject base
    TimeoutSeconds      int      // 0=server default, -1=none, >0=seconds
    IsSSE               bool     // true=SSE endpoint, false=standard endpoint
}
```

---

## Patterns

### Sending initial state on connect

```go
client.HandleSSE("/v0/sse/dashboard", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    switch msg.Type {
    case relaysdk.ClientMessageConnected:
        stats := fetchStats(msg.UID)
        conn.PatchElements(renderStats(stats))
    }
})
```

### Dispatching browser actions

```go
client.HandleSSE("/v0/sse/battle", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
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
            conn.Redirect("/v0/sse/dashboard")
        }
    }
})
```

### Reading signals from an action

```go
client.HandleSSE("/v0/sse/shop", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    if msg.Type == relaysdk.ClientMessageAction && msg.Action == "buy" {
        itemID := msg.SignalString("selectedItemId")
        quantity := int(msg.SignalFloat64("quantity"))
        // process purchase...
    }
})
```

### Blocking until disconnect

```go
client.HandleSSE("/v0/sse/live", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
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
client.HandleSSE("/v0/sse/ticker", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
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

client.HandleSSE("/v0/sse/live", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    switch msg.Type {
    case relaysdk.ClientMessageConnected:
        active.Store(msg.ConnUUID, conn)
    case relaysdk.ClientMessageDisconnected:
        active.Delete(msg.ConnUUID)
    }
})
```

### Standard endpoint with signals

```go
client.Handle("/v0/api/search", func(ctx context.Context, req *relaysdk.Request, conn *relaysdk.Conn) {
    query := req.SignalString("searchQuery")
    page  := int(req.SignalFloat64("page"))

    results := performSearch(query, page)
    body, _ := json.Marshal(results)
    conn.Respond(200, map[string][]string{"Content-Type": {"application/json"}}, body)
}, nil)
```

### Standard endpoint error response

```go
client.Handle("/v0/api/protected", func(ctx context.Context, req *relaysdk.Request, conn *relaysdk.Conn) {
    if req.UID == "" {
        conn.RespondError(401, "unauthorized")
        return
    }
    body, _ := json.Marshal(map[string]string{"uid": req.UID})
    conn.Respond(200, nil, body)
}, nil)
```

### Pushing to another user's connection

```go
client.HandleSSE("/v0/sse/battle", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    if msg.Type == relaysdk.ClientMessageAction && msg.Action == "challenge" {
        opponentUID     := msg.SignalString("opponentUID")
        opponentConnUID := lookupActiveConn(opponentUID)
        conn.PatchElementsTo(opponentUID, opponentConnUID,
            `<div id="challenge-banner">You have been challenged!</div>`)
    }
})
```

---

## Frontend integration

The relay injects `connUUID` into the Datastar signal store when the SSE
connection opens. Every page form includes it in its POST body automatically
via `data-bind="connUUID"`.

```html
<!-- dashboard.html -->
<div data-signals="{connUUID: ''}">
  <div data-on-load="@get('/v0/sse/dashboard')"></div>
</div>

<!-- Action form — connUUID is bound automatically from the signal store -->
<form data-on-submit="@post('/v0/sse/dashboard/message')">
  <input type="hidden" data-bind="connUUID">
  <input type="hidden" name="action" value="refresh">
  <button type="submit">Refresh</button>
</form>

<!-- Action with payload -->
<form data-on-submit="@post('/v0/sse/battle/message')">
  <input type="hidden" data-bind="connUUID">
  <input type="hidden" name="action" value="attack">
  <input data-bind="targetId" placeholder="Target ID">
  <button type="submit">Attack</button>
</form>
```

Each page posts to its own message endpoint (`/{path}/message`). The relay
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

For `HandleSSE("/v0/sse/dashboard", ...)` the SDK creates two subscriptions:

```
{website}.*.v0.sse.dashboard.*.message   ← authenticated (uid present)
{website}.v0.sse.dashboard.*.message     ← unauthenticated (uid absent)
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
  "apex_domain": "mysite.io",
  "request_timeout_seconds": 30,
  "endpoints": [
    { "path": "/v0/sse/dashboard", "allowed_methods": ["GET"], "require_auth": true,  "is_sse": true,  "timeout_seconds": -1 },
    { "path": "/v0/sse/battle",    "allowed_methods": ["GET"], "require_auth": true,  "is_sse": true,  "timeout_seconds": -1 },
    { "path": "/v0/sse/login",     "allowed_methods": ["GET"], "require_auth": false, "is_sse": true,  "timeout_seconds": -1 },
    { "path": "/v0/api/login",     "allowed_methods": ["POST"],"require_auth": false, "is_sse": false, "timeout_seconds": 10 }
  ]
}
```

| Field             | Description                                                        |
|-------------------|--------------------------------------------------------------------|
| `path`            | HTTP path, must start with `/v{N}/`, e.g. `"/v0/sse/dashboard"`   |
| `allowed_methods` | HTTP methods accepted. Empty = all methods.                        |
| `require_auth`    | Enforce session auth. Sets `UID` on requests and messages.         |
| `is_sse`          | `true` → SSE endpoint, use `HandleSSE`. `false` → use `Handle`.   |
| `timeout_seconds` | Per-endpoint timeout. `0`=server default, `-1`=none, `>0`=seconds. SSE endpoints should always be `-1`. |
