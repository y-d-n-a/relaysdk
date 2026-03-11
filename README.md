
# relaysdk

Go SDK for backends that communicate with the relay server over NATS.

The relay holds HTTP connections open and translates them into NATS messages.
Your backend never touches HTTP directly. It subscribes to NATS subjects,
receives structured message types, and pushes Datastar SSE events back to the
browser through a `Conn` handle.

The relay is a pure HTTP ↔ NATS broker — it does no business logic. All request
processing, validation, and response generation is the backend's responsibility.

---

## Architecture overview

```
Browser                 Relay (HTTP ↔ NATS)          Your Backend (this SDK)
  |                           |                               |
  |-- POST /v0/api/login ----->|                               |
  |                           | ── validates, publishes ───>  |
  |<-- HTTP 200 (published) ---|                               | RelayRequest received
  |                           |                               | backend processes...
  |                           |<── SSEEvent{patch_signals} ───| conn.PatchSignals(...)
  |<-- SSE frame (on open conn)|                               |
  |                           |                               |
  |-- POST /v0/api/login (bad auth) ->|                        |
  |<-- HTTP 401 (rejected) ----|                               | (never published)
  |                           |                               |
  |-- GET /v0/sse/dashboard -->|                               |
  |                           |── ClientMessage{connected} ──>|
  |                           |                               | HandleSSE handler called
  |                           |<── SSEEvent{patch_elements} ──| conn.PatchElements(html)
  |<-- SSE data frame ---------|                               |
  |                           |                               |
  |-- POST /v0/sse/dashboard/message ->|                       |
  |                           |── ClientMessage{action} ─────>|
  |<-- HTTP 200 (published) ---|                               | handler called again
  |                           |<── SSEEvent{patch_elements} ──| conn.PatchElements(html)
  |<-- SSE data frame ---------|                               |
  |                           |                               |
  |-- [tab closed] ----------->|                               |
  |                           |── ClientMessage{disconnected}>|
  |                           |                               | handler called, ctx cancelled
```

---

## How the relay responds to HTTP requests

The relay is the **sole entity** that sends HTTP responses to the browser. Your
backend never writes an HTTP response directly.

### Standard endpoints (non-SSE)

When the browser makes a request to a standard endpoint the relay either:

- **Publishes to NATS and immediately returns `HTTP 200`** — the request passed
  all relay-level checks (CORS, auth, endpoint exists, method allowed). Your
  backend receives the `RelayRequest` and processes it asynchronously.
- **Returns an HTTP error without publishing** — the relay rejected the request
  before it ever reached NATS. Possible rejection reasons:

| Status | Reason                                                        |
|--------|---------------------------------------------------------------|
| `400`  | Bad request (e.g. missing `connUUID` signal)                  |
| `401`  | Authentication required but no valid session token provided   |
| `403`  | CORS: request origin is not allowed                           |
| `404`  | No endpoint registered for this path                         |
| `405`  | HTTP method not in `allowed_methods` for this endpoint        |
| `410`  | SSE companion POST: `conn_uuid` not found or expired          |
| `500`  | Internal relay error (NATS publish failed, etc.)              |

### SSE companion endpoints (`POST /{ssePath}/message`)

Same rules apply. The relay returns `HTTP 200` if the action was published to
NATS, or an error code if it was rejected.

### SSE connection endpoints (`GET /sse/*`)

The relay holds the HTTP connection open for its entire lifetime. It never
sends a terminal HTTP response — the connection stays open until the browser
closes it or the server shuts down.

### Backend responses always travel over SSE

Once the relay publishes a request to NATS and returns `HTTP 200`, all data
your backend sends back to the browser travels exclusively over the browser's
**open SSE connection**. Use `conn.PatchElements`, `conn.PatchSignals`, etc.
to push data to the browser. There is no mechanism for the backend to send a
direct HTTP response.

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
        WebsiteName:           "mysite",
        ApexDomain:            "mysite.io",
        RequestTimeoutSeconds: 30,
        Endpoints: []relaysdk.EndpointConfig{
            {Path: "/v0/api/login",     AllowedMethods: []string{"POST"}, RequireAuth: false, IsSSE: false, TimeoutSeconds: 10},
            {Path: "/v0/sse/dashboard", AllowedMethods: []string{"GET"},  RequireAuth: true,  IsSSE: true,  TimeoutSeconds: -1},
        },
    })

    // Standard HTTP endpoint — the relay already returned HTTP 200 to the
    // browser. Push the result back via the open SSE connection.
    client.Handle("/v0/api/login", func(ctx context.Context, req *relaysdk.Request, conn *relaysdk.Conn) {
        // Process login...
        conn.PatchSignals(map[string]any{"uid": req.UID, "loggedIn": true})
        conn.PatchElements(`<div id="status">Welcome!</div>`)
    })

    // SSE page endpoint — one handler covers connected, action, and disconnected.
    client.HandleSSE("/v0/sse/dashboard", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
        switch msg.Type {
        case relaysdk.ClientMessageConnected:
            conn.PatchElements(`<div id="status">connected</div>`)
        case relaysdk.ClientMessageAction:
            conn.PatchElements(`<div id="data">fresh data</div>`)
        case relaysdk.ClientMessageDisconnected:
            log.Printf("uid=%s disconnected", msg.UID)
        }
    })

    select {} // block; use signal handling in production
}
```

---

## Concepts

### How the relay and backend communicate

The relay is a pure HTTP ↔ NATS bridge. When the browser makes an HTTP request:

1. The relay validates the request (CORS, auth, endpoint registration, method).
2. If validation fails the relay immediately returns an HTTP error (`401`, `403`,
   `404`, `405`, etc.) and the backend never sees the request.
3. If validation passes the relay publishes a structured message to NATS via
   JetStream and immediately returns `HTTP 200` to the browser.
4. Your backend receives the message, does the actual work, and pushes results
   back to the browser over the open SSE connection.

Your backend never sees `*http.Request`. The relay never sees your business
logic. They communicate entirely through NATS messages.

### Standard endpoints (`Handle`)

The browser sends an HTTP request (GET, POST, etc.) to a path registered with
`is_sse: false`. The relay validates the request and either rejects it with an
HTTP error or publishes a `RelayRequest` to NATS and returns `HTTP 200`.

Your handler receives the `RelayRequest` and pushes results to the browser via
`conn.PatchElements`, `conn.PatchSignals`, etc. on the browser's open SSE
connection. The `HTTP 200` the browser already received only means "your request
was accepted and is being processed" — the actual data always arrives over SSE.

Standard endpoint requests include `conn_uuid` — the ID of the browser's open
SSE connection. This is how your backend knows which SSE connection to push
events to. If `conn_uuid` is missing or the connection is not found, the relay
rejects the request with `HTTP 400` or `HTTP 410` before it reaches your handler.

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

### What the relay publishes to your backend

For standard endpoints the relay publishes a `RelayRequest` containing:

- `uuid` — unique request ID for correlation
- `conn_uuid` — the browser's open SSE connection ID (extracted from signals)
- `website`, `uid`, `method`, `path` — request metadata
- `headers`, `body`, `query_params` — raw HTTP request data
- `signals` — Datastar signal store values (extracted from query string for GET,
  from JSON body for all other methods)

For SSE endpoints the relay publishes a `ClientMessage` containing:

- `conn_uuid` — stable identifier for this SSE connection's lifetime
- `type` — `"connected"`, `"action"`, or `"disconnected"`
- `website`, `uid`, `path` — connection metadata
- `signals` — Datastar signal store values (for connected and action)
- `payload` — raw JSON from the browser POST (for action only)

### NATS subject layout

The relay constructs subjects from the website name and HTTP path segments.
You never need to build subjects manually — the SDK handles this for you.

```
Standard requests (relay → backend):
  auth:  {website}.{path_segs}.{uid}.{conn_uuid}.{req_uuid}.request
  anon:  {website}.{path_segs}.{conn_uuid}.{req_uuid}.request

SSE lifecycle (relay → backend):
  auth:  {website}.{path_segs}.{uid}.{conn_uuid}.request
  anon:  {website}.{path_segs}.{conn_uuid}.request

SSE events (backend → relay):
  auth:  {website}.{path_segs}.{uid}.{conn_uuid}.responses
  anon:  {website}.{path_segs}.{conn_uuid}.responses
```

Example for `battlefrontier`, path `/v0/sse/dashboard`, uid `user123`:

```
Relay → backend:  battlefrontier.v0.sse.dashboard.user123.{conn_uuid}.request
Backend → relay:  battlefrontier.v0.sse.dashboard.user123.{conn_uuid}.responses
```

### Per-page isolation

Each SSE endpoint path has its own NATS subject namespace. A
`HandleSSE("/v0/sse/dashboard", ...)` call only receives messages from users on
the dashboard page — never from users on the battle or inventory pages. NATS
subject matching enforces this at the infrastructure level.

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

### `relaysdk.New(cfg Config) (*Client, error)`

Creates a new SDK client, connects to NATS, and initialises a JetStream context.
This is the first function you call.

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

Publishes the website's endpoint configuration to the NATS KV store bucket
`relay-website-configs`. The relay watches this bucket and hot-reloads its
endpoint routing whenever a key is created or updated.

Call this **once on backend startup** (and whenever your endpoint config
changes). The relay will automatically register all listed endpoints and begin
routing HTTP traffic to your NATS handlers. The KV bucket is created
automatically if it does not exist.

```go
type WebsiteConfig struct {
    WebsiteName           string           // must match relay's website_name
    ApexDomain            string           // e.g. "battlefrontier.io"
    RequestTimeoutSeconds int              // default timeout for standard endpoints. 0=30s, -1=none
    Endpoints             []EndpointConfig
}

type EndpointConfig struct {
    Path                string   // HTTP path, e.g. "/v0/api/login"
    AllowedMethods      []string // e.g. ["GET","POST"]. Empty = all methods.
    RequireAuth         bool     // enforce session auth; sets UID on requests when true
    NatsSubjectOverride string   // override the auto-generated NATS subject base
    TimeoutSeconds      int      // 0=server default, -1=none, >0=seconds
    IsSSE               bool     // true = SSE endpoint. false = standard endpoint.
}
```

```go
err := client.PublishWebsiteConfig(relaysdk.WebsiteConfig{
    WebsiteName:           "mysite",
    ApexDomain:            "mysite.io",
    RequestTimeoutSeconds: 30,
    Endpoints: []relaysdk.EndpointConfig{
        {Path: "/v0/api/login",     AllowedMethods: []string{"POST"}, RequireAuth: false, IsSSE: false, TimeoutSeconds: 10},
        {Path: "/v0/sse/dashboard", AllowedMethods: []string{"GET"},  RequireAuth: true,  IsSSE: true,  TimeoutSeconds: -1},
    },
})
```

---

### `(*Client).Handle(path string, h Handler) error`

Subscribes to all standard (non-SSE) HTTP requests arriving on `path`. The
handler is called in a new goroutine for every incoming request.

By the time your handler is called, the relay has already returned `HTTP 200`
to the browser. Your job is to process the request and push any results back
to the browser over the open SSE connection using `conn.PatchElements`,
`conn.PatchSignals`, etc.

```go
type Handler func(ctx context.Context, req *Request, conn *Conn)
```

```go
client.Handle("/v0/api/login", func(ctx context.Context, req *relaysdk.Request, conn *relaysdk.Conn) {
    // req.Signals contains Datastar signal store values from the request
    // req.UID is the verified session UID (empty if require_auth is false)
    // req.ConnUUID is the browser's open SSE connection ID

    // Process the login...
    token := createSession(req.SignalString("email"), req.SignalString("password"))

    // Push results back to the browser over the open SSE connection.
    conn.PatchSignals(map[string]any{"token": token, "loggedIn": true})
    conn.PatchElements(`<div id="status">Welcome back!</div>`)
})
```

---

### `(*Client).HandleSSE(path string, h SSEHandler) error`

Subscribes to all `ClientMessage` lifecycle events for the given SSE endpoint
path. The handler is called for `connected`, `action`, and `disconnected`
messages — all three arrive on the same subscription.

The `context.Context` passed to the `connected` invocation is automatically
cancelled when the corresponding `disconnected` message arrives.

```go
type SSEHandler func(ctx context.Context, msg *ClientMessage, conn *Conn)
```

```go
client.HandleSSE("/v0/sse/dashboard", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    switch msg.Type {
    case relaysdk.ClientMessageConnected:
        // msg.Signals contains Datastar signal store values from the initial GET
        // msg.ConnUUID is the stable ID for this connection's lifetime
        conn.PatchElements(`<div id="status">online</div>`)
    case relaysdk.ClientMessageAction:
        // msg.Payload is the raw JSON body from the browser POST
        // msg.Signals contains Datastar signal store values from the POST
    case relaysdk.ClientMessageDisconnected:
        // clean up resources for this connection
    }
})
```

---

### `(*Client).Close()`

Drains all active NATS subscriptions and closes the NATS connection. Call this
on graceful shutdown.

```go
sigs := make(chan os.Signal, 1)
signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
<-sigs
client.Close()
```

---

## `Conn` method reference

A `*Conn` is passed to every `Handler` and `SSEHandler` invocation. It is the
handle through which your backend pushes data to the browser over the open SSE
connection.

All `Conn` methods publish a Datastar SSE event to the relay, which immediately
writes it as an SSE frame to the open browser connection.

---

### SSE push methods

#### `conn.PatchElements(html string) error`

Merges an HTML fragment into the browser DOM using Datastar's morphing
algorithm. Elements must have `id` attributes.

```go
conn.PatchElements(`<div id="score">42</div>`)
conn.PatchElements(`<ul id="items"><li>item 1</li><li>item 2</li></ul>`)
```

#### `conn.RemoveElement(selector string) error`

Removes the DOM element matching the CSS selector from the browser page.

```go
conn.RemoveElement("#notification-banner")
conn.RemoveElement(".loading-spinner")
```

#### `conn.PatchSignals(signals map[string]any) error`

Merges key-value pairs into the Datastar signal store on the browser.

```go
conn.PatchSignals(map[string]any{
    "playerHealth": 85,
    "isOnline":     true,
})
```

#### `conn.ExecuteScript(script string) error`

Runs a JavaScript snippet in the browser.

```go
conn.ExecuteScript("document.title = 'New Notification'")
```

#### `conn.Redirect(url string) error`

Navigates the browser to `url` via a Datastar redirect event.

```go
conn.Redirect("/v0/sse/login")
```

---

### SSE push to explicit connection ("To" variants)

These methods push to a specific `conn_uuid` rather than the connection
associated with the current handler invocation. Use them when one user's action
should trigger an update on a different user's open connection (e.g.
multiplayer games, admin notifications, chat).

#### `conn.PatchElementsTo(uid, connUUID, html string) error`
#### `conn.RemoveElementTo(uid, connUUID, selector string) error`
#### `conn.PatchSignalsTo(uid, connUUID string, signals map[string]any) error`
#### `conn.ExecuteScriptTo(uid, connUUID, script string) error`
#### `conn.RedirectTo(uid, connUUID, url string) error`

```go
// Push a score update to a specific opponent's connection
conn.PatchElementsTo(opponentUID, opponentConnUUID, `<div id="score">opponent: 10</div>`)
```

---

## Signal helper methods

Both `*Request` and `*ClientMessage` expose typed helper methods for reading
Datastar signal values without manual type assertions.

### On `*Request` and `*ClientMessage`

#### `Signal(key string) any`
Returns the raw signal value for `key`, or `nil` if not present.

#### `SignalString(key string) string`
Returns the signal value as a `string`. Returns `""` if absent or not a string.

#### `SignalFloat64(key string) float64`
Returns the signal value as a `float64`. JSON numbers unmarshal as float64.
Returns `0` if absent or wrong type.

#### `SignalBool(key string) bool`
Returns the signal value as a `bool`. Returns `false` if absent or wrong type.

### On `*Request` only

#### `IsSSE() bool`
Returns `true` when this request carries a `ConnUUID` (the browser has an open
SSE connection). Always `true` for standard endpoints — the relay rejects
requests with no `connUUID` before they reach your handler.

---

## Message types

### `Request` — received by `Handle` handlers

```go
type Request struct {
    UUID        string              // relay-assigned request UUID
    ConnUUID    string              // browser's open SSE connection ID (from signals)
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
    ConnUUID    string              // stable ID for this SSE connection's lifetime
    MessageID   string              // unique ID for this specific message
    Type        ClientMessageType   // "connected" | "action" | "disconnected"
    Website     string              // website_name from relay config
    UID         string              // session UID; empty when require_auth is false
    Path        string              // full HTTP path of the SSE endpoint
    Method      string              // HTTP method (populated for connected only)
    Headers     map[string][]string // HTTP headers (populated for connected only)
    QueryParams map[string][]string // URL query params (populated for connected only)
    Payload     json.RawMessage     // raw JSON from browser POST (action only)
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

---

## Patterns

### Sending initial state on connect

```go
client.HandleSSE("/v0/sse/dashboard", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    if msg.Type == relaysdk.ClientMessageConnected {
        stats := fetchStats(msg.UID)
        conn.PatchElements(renderStats(stats))
    }
})
```

### Dispatching browser actions

```go
client.HandleSSE("/v0/sse/battle", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    if msg.Type != relaysdk.ClientMessageAction {
        return
    }
    var p struct {
        Action   string `json:"action"`
        TargetID string `json:"targetId"`
    }
    json.Unmarshal(msg.Payload, &p)
    switch p.Action {
    case "attack":
        result := processAttack(msg.UID, p.TargetID)
        conn.PatchElements(renderBattleResult(result))
    case "retreat":
        conn.Redirect("/v0/sse/dashboard")
    }
})
```

### Reading signals from an action

```go
client.HandleSSE("/v0/sse/shop", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    if msg.Type == relaysdk.ClientMessageAction {
        itemID   := msg.SignalString("selectedItemId")
        quantity := int(msg.SignalFloat64("quantity"))
        // process purchase...
        conn.PatchElements(renderCart(itemID, quantity))
    }
})
```

### Blocking until disconnect

```go
client.HandleSSE("/v0/sse/live", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    if msg.Type == relaysdk.ClientMessageConnected {
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

### Standard endpoint — reading signals and pushing results over SSE

The relay has already returned `HTTP 200` to the browser before your handler
runs. Push all results back over the open SSE connection.

```go
client.Handle("/v0/api/search", func(ctx context.Context, req *relaysdk.Request, conn *relaysdk.Conn) {
    query := req.SignalString("searchQuery")
    page  := int(req.SignalFloat64("page"))

    results := performSearch(query, page)
    conn.PatchElements(renderResults(results))
    conn.PatchSignals(map[string]any{"resultCount": len(results)})
})
```

### Standard endpoint — handling errors

Since the relay already sent `HTTP 200`, communicate errors back to the browser
via SSE signals or patched elements rather than HTTP status codes.

```go
client.Handle("/v0/api/update-profile", func(ctx context.Context, req *relaysdk.Request, conn *relaysdk.Conn) {
    newName := req.SignalString("displayName")
    if newName == "" {
        conn.PatchSignals(map[string]any{
            "hasError": true,
            "error":    "display name cannot be empty",
        })
        return
    }

    if err := updateProfile(req.UID, newName); err != nil {
        conn.PatchSignals(map[string]any{
            "hasError": true,
            "error":    "failed to update profile, please try again",
        })
        return
    }

    conn.PatchElements(fmt.Sprintf(`<span id="display-name">%s</span>`, newName))
    conn.PatchSignals(map[string]any{"hasError": false, "error": ""})
})
```

### Pushing to another user's connection

```go
client.HandleSSE("/v0/sse/battle", func(ctx context.Context, msg *relaysdk.ClientMessage, conn *relaysdk.Conn) {
    if msg.Type == relaysdk.ClientMessageAction {
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
connection opens. Every page form automatically includes it via Datastar's
signal binding. The relay uses `connUUID` to validate standard endpoint requests
— if it is missing or the connection is not found, the relay returns an HTTP
error and the request never reaches your backend.

```html
<!-- page.html — initialise the connUUID signal to empty string -->
<div data-signals="{connUUID: '', hasError: false, error: ''}">
  <!-- Open the SSE connection on page load -->
  <div data-on-load="@get('/v0/sse/dashboard')"></div>
</div>

<!-- Action form — connUUID is bound automatically from the signal store -->
<form data-on-submit="@post('/v0/sse/dashboard/message')">
  <input type="hidden" data-bind="connUUID">
  <input type="hidden" name="action" value="refresh">
  <button type="submit">Refresh</button>
</form>

<!-- Display errors pushed back from the backend via conn.PatchSignals -->
<span id="error-msg" data-show="hasError" data-text="error"></span>

<!-- Action with payload -->
<form data-on-submit="@post('/v0/sse/battle/message')">
  <input type="hidden" data-bind="connUUID">
  <input data-bind="targetId" placeholder="Target ID">
  <button type="submit">Attack</button>
</form>
```

Each page posts to its own message endpoint (`/{path}/message`). The relay
validates that the `conn_uuid` was opened on the matching SSE path and rejects
cross-page reuse with `410 Gone`.

---

## NATS subject layout (reference)

The SDK constructs subjects automatically. This table is provided for debugging.

### Standard endpoints

| Direction           | Subject (authenticated)                                          |
|---------------------|------------------------------------------------------------------|
| Relay → backend     | `{website}.{path_segs}.{uid}.{conn_uuid}.{req_uuid}.request`    |

| Direction           | Subject (unauthenticated)                                        |
|---------------------|------------------------------------------------------------------|
| Relay → backend     | `{website}.{path_segs}.{conn_uuid}.{req_uuid}.request`          |

> Note: There is no "backend → relay" subject for standard endpoints. The relay
> returns `HTTP 200` immediately on publish. All backend responses travel over
> SSE via the responses subjects below.

### SSE endpoints

| Direction           | Subject (authenticated)                                          |
|---------------------|------------------------------------------------------------------|
| Relay → backend     | `{website}.{path_segs}.{uid}.{conn_uuid}.request`               |
| Backend → relay     | `{website}.{path_segs}.{uid}.{conn_uuid}.responses`             |

| Direction           | Subject (unauthenticated)                                        |
|---------------------|------------------------------------------------------------------|
| Relay → backend     | `{website}.{path_segs}.{conn_uuid}.request`                     |
| Backend → relay     | `{website}.{path_segs}.{conn_uuid}.responses`                   |

### SDK subscription wildcards

For `Handle("/v0/api/login", ...)` the SDK creates two subscriptions:

```
battlefrontier.v0.api.login.*.*.*.request   ← authenticated (uid + conn_uuid + req_uuid)
battlefrontier.v0.api.login.*.*.request     ← unauthenticated (conn_uuid + req_uuid)
```

For `HandleSSE("/v0/sse/dashboard", ...)` the SDK creates two subscriptions:

```
battlefrontier.v0.sse.dashboard.*.*.request ← authenticated (uid + conn_uuid)
battlefrontier.v0.sse.dashboard.*.request   ← unauthenticated (conn_uuid)
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

Application-level errors (validation failures, not-found, permission denied,
etc.) should be communicated back to the browser via `conn.PatchSignals` or
`conn.PatchElements` — not via HTTP status codes, since the relay has already
sent `HTTP 200` by the time your handler runs.

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
| `path`            | HTTP path, e.g. `"/v0/sse/dashboard"`                              |
| `allowed_methods` | HTTP methods accepted. Empty = all methods.                        |
| `require_auth`    | Enforce session auth. Sets `UID` on requests and messages.         |
| `is_sse`          | `true` → SSE endpoint, use `HandleSSE`. `false` → use `Handle`.   |
| `timeout_seconds` | Per-endpoint timeout. `0`=server default, `-1`=none, `>0`=seconds. SSE endpoints must be `-1`. |

