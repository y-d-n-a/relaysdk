
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
}

// IsSSE returns true when this request opened an SSE connection.
func (r *Request) IsSSE() bool {
	return r.ConnUUID != ""
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
	// ClientMessageConnected is published when the browser opens the SSE connection.
	ClientMessageConnected ClientMessageType = "connected"

	// ClientMessageAction is published when the browser POSTs to /sse/{page}/message.
	ClientMessageAction ClientMessageType = "action"

	// ClientMessageDisconnected is published when the browser closes the SSE connection.
	ClientMessageDisconnected ClientMessageType = "disconnected"
)

// ClientMessage is the single NATS message type for all SSE lifecycle events
// and browser-initiated actions under the new per-page endpoint model.
//
// NATS subject (uid present): {website}.{uid}.{pathSegments}.{conn_uuid}.message
// NATS subject (uid absent):  {website}.{pathSegments}.{conn_uuid}.message
//
// The full path — including any version prefix — is used directly as path
// segments. No version stripping or injection is performed. A path of
// /v0/sse/index produces segments v0.sse.index. A path of /sse/dashboard
// produces segments sse.dashboard.
//
// Example for /v0/sse/index, uid absent:
//
//	battlefrontier.v0.sse.index.{conn_uuid}.message
//
// Example for /v1/sse/dashboard, uid present:
//
//	battlefrontier.user123.v1.sse.dashboard.{conn_uuid}.message
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
// For standard endpoints: call conn.Respond(...) exactly once.
type Handler func(ctx context.Context, req *Request, conn *Conn)

// DisconnectHandler is called when an SSE client disconnects (legacy Handle path).
type DisconnectHandler func(note *DisconnectNotification)

// SSEHandler is called for every ClientMessage received on a subscribed SSE path.
// msg.Type distinguishes connection lifecycle events from action messages.
// msg.Action identifies the specific operation the browser requested.
// The context passed to the connected handler invocation is cancelled when a
// disconnected message arrives for the same conn_uuid.
type SSEHandler func(ctx context.Context, msg *ClientMessage, conn *Conn)

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

// Handle subscribes to all requests arriving on the given HTTP path for
// standard (non-SSE) endpoints.
//
// The full path — including any version prefix — is used directly as path
// segments in the NATS subject wildcards. No version extraction or injection
// is performed. The path /v0/api/logout produces segments v0.api.logout.
//
// Two NATS subscriptions are created per path:
//   - {website}.*.{pathSegments}.request.*  — authenticated (uid present)
//   - {website}.{pathSegments}.request.*    — unauthenticated (uid absent)
//
// Example for /v0/api/logout:
//
//	battlefrontier.*.v0.api.logout.request.*
//	battlefrontier.v0.api.logout.request.*
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
//
// The full path — including any version prefix — is used directly as path
// segments in the NATS subject wildcards. No version extraction or injection
// is performed. The path /v0/sse/index produces segments v0.sse.index.
//
// Two NATS wildcard subscriptions are created per path:
//   - {website}.*.{pathSegments}.*.message  — authenticated (uid present)
//   - {website}.{pathSegments}.*.message    — unauthenticated (uid absent)
//
// Example for /v0/sse/index:
//
//	battlefrontier.*.v0.sse.index.*.message   (authenticated)
//	battlefrontier.v0.sse.index.*.message     (unauthenticated)
//
// Example for /sse/dashboard (no version):
//
//	battlefrontier.*.sse.dashboard.*.message  (authenticated)
//	battlefrontier.sse.dashboard.*.message    (unauthenticated)
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

// requestSubjectWithUID returns the NATS wildcard subject for authenticated
// standard requests.
//
// The full path is used directly — no version extraction. A path of
// /v0/api/login produces v0.api.login as the path segments.
//
// Format: {website}.*.{pathSegments}.request.*
func (c *Client) requestSubjectWithUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 4+len(segs))
	parts = append(parts, c.websiteName, "*")
	parts = append(parts, segs...)
	parts = append(parts, "request", "*")
	return strings.Join(parts, ".")
}

// requestSubjectNoUID returns the NATS wildcard subject for unauthenticated
// standard requests.
//
// The full path is used directly — no version extraction.
//
// Format: {website}.{pathSegments}.request.*
func (c *Client) requestSubjectNoUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 3+len(segs))
	parts = append(parts, c.websiteName)
	parts = append(parts, segs...)
	parts = append(parts, "request", "*")
	return strings.Join(parts, ".")
}

// clientMessageSubjectWithUID returns the NATS wildcard subject for
// authenticated SSE page connections (uid present).
//
// The full path is used directly — no version extraction. A path of
// /v0/sse/index produces v0.sse.index as the path segments.
//
// Format: {website}.*.{pathSegments}.*.message
func (c *Client) clientMessageSubjectWithUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 4+len(segs))
	parts = append(parts, c.websiteName, "*")
	parts = append(parts, segs...)
	parts = append(parts, "*", "message")
	return strings.Join(parts, ".")
}

// clientMessageSubjectNoUID returns the NATS wildcard subject for
// unauthenticated SSE page connections (uid absent).
//
// The full path is used directly — no version extraction.
//
// Format: {website}.{pathSegments}.*.message
func (c *Client) clientMessageSubjectNoUID(path string) string {
	segs := pathSegments(path)
	parts := make([]string, 0, 3+len(segs))
	parts = append(parts, c.websiteName)
	parts = append(parts, segs...)
	parts = append(parts, "*", "message")
	return strings.Join(parts, ".")
}

// ─── Conn — per-request / per-message connection handle ──────────────────────

// Conn is passed to every Handler and SSEHandler invocation.
type Conn struct {
	nc          *nats.Conn
	js          nats.JetStreamContext
	websiteName string
	req         *Request       // set for legacy Handle model
	clientMsg   *ClientMessage // set for new HandleSSE model
	discSub     *nats.Subscription
	discCancel  context.CancelFunc
}

// connUUID returns the connection UUID regardless of which model is in use.
func (c *Conn) connUUID() string {
	if c.clientMsg != nil {
		return c.clientMsg.ConnUUID
	}
	if c.req != nil {
		return c.req.ConnUUID
	}
	return ""
}

// uid returns the user ID regardless of which model is in use.
func (c *Conn) uid() string {
	if c.clientMsg != nil {
		return c.clientMsg.UID
	}
	if c.req != nil {
		return c.req.UID
	}
	return ""
}

// reqUUID returns the relay request UUID (standard endpoints only).
func (c *Conn) reqUUID() string {
	if c.req != nil {
		return c.req.UUID
	}
	return ""
}

// reqPath returns the HTTP path regardless of which model is in use.
func (c *Conn) reqPath() string {
	if c.clientMsg != nil {
		return c.clientMsg.Path
	}
	if c.req != nil {
		return c.req.Path
	}
	return ""
}

// Respond publishes a RelayResponse for a standard (non-SSE) endpoint via
// core NATS.
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

// RespondError publishes an error response for a standard endpoint via core
// NATS.
func (c *Conn) RespondError(status int, errMsg string) error {
	resp := Response{
		UUID:       c.reqUUID(),
		StatusCode: status,
		Error:      errMsg,
	}
	return c.publishResponse(resp)
}

// PatchElements pushes an HTML patch to the SSE connection via JetStream.
func (c *Conn) PatchElements(html string) error {
	return c.publishSSEEvent(sseEvent{
		ConnUUID: c.connUUID(),
		Action:   PatchElements,
		HTML:     html,
	})
}

// RemoveElement removes a DOM element by CSS selector on the SSE client via
// JetStream.
func (c *Conn) RemoveElement(selector string) error {
	return c.publishSSEEvent(sseEvent{
		ConnUUID: c.connUUID(),
		Action:   RemoveElement,
		Selector: selector,
	})
}

// PatchSignals merges signals into the Datastar store on the SSE client via
// JetStream.
func (c *Conn) PatchSignals(signals map[string]any) error {
	return c.publishSSEEvent(sseEvent{
		ConnUUID: c.connUUID(),
		Action:   PatchSignals,
		Signals:  signals,
	})
}

// ExecuteScript runs a script on the SSE client via JetStream.
func (c *Conn) ExecuteScript(script string) error {
	return c.publishSSEEvent(sseEvent{
		ConnUUID: c.connUUID(),
		Action:   ExecuteScript,
		Script:   script,
	})
}

// Redirect navigates the SSE client to a new URL via JetStream.
func (c *Conn) Redirect(url string) error {
	return c.publishSSEEvent(sseEvent{
		ConnUUID: c.connUUID(),
		Action:   Redirect,
		URL:      url,
	})
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

// responseSubject builds the NATS subject the relay listens on for the backend
// reply to a standard request.
//
// The full path is used directly as path segments. No version extraction.
//
// Format (uid present): {website}.{uid}.{pathSegments}.response.{uuid}
// Format (uid absent):  {website}.{pathSegments}.response.{uuid}
func responseSubject(website, uid, path, uuid string) string {
	segs := baseSegments(website, uid, path)
	segs = append(segs, "response", uuid)
	return strings.Join(segs, ".")
}

// sseEventSubject builds the NATS subject the relay subscribes to in order to
// receive Datastar events the backend wants pushed to the client.
//
// The SSE event subject does NOT include the path or version. It is keyed only
// by website, uid, and conn_uuid. The conn_uuid is globally unique (UUID v4)
// so no path disambiguation is needed.
//
// Format (uid present): {website}.{uid}.sse.{conn_uuid}.event
// Format (uid absent):  {website}.sse.{conn_uuid}.event
func sseEventSubject(website, uid, connUUID string) string {
	return strings.Join(sseSegments(website, uid, connUUID, "event"), ".")
}

// sseDisconnectedSubject builds the legacy disconnect notification subject.
//
// Format (uid present): {website}.{uid}.sse.{conn_uuid}.disconnected
// Format (uid absent):  {website}.sse.{conn_uuid}.disconnected
func sseDisconnectedSubject(website, uid, connUUID string) string {
	return strings.Join(sseSegments(website, uid, connUUID, "disconnected"), ".")
}

// sseSegments builds the segment slice for SSE event/connected/disconnected
// subjects, omitting uid when empty.
func sseSegments(website, uid, connUUID, suffix string) []string {
	if uid == "" {
		return []string{website, "sse", connUUID, suffix}
	}
	return []string{website, uid, "sse", connUUID, suffix}
}

// baseSegments builds the common website+uid+path portion shared by subject
// builders. When uid is empty it is omitted. The full path — including any
// version prefix — is split on "/" and each non-empty segment is included.
// Dots in path segments are replaced with underscores so they do not break
// NATS subject parsing.
//
// This mirrors the relay's internal/relay/subject.go baseSegments exactly.
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

// pathSegments splits a URL path on "/" and returns the non-empty segments
// with dots replaced by underscores. The full path is used as-is — no version
// extraction or stripping is performed.
//
// Examples:
//
//	/v0/sse/index     → ["v0", "sse", "index"]
//	/sse/dashboard    → ["sse", "dashboard"]
//	/v1/api/login     → ["v1", "api", "login"]
//	/api/v1.2/ep      → ["api", "v1_2", "ep"]
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
