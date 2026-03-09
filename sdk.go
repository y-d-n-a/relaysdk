
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

// Request is received by the backend for standard endpoints.
//
// Signals contains the Datastar signal store values sent with the request.
// For GET requests these come from the ?datastar= query parameter.
// For POST/PUT/PATCH/DELETE these come from the JSON body.
// The relay extracts them before publishing so the backend never needs an
// *http.Request.
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
	// Signals contains the Datastar signal store values extracted by the relay.
	// Empty map when no signals were sent with the request.
	Signals map[string]any `json:"signals,omitempty"`
}

// IsSSE returns true when this request opened an SSE connection.
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

// Response is what the backend publishes for standard (non-SSE) endpoints.
type Response struct {
	UUID       string              `json:"uuid"`
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
	Body       json.RawMessage     `json:"body"`
	Error      string              `json:"error,omitempty"`
}

// DisconnectNotification is received when an SSE client disconnects (legacy).
type DisconnectNotification struct {
	ConnUUID string `json:"conn_uuid"`
	Website  string `json:"website"`
	UID      string `json:"uid"`
	Path     string `json:"path"`
}

// ─── ClientMessage (new model) ────────────────────────────────────────────────

// ClientMessageType identifies the lifecycle stage of a ClientMessage.
type ClientMessageType string

const (
	ClientMessageConnected    ClientMessageType = "connected"
	ClientMessageAction       ClientMessageType = "action"
	ClientMessageDisconnected ClientMessageType = "disconnected"
)

// ClientMessage is the single NATS message type for all SSE lifecycle events
// and browser-initiated actions under the new per-page endpoint model.
//
// Signals contains the Datastar signal store values extracted by the relay:
//   - ClientMessageConnected: signals from the initial SSE GET/POST request.
//   - ClientMessageAction: signals from the companion POST body.
//   - ClientMessageDisconnected: always empty (no request body on disconnect).
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
	Action      string              `json:"action,omitempty"`
	Payload     json.RawMessage     `json:"payload,omitempty"`
	// Signals contains the Datastar signal store values extracted by the relay.
	// Empty map when no signals were sent.
	Signals map[string]any `json:"signals,omitempty"`
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

// sseEvent is the internal payload published to the relay for SSE pushes.
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
type Handler func(ctx context.Context, req *Request, conn *Conn)

// DisconnectHandler is called when an SSE client disconnects (legacy Handle path).
type DisconnectHandler func(note *DisconnectNotification)

// SSEHandler is called for every ClientMessage received on a subscribed SSE path.
type SSEHandler func(ctx context.Context, msg *ClientMessage, conn *Conn)

// ─── WebsiteConfig mirrors config.WebsiteConfig for SDK use ──────────────────

// WebsiteConfig is the configuration that a backend server publishes to the
// NATS KV store so that relay servers can pick it up automatically.
//
// Publish it on startup using client.PublishWebsiteConfig(cfg).
type WebsiteConfig struct {
	WebsiteName           string           `json:"website_name"`
	ApexDomain            string           `json:"apex_domain"`
	RequestTimeoutSeconds int              `json:"request_timeout_seconds"`
	Endpoints             []EndpointConfig `json:"endpoints"`
}

// EndpointConfig describes a single relay endpoint for use in WebsiteConfig.
type EndpointConfig struct {
	Path                string   `json:"path"`
	AllowedMethods      []string `json:"allowed_methods"`
	RequireAuth         bool     `json:"require_auth"`
	NatsSubjectOverride string   `json:"nats_subject_override"`
	TimeoutSeconds      int      `json:"timeout_seconds"`
	IsSSE               bool     `json:"is_sse"`
}

// ─── SDK Client ───────────────────────────────────────────────────────────────

// Client is the SDK entry point. Create one per backend process.
type Client struct {
	nc          *nats.Conn
	js          nats.JetStreamContext
	websiteName string
	subs        []*nats.Subscription
	jsSubs      []*nats.Subscription
}

// Config holds the SDK configuration.
type Config struct {
	// NatsURLs is the list of NATS server URLs to try in order.
	NatsURLs []string

	// WebsiteName must match the relay's website_name for this site.
	WebsiteName string
}

// New connects to NATS, initialises a JetStream context, and returns a ready
// Client.
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
// Call this on backend startup (and whenever the config changes) so that all
// relay servers pick up the config automatically. The relay watches the KV
// store and hot-reloads when a key is updated.
//
// The KV bucket is created automatically if it does not exist.
func (c *Client) PublishWebsiteConfig(cfg WebsiteConfig) error {
	if cfg.WebsiteName == "" {
		return fmt.Errorf("relaysdk: PublishWebsiteConfig: WebsiteName is required")
	}
	if cfg.ApexDomain == "" {
		return fmt.Errorf("relaysdk: PublishWebsiteConfig: ApexDomain is required")
	}

	const bucketName = "relay-website-configs"

	// Ensure the bucket exists.
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

// Handle subscribes to all requests arriving on the given HTTP path for
// standard (non-SSE) endpoints.
func (c *Client) Handle(path string, h Handler, onDisconnect DisconnectHandler) error {
	authedSubject := c.requestSubjectWithUID(path)
	anonSubject := c.requestSubjectNoUID(path)

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

		ctx := context.Background()

		if req.IsSSE() && onDisconnect != nil {
			discSubject := sseDisconnectedSubject(c.websiteName, req.UID, req.ConnUUID)
			discCtx, discCancel := context.WithCancel(ctx)
			conn.discCancel = discCancel

			discSub, err := c.nc.Subscribe(discSubject, func(dm *nats.Msg) {
				var note DisconnectNotification
				if err := json.Unmarshal(dm.Data, &note); err != nil {
					log.Printf("relaysdk: malformed disconnect notification: %v", err)
					return
				}
				discCancel()
				onDisconnect(&note)
			})
			if err != nil {
				log.Printf("relaysdk: failed to subscribe to disconnect subject %q: %v", discSubject, err)
			} else {
				conn.discSub = discSub
				discSub.AutoUnsubscribe(1)
			}
			ctx = discCtx
		} else if req.IsSSE() {
			discSubject := sseDisconnectedSubject(c.websiteName, req.UID, req.ConnUUID)
			discCtx, discCancel := context.WithCancel(ctx)
			conn.discCancel = discCancel

			discSub, err := c.nc.Subscribe(discSubject, func(dm *nats.Msg) {
				discCancel()
			})
			if err != nil {
				log.Printf("relaysdk: failed to subscribe to disconnect subject %q: %v", discSubject, err)
			} else {
				conn.discSub = discSub
				discSub.AutoUnsubscribe(1)
			}
			ctx = discCtx
		}

		go func() {
			defer func() {
				if conn.discSub != nil {
					conn.discSub.Unsubscribe()
				}
				if conn.discCancel != nil {
					conn.discCancel()
				}
			}()
			h(ctx, &req, conn)
		}()
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
	log.Printf("relaysdk: handling path %q → subject %q (JetStream)", path, authedSubject)

	sub2, err := c.js.Subscribe(anonSubject, handler,
		nats.DeliverAll(),
		nats.AckExplicit(),
		nats.MaxDeliver(3),
	)
	if err != nil {
		return fmt.Errorf("relaysdk: subscribe %q: %w", anonSubject, err)
	}
	c.jsSubs = append(c.jsSubs, sub2)
	log.Printf("relaysdk: handling path %q → subject %q (JetStream)", path, anonSubject)

	return nil
}

// HandleSSE subscribes to all ClientMessage events for the given SSE path via
// JetStream.
func (c *Client) HandleSSE(path string, h SSEHandler) error {
	authedSubject := c.clientMessageSubjectWithUID(path)
	anonSubject := c.clientMessageSubjectNoUID(path)

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

// Close drains all subscriptions and closes the NATS connection.
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

func (c *Client) requestSubjectWithUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 4+len(segs))
	parts = append(parts, c.websiteName, "*")
	parts = append(parts, segs...)
	parts = append(parts, "request", "*")
	return strings.Join(parts, ".")
}

func (c *Client) requestSubjectNoUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 3+len(segs))
	parts = append(parts, c.websiteName)
	parts = append(parts, segs...)
	parts = append(parts, "request", "*")
	return strings.Join(parts, ".")
}

func (c *Client) clientMessageSubjectWithUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 4+len(segs))
	parts = append(parts, c.websiteName, "*")
	parts = append(parts, segs...)
	parts = append(parts, "*", "message")
	return strings.Join(parts, ".")
}

func (c *Client) clientMessageSubjectNoUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 3+len(segs))
	parts = append(parts, c.websiteName)
	parts = append(parts, segs...)
	parts = append(parts, "*", "message")
	return strings.Join(parts, ".")
}

// ─── Conn ─────────────────────────────────────────────────────────────────────

// Conn is passed to every Handler and SSEHandler invocation.
type Conn struct {
	nc          *nats.Conn
	js          nats.JetStreamContext
	websiteName string
	req         *Request
	clientMsg   *ClientMessage
	discSub     *nats.Subscription
	discCancel  context.CancelFunc
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

func (c *Conn) reqUUID() string {
	if c.req != nil {
		return c.req.UUID
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

// Respond publishes a RelayResponse for a standard (non-SSE) endpoint.
func (c *Conn) Respond(status int, headers map[string][]string, body json.RawMessage) error {
	if body == nil {
		body = json.RawMessage("null")
	}
	resp := Response{
		UUID:       c.reqUUID(),
		StatusCode: status,
		Headers:    headers,
		Body:       body,
	}
	return c.publishResponse(resp)
}

// RespondError publishes an error response for a standard endpoint.
func (c *Conn) RespondError(status int, errMsg string) error {
	resp := Response{
		UUID:       c.reqUUID(),
		StatusCode: status,
		Error:      errMsg,
	}
	return c.publishResponse(resp)
}

// PatchElements pushes an HTML patch to the SSE connection.
func (c *Conn) PatchElements(html string) error {
	return c.publishSSEEvent(sseEvent{ConnUUID: c.connUUID(), Action: PatchElements, HTML: html})
}

// RemoveElement removes a DOM element by CSS selector on the SSE client.
func (c *Conn) RemoveElement(selector string) error {
	return c.publishSSEEvent(sseEvent{ConnUUID: c.connUUID(), Action: RemoveElement, Selector: selector})
}

// PatchSignals merges signals into the Datastar store on the SSE client.
func (c *Conn) PatchSignals(signals map[string]any) error {
	return c.publishSSEEvent(sseEvent{ConnUUID: c.connUUID(), Action: PatchSignals, Signals: signals})
}

// ExecuteScript runs a script on the SSE client.
func (c *Conn) ExecuteScript(script string) error {
	return c.publishSSEEvent(sseEvent{ConnUUID: c.connUUID(), Action: ExecuteScript, Script: script})
}

// Redirect navigates the SSE client to a new URL.
func (c *Conn) Redirect(url string) error {
	return c.publishSSEEvent(sseEvent{ConnUUID: c.connUUID(), Action: Redirect, URL: url})
}

// ─── "To" variants — push to an explicit connection ──────────────────────────

// PatchElementsTo pushes an HTML patch to a specific SSE connection identified
// by uid and connUUID. Use this to push to another user's open connection.
func (c *Conn) PatchElementsTo(uid, connUUID, html string) error {
	return c.publishSSEEventTo(uid, connUUID, sseEvent{ConnUUID: connUUID, Action: PatchElements, HTML: html})
}

// RemoveElementTo removes a DOM element on a specific SSE connection.
func (c *Conn) RemoveElementTo(uid, connUUID, selector string) error {
	return c.publishSSEEventTo(uid, connUUID, sseEvent{ConnUUID: connUUID, Action: RemoveElement, Selector: selector})
}

// PatchSignalsTo merges signals on a specific SSE connection.
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

// ─── internal helpers ─────────────────────────────────────────────────────────

func (c *Conn) publishResponse(resp Response) error {
	subject := responseSubject(c.websiteName, c.uid(), c.reqPath(), c.reqUUID())
	payload, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("relaysdk: marshal response: %w", err)
	}
	return c.nc.Publish(subject, payload)
}

func (c *Conn) publishSSEEvent(event sseEvent) error {
	subject := sseEventSubject(c.websiteName, c.uid(), c.connUUID())
	return c.jsPublish(subject, event)
}

func (c *Conn) publishSSEEventTo(uid, connUUID string, event sseEvent) error {
	subject := sseEventSubject(c.websiteName, uid, connUUID)
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

// ─── Subject builders ─────────────────────────────────────────────────────────

func responseSubject(website, uid, path, uuid string) string {
	segs := baseSegments(website, uid, path)
	segs = append(segs, "response", uuid)
	return strings.Join(segs, ".")
}

func sseEventSubject(website, uid, connUUID string) string {
	return strings.Join(sseSegments(website, uid, connUUID, "event"), ".")
}

func sseDisconnectedSubject(website, uid, connUUID string) string {
	return strings.Join(sseSegments(website, uid, connUUID, "disconnected"), ".")
}

func sseSegments(website, uid, connUUID, suffix string) []string {
	if uid == "" {
		return []string{website, "sse", connUUID, suffix}
	}
	return []string{website, uid, "sse", connUUID, suffix}
}

func baseSegments(website, uid, path string) []string {
	segs := pathSegments(path)
	var out []string
	if uid == "" {
		out = make([]string, 0, 1+len(segs))
		out = append(out, website)
	} else {
		out = make([]string, 0, 2+len(segs))
		out = append(out, website, uid)
	}
	return append(out, segs...)
}

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
