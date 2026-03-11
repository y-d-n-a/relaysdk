
package relaysdk

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// ─── Message types (mirror relay internal types) ──────────────────────────────

// Request is received by the backend for standard (non-SSE) endpoints.
//
// The relay publishes one Request per HTTP call on a standard endpoint after
// all relay-level validation has passed (CORS, auth, endpoint registration,
// method check). By the time your handler receives a Request, the relay has
// already returned HTTP 200 to the browser. Your handler should push results
// back to the browser over the open SSE connection using conn.PatchElements,
// conn.PatchSignals, etc.
//
// ConnUUID is the conn_uuid of the open SSE connection that the browser has
// established for the current page. The relay extracts it from the Datastar
// signal store (the frontend automatically includes it in every request because
// handleSSE patches it into the signal store when the SSE connection opens).
// The backend uses ConnUUID to route SSE events back to the correct browser
// connection via conn.PatchElements, conn.PatchSignals, etc.
//
// Signals contains the Datastar signal store values sent with the request.
// The relay uses datastar.ReadSignals to extract these regardless of HTTP
// method — GET reads from the query string, all other methods read from the
// body. The backend never needs an *http.Request.
type Request struct {
	UUID        string              `json:"uuid"`
	ConnUUID    string              `json:"conn_uuid,omitempty"`
	Website     string              `json:"website"`
	UID         string              `json:"uid"`
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	Headers     map[string][]string `json:"headers"`
	Body        json.RawMessage     `json:"body"`
	QueryParams map[string][]string `json:"query_params"`
	Signals     map[string]any      `json:"signals,omitempty"`
}

// IsSSE returns true when this request carries a ConnUUID, meaning it arrived
// from a browser that has an open SSE connection. The relay rejects standard
// endpoint requests that lack a connUUID before they reach your handler, so
// this is always true for requests delivered via Handle.
func (r *Request) IsSSE() bool {
	return r.ConnUUID != ""
}

// Signal returns the value of a single signal by key, or nil if not present.
func (r *Request) Signal(key string) any {
	if r.Signals == nil {
		return nil
	}
	return r.Signals[key]
}

// SignalString returns the value of a signal as a string.
// Returns "" if the key is absent or the value is not a string.
func (r *Request) SignalString(key string) string {
	v := r.Signal(key)
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// SignalFloat64 returns the value of a signal as a float64.
// JSON numbers unmarshal as float64. Returns 0 if absent or wrong type.
func (r *Request) SignalFloat64(key string) float64 {
	v := r.Signal(key)
	if v == nil {
		return 0
	}
	f, _ := v.(float64)
	return f
}

// SignalBool returns the value of a signal as a bool.
// Returns false if absent or wrong type.
func (r *Request) SignalBool(key string) bool {
	v := r.Signal(key)
	if v == nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

// ─── ClientMessage (SSE lifecycle model) ─────────────────────────────────────

// ClientMessageType identifies the lifecycle stage of a ClientMessage.
type ClientMessageType string

const (
	// ClientMessageConnected is published when the browser opens the SSE connection.
	ClientMessageConnected ClientMessageType = "connected"

	// ClientMessageReconnected is published when the browser reopens the SSE connection.
	ClientMessageReconnected ClientMessageType = "reconnected"

	// ClientMessageAction is published when the browser POSTs to /{path}/message.
	ClientMessageAction ClientMessageType = "action"

	// ClientMessageDisconnected is published when the browser closes the SSE connection.
	ClientMessageDisconnected ClientMessageType = "disconnected"
)

// ClientMessage is the single NATS message type for all SSE lifecycle events
// and browser-initiated actions. The relay publishes one of these for every
// SSE lifecycle event; the backend's HandleSSE handler receives all three types
// on the same subscription.
//
// ConnUUID identifies the open SSE connection for its entire lifetime. The
// backend uses it to route SSE events back to the browser via conn.PatchElements,
// conn.PatchSignals, etc.
//
// Signals contains the Datastar signal store values sent with the request.
// Populated for ClientMessageConnected (from the GET query string) and
// ClientMessageAction (from the POST body). Empty for ClientMessageDisconnected.
//
// Payload carries the raw JSON body of the companion POST for
// ClientMessageAction. Interpret it however your application requires.
type ClientMessage struct {
	ConnUUID    string              `json:"conn_uuid"`
	MessageID   string              `json:"message_id"`
	Type        ClientMessageType   `json:"type"`
	Website     string              `json:"website"`
	UID         string              `json:"uid"`
	Path        string              `json:"path"`
	Method      string              `json:"method,omitempty"`
	Headers     map[string][]string `json:"headers,omitempty"`
	QueryParams map[string][]string `json:"query_params,omitempty"`
	Payload     json.RawMessage     `json:"payload,omitempty"`
	Signals     map[string]any      `json:"signals,omitempty"`
}

// Signal returns the value of a single signal by key, or nil if not present.
func (m *ClientMessage) Signal(key string) any {
	if m.Signals == nil {
		return nil
	}
	return m.Signals[key]
}

// SignalString returns the value of a signal as a string.
// Returns "" if the key is absent or the value is not a string.
func (m *ClientMessage) SignalString(key string) string {
	v := m.Signal(key)
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// SignalFloat64 returns the value of a signal as a float64.
// JSON numbers unmarshal as float64. Returns 0 if absent or wrong type.
func (m *ClientMessage) SignalFloat64(key string) float64 {
	v := m.Signal(key)
	if v == nil {
		return 0
	}
	f, _ := v.(float64)
	return f
}

// SignalBool returns the value of a signal as a bool.
// Returns false if absent or wrong type.
func (m *ClientMessage) SignalBool(key string) bool {
	v := m.Signal(key)
	if v == nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

// ─── SSE action types ─────────────────────────────────────────────────────────

// SSEAction enumerates the Datastar SSE methods the relay can invoke.
type SSEAction string

const (
	PatchElements SSEAction = "patch_elements"
	RemoveElement SSEAction = "remove_element"
	PatchSignals  SSEAction = "patch_signals"
	ExecuteScript SSEAction = "execute_script"
	Redirect      SSEAction = "redirect"
)

// sseEvent is the internal payload published to the relay's responses subject
// to push a Datastar event down an open SSE connection.
//
// Published via JetStream to:
//
//	uid present: {website}.{path_segs}.{uid}.{conn_uuid}.responses
//	uid absent:  {website}.{path_segs}.{conn_uuid}.responses
//
// The relay subscribes to this subject when the SSE connection opens and
// dispatches each event to the browser using the appropriate Datastar method.
type sseEvent struct {
	ConnUUID string         `json:"conn_uuid"`
	Action   SSEAction      `json:"action"`
	HTML     string         `json:"html,omitempty"`
	Selector string         `json:"selector,omitempty"`
	Signals  map[string]any `json:"signals,omitempty"`
	Script   string         `json:"script,omitempty"`
	URL      string         `json:"url,omitempty"`
}

// ─── Handler types ────────────────────────────────────────────────────────────

// Handler is called for every incoming standard request on a subscribed path.
//
// By the time this handler is called, the relay has already returned HTTP 200
// to the browser. Push all results, errors, and state changes back to the
// browser over the open SSE connection using conn.PatchElements,
// conn.PatchSignals, etc.
type Handler func(ctx context.Context, req *Request, conn *Conn)

// SSEHandler is called for every ClientMessage lifecycle event on a subscribed
// SSE path. All three types (connected, action, disconnected) arrive on the
// same subscription. The handler is called in a new goroutine for each message.
type SSEHandler func(ctx context.Context, msg *ClientMessage, conn *Conn)

// ─── WebsiteConfig mirrors config.WebsiteConfig for SDK use ──────────────────

// WebsiteConfig is the configuration that a backend server publishes to the
// NATS KV store bucket "relay-website-configs" so that relay servers can pick
// it up automatically and begin routing HTTP traffic.
//
// Call client.PublishWebsiteConfig(cfg) once on backend startup (and whenever
// your endpoint config changes). The relay hot-reloads its routing whenever
// a key is created or updated in the KV bucket.
type WebsiteConfig struct {
	WebsiteName           string           `json:"website_name"`
	ApexDomain            string           `json:"apex_domain"`
	RequestTimeoutSeconds int              `json:"request_timeout_seconds"`
	Endpoints             []EndpointConfig `json:"endpoints"`
}

// EndpointConfig describes a single relay endpoint for use in WebsiteConfig.
type EndpointConfig struct {
	// Path is the HTTP path, e.g. "/v0/api/login" or "/sse/v0/dashboard".
	Path string `json:"path"`

	// AllowedMethods lists the HTTP methods this endpoint accepts.
	// Empty means all methods are accepted.
	AllowedMethods []string `json:"allowed_methods"`

	// RequireAuth controls whether session authentication is enforced.
	// When true, the relay verifies the session token and populates UID on
	// all Request and ClientMessage values delivered to the backend.
	RequireAuth bool `json:"require_auth"`

	// NatsSubjectOverride bypasses the auto-generated subject base.
	// Leave empty in almost all cases.
	NatsSubjectOverride string `json:"nats_subject_override"`

	// TimeoutSeconds controls the backend response timeout.
	//  0  = use the server-level default (RequestTimeoutSeconds).
	//  -1 = no timeout. Required for SSE endpoints.
	//  >0 = override in seconds.
	TimeoutSeconds int `json:"timeout_seconds"`

	// IsSSE marks this endpoint as a long-lived Datastar SSE connection.
	// Use HandleSSE for these endpoints. Use Handle for all others.
	IsSSE bool `json:"is_sse"`
}

// ─── SDK Client ───────────────────────────────────────────────────────────────

// Client is the SDK entry point. Create one per backend process with New.
type Client struct {
	nc          *nats.Conn
	js          nats.JetStreamContext
	websiteName string
	subs        []*nats.Subscription
	jsSubs      []*nats.Subscription
}

// Config holds the SDK configuration passed to New.
type Config struct {
	// NatsURLs is the list of NATS server URLs to try in order.
	// At least one is required.
	NatsURLs []string

	// WebsiteName must match the relay's website_name for this site.
	// It is used as the first segment of every NATS subject.
	WebsiteName string
}

// New connects to NATS, initialises a JetStream context, and returns a ready
// Client. Tries each URL in NatsURLs in order and uses the first that succeeds.
// Reconnects automatically on drop.
func New(cfg Config) (*Client, error) {
	if len(cfg.NatsURLs) == 0 {
		return nil, fmt.Errorf("relaysdk: at least one NATS URL required")
	}
	if cfg.WebsiteName == "" {
		return nil, fmt.Errorf("relaysdk: WebsiteName required")
	}

	opts := []nats.Option{
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			if err != nil {
				log.Printf("relaysdk: nats disconnected: %v", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Printf("relaysdk: nats reconnected to %s", nc.ConnectedUrl())
		}),
	}

	var (
		nc      *nats.Conn
		lastErr error
	)
	for _, u := range cfg.NatsURLs {
		nc, lastErr = nats.Connect(u, opts...)
		if lastErr == nil {
			log.Printf("relaysdk: connected to %s", u)
			break
		}
		log.Printf("relaysdk: failed to connect to %s: %v", u, lastErr)
	}
	if nc == nil {
		return nil, fmt.Errorf("relaysdk: could not connect to any NATS server: %w", lastErr)
	}

	js, err := nc.JetStream(nats.PublishAsyncMaxPending(256))
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("relaysdk: failed to create JetStream context: %w", err)
	}

	return &Client{
		nc:          nc,
		js:          js,
		websiteName: cfg.WebsiteName,
	}, nil
}

// PublishWebsiteConfig publishes the given WebsiteConfig to the NATS KV store
// bucket "relay-website-configs" under the key cfg.WebsiteName.
//
// The relay watches this bucket and hot-reloads its endpoint routing whenever
// a key is created or updated. Call this once on backend startup and whenever
// your endpoint configuration changes. The KV bucket is created automatically
// if it does not exist.
func (c *Client) PublishWebsiteConfig(cfg WebsiteConfig) error {
	if cfg.WebsiteName == "" {
		return fmt.Errorf("relaysdk: PublishWebsiteConfig: WebsiteName is required")
	}
	if cfg.ApexDomain == "" {
		return fmt.Errorf("relaysdk: PublishWebsiteConfig: ApexDomain is required")
	}

	const bucketName = "relay-website-configs"

	kv, err := c.js.KeyValue(bucketName)
	if err == nats.ErrBucketNotFound {
		kv, err = c.js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket:  bucketName,
			Storage: nats.FileStorage,
			History: 5,
		})
	}
	if err != nil {
		return fmt.Errorf("relaysdk: PublishWebsiteConfig: open/create KV bucket %q: %w", bucketName, err)
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("relaysdk: PublishWebsiteConfig: marshal: %w", err)
	}

	if _, err := kv.Put(cfg.WebsiteName, data); err != nil {
		return fmt.Errorf("relaysdk: PublishWebsiteConfig: KV put %q: %w", cfg.WebsiteName, err)
	}

	log.Printf("relaysdk: published website config for %q to KV bucket %q", cfg.WebsiteName, bucketName)
	return nil
}

// Handle subscribes to all standard (non-SSE) HTTP requests arriving on the
// given path. The handler is called in a new goroutine for every incoming
// request.
//
// By the time your handler is called, the relay has already returned HTTP 200
// to the browser. Push all results back to the browser over the open SSE
// connection using conn.PatchElements, conn.PatchSignals, etc.
//
// Application-level errors (validation failures, not-found, permission denied,
// etc.) should be communicated via conn.PatchSignals or conn.PatchElements,
// not via HTTP status codes.
//
// The relay publishes standard requests to:
//
//	authenticated:   {website}.{path_segs}.{uid}.{conn_uuid}.{req_uuid}.request
//	unauthenticated: {website}.{path_segs}.{conn_uuid}.{req_uuid}.request
//
// This method creates two JetStream subscriptions — one for each pattern —
// so a single Handle call covers both authenticated and unauthenticated
// requests for the same path.
func (c *Client) Handle(path string, h Handler) error {
	authedSubject := c.apiRequestSubjectWithUID(path)
	anonSubject := c.apiRequestSubjectNoUID(path)

	handler := func(msg *nats.Msg) {
		if err := msg.Ack(); err != nil {
			log.Printf("relaysdk: Handle ack error on %q: %v", msg.Subject, err)
		}

		var req Request
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			log.Printf("relaysdk: malformed request on %q: %v", msg.Subject, err)
			return
		}

		conn := &Conn{
			nc:          c.nc,
			js:          c.js,
			websiteName: c.websiteName,
			req:         &req,
		}

		go h(context.Background(), &req, conn)
	}

	sub1, err := c.js.Subscribe(authedSubject, handler,
		nats.DeliverAll(),
		nats.AckExplicit(),
		nats.MaxDeliver(3),
	)
	if err != nil {
		return fmt.Errorf("relaysdk: subscribe %q: %w", authedSubject, err)
	}
	c.jsSubs = append(c.jsSubs, sub1)
	log.Printf("relaysdk: Handle path %q → subject %q (JetStream)", path, authedSubject)

	sub2, err := c.js.Subscribe(anonSubject, handler,
		nats.DeliverAll(),
		nats.AckExplicit(),
		nats.MaxDeliver(3),
	)
	if err != nil {
		return fmt.Errorf("relaysdk: subscribe %q: %w", anonSubject, err)
	}
	c.jsSubs = append(c.jsSubs, sub2)
	log.Printf("relaysdk: Handle path %q → subject %q (JetStream)", path, anonSubject)

	return nil
}

// HandleSSE subscribes to all ClientMessage lifecycle events for the given SSE
// endpoint path. The handler is called for connected, action, and disconnected
// messages — all three arrive on the same subscription.
//
// The relay publishes SSE lifecycle events to:
//
//	authenticated:   {website}.{path_segs}.{uid}.{conn_uuid}.request
//	unauthenticated: {website}.{path_segs}.{conn_uuid}.request
//
// This method creates two JetStream subscriptions — one for each pattern —
// so a single HandleSSE call covers both authenticated and unauthenticated
// connections for the same path.
//
// Context cancellation: the context passed to the ClientMessageConnected
// handler invocation is cancelled automatically when the corresponding
// ClientMessageDisconnected message arrives for the same conn_uuid. Use
// <-ctx.Done() to block until the user leaves.
func (c *Client) HandleSSE(path string, h SSEHandler) error {
	authedSubject := c.sseLifecycleSubjectWithUID(path)
	anonSubject := c.sseLifecycleSubjectNoUID(path)

	var (
		cancelMu  sync.Mutex
		cancelMap = make(map[string]context.CancelFunc)
	)

	handler := func(msg *nats.Msg) {
		if err := msg.Ack(); err != nil {
			log.Printf("relaysdk: HandleSSE ack error on %q: %v", msg.Subject, err)
		}

		var cm ClientMessage
		if err := json.Unmarshal(msg.Data, &cm); err != nil {
			log.Printf("relaysdk: malformed ClientMessage on %q: %v", msg.Subject, err)
			return
		}

		conn := &Conn{
			nc:          c.nc,
			js:          c.js,
			websiteName: c.websiteName,
			clientMsg:   &cm,
		}

		switch cm.Type {
		case ClientMessageConnected:
			ctx, cancel := context.WithCancel(context.Background())
			cancelMu.Lock()
			cancelMap[cm.ConnUUID] = cancel
			cancelMu.Unlock()

			go func() {
				defer func() {
					cancelMu.Lock()
					if fn, ok := cancelMap[cm.ConnUUID]; ok {
						fn()
						delete(cancelMap, cm.ConnUUID)
					}
					cancelMu.Unlock()
				}()
				h(ctx, &cm, conn)
			}()

		case ClientMessageAction:
			go h(context.Background(), &cm, conn)

		case ClientMessageDisconnected:
			cancelMu.Lock()
			if cancel, ok := cancelMap[cm.ConnUUID]; ok {
				cancel()
				delete(cancelMap, cm.ConnUUID)
			}
			cancelMu.Unlock()

			go h(context.Background(), &cm, conn)

		default:
			log.Printf("relaysdk: unknown ClientMessage type %q for conn %q", cm.Type, cm.ConnUUID)
		}
	}

	sub1, err := c.js.Subscribe(authedSubject, handler,
		nats.DeliverAll(),
		nats.AckExplicit(),
		nats.MaxDeliver(3),
	)
	if err != nil {
		return fmt.Errorf("relaysdk: HandleSSE subscribe %q: %w", authedSubject, err)
	}
	c.jsSubs = append(c.jsSubs, sub1)
	log.Printf("relaysdk: HandleSSE path %q → subject %q (JetStream)", path, authedSubject)

	sub2, err := c.js.Subscribe(anonSubject, handler,
		nats.DeliverAll(),
		nats.AckExplicit(),
		nats.MaxDeliver(3),
	)
	if err != nil {
		return fmt.Errorf("relaysdk: HandleSSE subscribe %q: %w", anonSubject, err)
	}
	c.jsSubs = append(c.jsSubs, sub2)
	log.Printf("relaysdk: HandleSSE path %q → subject %q (JetStream)", path, anonSubject)

	return nil
}

// Close drains all active NATS subscriptions and closes the NATS connection.
// Call this on graceful shutdown to ensure all in-flight messages are processed
// before the process exits.
func (c *Client) Close() {
	for _, s := range c.jsSubs {
		s.Drain()
	}
	for _, s := range c.subs {
		s.Drain()
	}
	c.nc.Drain()
}

// ─── Subject builders (client-level) ─────────────────────────────────────────
//
// These methods build the JetStream wildcard subjects that the SDK subscribes
// to. They mirror the relay's subject.go exactly so that relay publishes are
// always matched by SDK subscriptions.
//
// ── Standard API endpoints ────────────────────────────────────────────────────
//
// The relay (BuildAPIRequestSubject) publishes standard requests to:
//
//	uid present: {website}.{path_segs}.{uid}.{conn_uuid}.{req_uuid}.request
//	uid absent:  {website}.{path_segs}.{conn_uuid}.{req_uuid}.request
//
// SDK wildcard subscriptions:
//
//	uid present: {website}.{path_segs}.*.*.*.request  (uid + conn_uuid + req_uuid)
//	uid absent:  {website}.{path_segs}.*.*.request    (conn_uuid + req_uuid)
//
// ── SSE lifecycle endpoints ───────────────────────────────────────────────────
//
// The relay (BuildRequestSubject) publishes SSE lifecycle events to:
//
//	uid present: {website}.{path_segs}.{uid}.{conn_uuid}.request
//	uid absent:  {website}.{path_segs}.{conn_uuid}.request
//
// SDK wildcard subscriptions:
//
//	uid present: {website}.{path_segs}.*.*.request  (uid + conn_uuid)
//	uid absent:  {website}.{path_segs}.*.request    (conn_uuid)

// apiRequestSubjectWithUID returns the JetStream wildcard subject for standard
// endpoint requests where a uid is present.
//
// Format: {website}.{path_segs}.*.*.*.request
// Wildcards match: uid, conn_uuid, req_uuid
func (c *Client) apiRequestSubjectWithUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 1+len(segs)+4)
	parts = append(parts, c.websiteName)
	parts = append(parts, segs...)
	parts = append(parts, "*", "*", "*", "request")
	return strings.Join(parts, ".")
}

// apiRequestSubjectNoUID returns the JetStream wildcard subject for standard
// endpoint requests where no uid is present (unauthenticated endpoints).
//
// Format: {website}.{path_segs}.*.*.request
// Wildcards match: conn_uuid, req_uuid
func (c *Client) apiRequestSubjectNoUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 1+len(segs)+3)
	parts = append(parts, c.websiteName)
	parts = append(parts, segs...)
	parts = append(parts, "*", "*", "request")
	return strings.Join(parts, ".")
}

// sseLifecycleSubjectWithUID returns the JetStream wildcard subject for SSE
// lifecycle events where a uid is present.
//
// Format: {website}.{path_segs}.*.*.request
// Wildcards match: uid, conn_uuid
func (c *Client) sseLifecycleSubjectWithUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 1+len(segs)+3)
	parts = append(parts, c.websiteName)
	parts = append(parts, segs...)
	parts = append(parts, "*", "*", "request")
	return strings.Join(parts, ".")
}

// sseLifecycleSubjectNoUID returns the JetStream wildcard subject for SSE
// lifecycle events where no uid is present (unauthenticated SSE endpoints).
//
// Format: {website}.{path_segs}.*.request
// Wildcard matches: conn_uuid
func (c *Client) sseLifecycleSubjectNoUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 1+len(segs)+2)
	parts = append(parts, c.websiteName)
	parts = append(parts, segs...)
	parts = append(parts, "*", "request")
	return strings.Join(parts, ".")
}

// ─── Conn ─────────────────────────────────────────────────────────────────────

// Conn is passed to every Handler and SSEHandler invocation. It is the handle
// through which your backend pushes data back to the browser over the open SSE
// connection.
//
// All Conn methods publish Datastar SSE events to the relay, which immediately
// writes them as SSE frames to the open browser connection. There is no
// mechanism for the backend to send a direct HTTP response — the relay handles
// all HTTP responses itself.
type Conn struct {
	nc          *nats.Conn
	js          nats.JetStreamContext
	websiteName string
	req         *Request
	clientMsg   *ClientMessage
}

func (c *Conn) connUUID() string {
	if c.clientMsg != nil {
		return c.clientMsg.ConnUUID
	}
	if c.req != nil {
		return c.req.ConnUUID
	}
	return ""
}

func (c *Conn) uid() string {
	if c.clientMsg != nil {
		return c.clientMsg.UID
	}
	if c.req != nil {
		return c.req.UID
	}
	return ""
}

func (c *Conn) reqPath() string {
	if c.clientMsg != nil {
		return c.clientMsg.Path
	}
	if c.req != nil {
		return c.req.Path
	}
	return ""
}

// ─── SSE push methods ─────────────────────────────────────────────────────────

// PatchElements pushes an HTML fragment to the browser using Datastar's
// morphing algorithm. The html string must contain elements with id attributes.
// Datastar matches them against existing DOM elements and updates only what
// changed.
func (c *Conn) PatchElements(html string) error {
	return c.publishSSEEvent(sseEvent{ConnUUID: c.connUUID(), Action: PatchElements, HTML: html})
}

// RemoveElement removes the DOM element matching the CSS selector from the
// browser page.
func (c *Conn) RemoveElement(selector string) error {
	return c.publishSSEEvent(sseEvent{ConnUUID: c.connUUID(), Action: RemoveElement, Selector: selector})
}

// PatchSignals merges key-value pairs into the Datastar signal store on the
// browser. The browser's reactive system automatically updates any DOM elements
// bound to these signals.
func (c *Conn) PatchSignals(signals map[string]any) error {
	return c.publishSSEEvent(sseEvent{ConnUUID: c.connUUID(), Action: PatchSignals, Signals: signals})
}

// ExecuteScript runs a JavaScript snippet in the browser.
func (c *Conn) ExecuteScript(script string) error {
	return c.publishSSEEvent(sseEvent{ConnUUID: c.connUUID(), Action: ExecuteScript, Script: script})
}

// Redirect navigates the browser to url via a Datastar redirect event.
func (c *Conn) Redirect(url string) error {
	return c.publishSSEEvent(sseEvent{ConnUUID: c.connUUID(), Action: Redirect, URL: url})
}

// ─── "To" variants — push to an explicit connection ──────────────────────────

// PatchElementsTo pushes an HTML patch to a specific SSE connection identified
// by uid and connUUID.
func (c *Conn) PatchElementsTo(uid, connUUID, html string) error {
	return c.publishSSEEventTo(uid, connUUID, sseEvent{ConnUUID: connUUID, Action: PatchElements, HTML: html})
}

// RemoveElementTo removes a DOM element on a specific SSE connection.
func (c *Conn) RemoveElementTo(uid, connUUID, selector string) error {
	return c.publishSSEEventTo(uid, connUUID, sseEvent{ConnUUID: connUUID, Action: RemoveElement, Selector: selector})
}

// PatchSignalsTo merges signals into the Datastar store on a specific SSE
// connection.
func (c *Conn) PatchSignalsTo(uid, connUUID string, signals map[string]any) error {
	return c.publishSSEEventTo(uid, connUUID, sseEvent{ConnUUID: connUUID, Action: PatchSignals, Signals: signals})
}

// ExecuteScriptTo runs a script on a specific SSE connection.
func (c *Conn) ExecuteScriptTo(uid, connUUID, script string) error {
	return c.publishSSEEventTo(uid, connUUID, sseEvent{ConnUUID: connUUID, Action: ExecuteScript, Script: script})
}

// RedirectTo navigates a specific SSE connection to a new URL.
func (c *Conn) RedirectTo(uid, connUUID, url string) error {
	return c.publishSSEEventTo(uid, connUUID, sseEvent{ConnUUID: connUUID, Action: Redirect, URL: url})
}

// ─── internal publish helpers ─────────────────────────────────────────────────

// publishSSEEvent publishes a Datastar SSE event via JetStream to the
// responses subject for the current connection.
func (c *Conn) publishSSEEvent(event sseEvent) error {
	subject := sseResponsesSubject(c.websiteName, c.uid(), c.reqPath(), c.connUUID())
	return c.jsPublish(subject, event)
}

// publishSSEEventTo publishes a Datastar SSE event to a specific connection
// identified by uid and connUUID, using the current request's path to build
// the subject.
func (c *Conn) publishSSEEventTo(uid, connUUID string, event sseEvent) error {
	subject := sseResponsesSubject(c.websiteName, uid, c.reqPath(), connUUID)
	return c.jsPublish(subject, event)
}

func (c *Conn) jsPublish(subject string, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("relaysdk: marshal: %w", err)
	}
	if _, err := c.js.Publish(subject, payload); err != nil {
		return fmt.Errorf("relaysdk: js publish to %q: %w", subject, err)
	}
	return nil
}

// ─── Subject builders (package-level, used by Conn) ──────────────────────────

// sseResponsesSubject builds the JetStream subject the backend publishes SSE
// events to. Mirrors relay/subject.go BuildSSEResponsesSubject.
//
// The relay subscribes to this exact subject (no wildcard) when the SSE
// connection opens and dispatches each event to the browser.
//
// Format (uid present): {website}.{path_segs}.{uid}.{conn_uuid}.responses
// Format (uid absent):  {website}.{path_segs}.{conn_uuid}.responses
func sseResponsesSubject(website, uid, path, connUUID string) string {
	segs := apiSegments(website, uid, path)
	segs = append(segs, connUUID, "responses")
	return strings.Join(segs, ".")
}

// ─── Internal segment helpers ─────────────────────────────────────────────────

// apiSegments builds the website + path-segments + uid portion shared by all
// subject builders. Mirrors relay/subject.go apiSegments exactly.
//
// uid comes AFTER the path segments:
//
//	uid non-empty: [website, seg1, seg2, …, uid]
//	uid empty:     [website, seg1, seg2, …]
func apiSegments(website, uid, path string) []string {
	segs := pathSegments(path)
	parts := make([]string, 0, 1+len(segs)+1)
	parts = append(parts, website)
	parts = append(parts, segs...)
	if uid != "" {
		parts = append(parts, uid)
	}
	return parts
}

// baseSegments is an alias for apiSegments, kept for test compatibility.
func baseSegments(website, uid, path string) []string {
	return apiSegments(website, uid, path)
}

// pathSegments splits a URL path into dot-safe NATS subject segments.
// Empty segments (from leading/trailing slashes) are dropped.
// Dots within a segment are replaced with underscores to avoid breaking
// NATS subject parsing.
func pathSegments(path string) []string {
	parts := strings.Split(path, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = strings.ReplaceAll(p, ".", "_")
		out = append(out, p)
	}
	return out
}
