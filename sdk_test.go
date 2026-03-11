
package relaysdk

import (
	"strings"
	"testing"
)

// ─── Subject layout reference ─────────────────────────────────────────────────
//
// Standard API endpoints (relay → backend via BuildAPIRequestSubject):
//
//	uid present: {website}.{path_segs}.{uid}.{conn_uuid}.{req_uuid}.request
//	uid absent:  {website}.{path_segs}.{conn_uuid}.{req_uuid}.request
//
// SDK wildcard subscriptions (apiRequestSubjectWithUID / apiRequestSubjectNoUID):
//
//	uid present: {website}.{path_segs}.*.*.*.request   (uid + conn_uuid + req_uuid)
//	uid absent:  {website}.{path_segs}.*.*.request     (conn_uuid + req_uuid)
//
// SSE lifecycle events (relay → backend via BuildRequestSubject):
//
//	uid present: {website}.{path_segs}.{uid}.{conn_uuid}.request
//	uid absent:  {website}.{path_segs}.{conn_uuid}.request
//
// SDK wildcard subscriptions (sseLifecycleSubjectWithUID / sseLifecycleSubjectNoUID):
//
//	uid present: {website}.{path_segs}.*.*.request   (uid + conn_uuid)
//	uid absent:  {website}.{path_segs}.*.request     (conn_uuid)
//
// SSE responses (backend → relay via sseResponsesSubject):
//
//	uid present: {website}.{path_segs}.{uid}.{conn_uuid}.responses
//	uid absent:  {website}.{path_segs}.{conn_uuid}.responses
//
// Standard responses (backend → relay via responseSubject):
//
//	uid present: {website}.{path_segs}.{uid}.{req_uuid}.response
//	uid absent:  {website}.{path_segs}.{req_uuid}.response

// ─── sseLifecycleSubjectWithUID ───────────────────────────────────────────────

// TestSSELifecycleSubjectWithUID verifies the wildcard subject used by
// HandleSSE for authenticated SSE connections.
//
// Format: {website}.{path_segs}.*.*.request
// Wildcards match: uid, conn_uuid
func TestSSELifecycleSubjectWithUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	cases := []struct {
		path string
		want string
	}{
		{
			path: "/sse/dashboard",
			want: "battlefrontier.sse.dashboard.*.*.request",
		},
		{
			path: "/sse/login",
			want: "battlefrontier.sse.login.*.*.request",
		},
		{
			path: "/sse/public",
			want: "battlefrontier.sse.public.*.*.request",
		},
		{
			path: "/sse/game/battle",
			want: "battlefrontier.sse.game.battle.*.*.request",
		},
		{
			path: "/sse/v1.2/page",
			want: "battlefrontier.sse.v1_2.page.*.*.request",
		},
		{
			path: "/v0/sse/index",
			want: "battlefrontier.v0.sse.index.*.*.request",
		},
		{
			path: "/v1/sse/dashboard",
			want: "battlefrontier.v1.sse.dashboard.*.*.request",
		},
		{
			path: "/v2/sse/battle",
			want: "battlefrontier.v2.sse.battle.*.*.request",
		},
	}

	for _, tc := range cases {
		got := c.sseLifecycleSubjectWithUID(tc.path)
		if got != tc.want {
			t.Errorf("sseLifecycleSubjectWithUID(%q)\n  got  %q\n  want %q", tc.path, got, tc.want)
		}
	}
}

// ─── sseLifecycleSubjectNoUID ─────────────────────────────────────────────────

// TestSSELifecycleSubjectNoUID verifies the wildcard subject used by
// HandleSSE for unauthenticated SSE connections.
//
// Format: {website}.{path_segs}.*.request
// Wildcard matches: conn_uuid
func TestSSELifecycleSubjectNoUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	cases := []struct {
		path string
		want string
	}{
		{
			path: "/sse/dashboard",
			want: "battlefrontier.sse.dashboard.*.request",
		},
		{
			path: "/sse/login",
			want: "battlefrontier.sse.login.*.request",
		},
		{
			path: "/sse/public",
			want: "battlefrontier.sse.public.*.request",
		},
		{
			path: "/sse/game/battle",
			want: "battlefrontier.sse.game.battle.*.request",
		},
		{
			path: "/sse/v1.2/page",
			want: "battlefrontier.sse.v1_2.page.*.request",
		},
		{
			path: "/v0/sse/index",
			want: "battlefrontier.v0.sse.index.*.request",
		},
		{
			path: "/v1/sse/dashboard",
			want: "battlefrontier.v1.sse.dashboard.*.request",
		},
		{
			path: "/v2/sse/battle",
			want: "battlefrontier.v2.sse.battle.*.request",
		},
	}

	for _, tc := range cases {
		got := c.sseLifecycleSubjectNoUID(tc.path)
		if got != tc.want {
			t.Errorf("sseLifecycleSubjectNoUID(%q)\n  got  %q\n  want %q", tc.path, got, tc.want)
		}
	}
}

// TestSSELifecycleSubject_TerminalTokenIsRequest confirms that the terminal
// token for SSE lifecycle events is "request".
func TestSSELifecycleSubject_TerminalTokenIsRequest(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	gotWith := c.sseLifecycleSubjectWithUID("/v0/sse/dashboard")
	gotWithout := c.sseLifecycleSubjectNoUID("/v0/sse/dashboard")

	for label, s := range map[string]string{"withUID": gotWith, "noUID": gotWithout} {
		if !strings.HasSuffix(s, ".request") {
			t.Errorf("%s: subject %q should end in .request", label, s)
		}
		for _, seg := range splitDot(s) {
			if seg == "message" {
				t.Errorf("%s: subject %q contains legacy 'message' token", label, s)
			}
		}
	}
}

// ─── apiRequestSubjectWithUID / apiRequestSubjectNoUID ────────────────────────

// TestAPIRequestSubjectWithUID verifies the wildcard subject for authenticated
// standard requests.
//
// Format: {website}.{path_segs}.*.*.*.request
// Wildcards match: uid, conn_uuid, req_uuid
func TestAPIRequestSubjectWithUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	cases := []struct {
		path string
		want string
	}{
		{
			path: "/api/login",
			want: "battlefrontier.api.login.*.*.*.request",
		},
		{
			path: "/api/v2/profile/update",
			want: "battlefrontier.api.v2.profile.update.*.*.*.request",
		},
		{
			path: "/api/v1.2/endpoint",
			want: "battlefrontier.api.v1_2.endpoint.*.*.*.request",
		},
		{
			path: "/v0/api/login",
			want: "battlefrontier.v0.api.login.*.*.*.request",
		},
		{
			path: "/v1/api/logout",
			want: "battlefrontier.v1.api.logout.*.*.*.request",
		},
	}

	for _, tc := range cases {
		got := c.apiRequestSubjectWithUID(tc.path)
		if got != tc.want {
			t.Errorf("apiRequestSubjectWithUID(%q)\n  got  %q\n  want %q", tc.path, got, tc.want)
		}
	}
}

// TestAPIRequestSubjectNoUID verifies the wildcard subject for unauthenticated
// standard requests.
//
// Format: {website}.{path_segs}.*.*.request
// Wildcards match: conn_uuid, req_uuid
func TestAPIRequestSubjectNoUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	cases := []struct {
		path string
		want string
	}{
		{
			path: "/api/login",
			want: "battlefrontier.api.login.*.*.request",
		},
		{
			path: "/api/v2/profile/update",
			want: "battlefrontier.api.v2.profile.update.*.*.request",
		},
		{
			path: "/v0/api/login",
			want: "battlefrontier.v0.api.login.*.*.request",
		},
		{
			path: "/v1/api/logout",
			want: "battlefrontier.v1.api.logout.*.*.request",
		},
	}

	for _, tc := range cases {
		got := c.apiRequestSubjectNoUID(tc.path)
		if got != tc.want {
			t.Errorf("apiRequestSubjectNoUID(%q)\n  got  %q\n  want %q", tc.path, got, tc.want)
		}
	}
}

// ─── responseSubject ──────────────────────────────────────────────────────────

// TestResponseSubject verifies the response subject builder.
//
// Format (uid present): {website}.{path_segs}.{uid}.{req_uuid}.response
// Format (uid absent):  {website}.{path_segs}.{req_uuid}.response
func TestResponseSubject(t *testing.T) {
	cases := []struct {
		website string
		uid     string
		path    string
		reqUUID string
		want    string
	}{
		{
			website: "battlefrontier",
			uid:     "user123",
			path:    "/api/login",
			reqUUID: "req-1",
			want:    "battlefrontier.api.login.user123.req-1.response",
		},
		{
			website: "battlefrontier",
			uid:     "",
			path:    "/api/login",
			reqUUID: "req-1",
			want:    "battlefrontier.api.login.req-1.response",
		},
		{
			website: "mysite",
			uid:     "u1",
			path:    "/api/v2/profile/update",
			reqUUID: "req-2",
			want:    "mysite.api.v2.profile.update.u1.req-2.response",
		},
		{
			website: "battlefrontier",
			uid:     "user123",
			path:    "/v0/api/logout",
			reqUUID: "req-3",
			want:    "battlefrontier.v0.api.logout.user123.req-3.response",
		},
		{
			website: "battlefrontier",
			uid:     "",
			path:    "/v0/api/login",
			reqUUID: "req-4",
			want:    "battlefrontier.v0.api.login.req-4.response",
		},
	}

	for _, tc := range cases {
		got := responseSubject(tc.website, tc.uid, tc.path, tc.reqUUID)
		if got != tc.want {
			t.Errorf("responseSubject(%q, %q, %q, %q)\n  got  %q\n  want %q",
				tc.website, tc.uid, tc.path, tc.reqUUID, got, tc.want)
		}
	}
}

// ─── sseResponsesSubject ──────────────────────────────────────────────────────

// TestSSEResponsesSubject verifies the SSE responses subject builder.
//
// Format (uid present): {website}.{path_segs}.{uid}.{conn_uuid}.responses
// Format (uid absent):  {website}.{path_segs}.{conn_uuid}.responses
func TestSSEResponsesSubject(t *testing.T) {
	cases := []struct {
		website  string
		uid      string
		path     string
		connUUID string
		want     string
	}{
		{
			website:  "battlefrontier",
			uid:      "user123",
			path:     "/v0/sse/dashboard",
			connUUID: "conn-1",
			want:     "battlefrontier.v0.sse.dashboard.user123.conn-1.responses",
		},
		{
			website:  "battlefrontier",
			uid:      "",
			path:     "/v0/sse/index",
			connUUID: "conn-1",
			want:     "battlefrontier.v0.sse.index.conn-1.responses",
		},
		{
			website:  "battlefrontier",
			uid:      "user123",
			path:     "/sse/dashboard",
			connUUID: "conn-2",
			want:     "battlefrontier.sse.dashboard.user123.conn-2.responses",
		},
		{
			website:  "battlefrontier",
			uid:      "",
			path:     "/sse/dashboard",
			connUUID: "conn-3",
			want:     "battlefrontier.sse.dashboard.conn-3.responses",
		},
	}

	for _, tc := range cases {
		got := sseResponsesSubject(tc.website, tc.uid, tc.path, tc.connUUID)
		if got != tc.want {
			t.Errorf("sseResponsesSubject(%q, %q, %q, %q)\n  got  %q\n  want %q",
				tc.website, tc.uid, tc.path, tc.connUUID, got, tc.want)
		}
	}
}

// TestSSEResponsesSubject_PluralToken confirms the terminal token is "responses".
func TestSSEResponsesSubject_PluralToken(t *testing.T) {
	got := sseResponsesSubject("battlefrontier", "user123", "/v0/sse/dashboard", "conn-1")
	segs := splitDot(got)
	last := segs[len(segs)-1]
	if last != "responses" {
		t.Errorf("sseResponsesSubject terminal token: got %q, want %q", last, "responses")
	}
}

// ─── apiSegments ──────────────────────────────────────────────────────────────

// TestAPISegments verifies that uid comes after path segments in the slice.
func TestAPISegments(t *testing.T) {
	cases := []struct {
		website string
		uid     string
		path    string
		want    []string
	}{
		{
			website: "battlefrontier",
			uid:     "user123",
			path:    "/v0/sse/dashboard",
			want:    []string{"battlefrontier", "v0", "sse", "dashboard", "user123"},
		},
		{
			website: "battlefrontier",
			uid:     "",
			path:    "/v0/sse/dashboard",
			want:    []string{"battlefrontier", "v0", "sse", "dashboard"},
		},
		{
			website: "mysite",
			uid:     "u1",
			path:    "/api/v2/profile",
			want:    []string{"mysite", "api", "v2", "profile", "u1"},
		},
		{
			website: "battlefrontier",
			uid:     "user123",
			path:    "/v0/api/logout",
			want:    []string{"battlefrontier", "v0", "api", "logout", "user123"},
		},
		{
			website: "battlefrontier",
			uid:     "",
			path:    "/v0/api/login",
			want:    []string{"battlefrontier", "v0", "api", "login"},
		},
	}

	for _, tc := range cases {
		got := apiSegments(tc.website, tc.uid, tc.path)
		if len(got) != len(tc.want) {
			t.Errorf("apiSegments(%q, %q, %q) len: got %d %v, want %d %v",
				tc.website, tc.uid, tc.path, len(got), got, len(tc.want), tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("apiSegments(%q, %q, %q)[%d]: got %q, want %q",
					tc.website, tc.uid, tc.path, i, got[i], tc.want[i])
			}
		}
	}
}

// ─── pathSegments ─────────────────────────────────────────────────────────────

// TestPathSegments verifies the path segment splitter.
func TestPathSegments(t *testing.T) {
	cases := []struct {
		path string
		want []string
	}{
		{"/api/login", []string{"api", "login"}},
		{"/sse/dashboard", []string{"sse", "dashboard"}},
		{"/sse/game/battle", []string{"sse", "game", "battle"}},
		{"/api/v1.2/endpoint", []string{"api", "v1_2", "endpoint"}},
		{"/api/login/", []string{"api", "login"}},
		{"api/login", []string{"api", "login"}},
		{"/", []string{}},
		{"", []string{}},
		{"/v0/sse/index", []string{"v0", "sse", "index"}},
		{"/v1/api/login", []string{"v1", "api", "login"}},
		{"/v10/sse/dashboard", []string{"v10", "sse", "dashboard"}},
	}

	for _, tc := range cases {
		got := pathSegments(tc.path)
		if len(got) != len(tc.want) {
			t.Errorf("pathSegments(%q) len: got %d %v, want %d %v",
				tc.path, len(got), got, len(tc.want), tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("pathSegments(%q)[%d]: got %q, want %q", tc.path, i, got[i], tc.want[i])
			}
		}
	}
}

// ─── baseSegments (alias) ─────────────────────────────────────────────────────

// TestBaseSegments verifies the baseSegments alias behaves identically to
// apiSegments.
func TestBaseSegments(t *testing.T) {
	cases := []struct {
		website string
		uid     string
		path    string
		want    []string
	}{
		{
			website: "battlefrontier",
			uid:     "user123",
			path:    "/sse/dashboard",
			want:    []string{"battlefrontier", "sse", "dashboard", "user123"},
		},
		{
			website: "battlefrontier",
			uid:     "",
			path:    "/sse/dashboard",
			want:    []string{"battlefrontier", "sse", "dashboard"},
		},
		{
			website: "mysite",
			uid:     "u1",
			path:    "/api/v2/profile",
			want:    []string{"mysite", "api", "v2", "profile", "u1"},
		},
		{
			website: "battlefrontier",
			uid:     "user123",
			path:    "/v0/sse/index",
			want:    []string{"battlefrontier", "v0", "sse", "index", "user123"},
		},
		{
			website: "battlefrontier",
			uid:     "",
			path:    "/v0/sse/index",
			want:    []string{"battlefrontier", "v0", "sse", "index"},
		},
	}

	for _, tc := range cases {
		got := baseSegments(tc.website, tc.uid, tc.path)
		if len(got) != len(tc.want) {
			t.Errorf("baseSegments(%q, %q, %q) len: got %d %v, want %d %v",
				tc.website, tc.uid, tc.path, len(got), got, len(tc.want), tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("baseSegments(%q, %q, %q)[%d]: got %q, want %q",
					tc.website, tc.uid, tc.path, i, got[i], tc.want[i])
			}
		}
	}
}

// ─── All configured endpoints ─────────────────────────────────────────────────

// TestAllConfiguredEndpoints_SSELifecycleSubjects verifies the wildcard
// subjects the SDK subscribes to for every SSE endpoint in battlefrontier.json.
func TestAllConfiguredEndpoints_SSELifecycleSubjects(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	cases := []struct {
		path           string
		wantWithUID    string
		wantWithoutUID string
	}{
		{
			path:           "/v0/sse/index",
			wantWithUID:    "battlefrontier.v0.sse.index.*.*.request",
			wantWithoutUID: "battlefrontier.v0.sse.index.*.request",
		},
		{
			path:           "/v0/sse/dashboard",
			wantWithUID:    "battlefrontier.v0.sse.dashboard.*.*.request",
			wantWithoutUID: "battlefrontier.v0.sse.dashboard.*.request",
		},
		{
			path:           "/v0/sse/login",
			wantWithUID:    "battlefrontier.v0.sse.login.*.*.request",
			wantWithoutUID: "battlefrontier.v0.sse.login.*.request",
		},
	}

	for _, tc := range cases {
		gotWith := c.sseLifecycleSubjectWithUID(tc.path)
		if gotWith != tc.wantWithUID {
			t.Errorf("sseLifecycleSubjectWithUID(%q)\n  got  %q\n  want %q",
				tc.path, gotWith, tc.wantWithUID)
		}

		gotWithout := c.sseLifecycleSubjectNoUID(tc.path)
		if gotWithout != tc.wantWithoutUID {
			t.Errorf("sseLifecycleSubjectNoUID(%q)\n  got  %q\n  want %q",
				tc.path, gotWithout, tc.wantWithoutUID)
		}
	}
}

// ─── ClientMessageType constants ──────────────────────────────────────────────

// TestClientMessageType_Constants verifies the string values of the
// ClientMessageType constants match what the relay publishes.
func TestClientMessageType_Constants(t *testing.T) {
	if ClientMessageConnected != "connected" {
		t.Errorf("ClientMessageConnected: got %q, want %q", ClientMessageConnected, "connected")
	}
	if ClientMessageAction != "action" {
		t.Errorf("ClientMessageAction: got %q, want %q", ClientMessageAction, "action")
	}
	if ClientMessageDisconnected != "disconnected" {
		t.Errorf("ClientMessageDisconnected: got %q, want %q", ClientMessageDisconnected, "disconnected")
	}
}

// ─── Versioned vs unversioned ─────────────────────────────────────────────────

// TestVersionedVsUnversionedSubjects confirms that versioned and unversioned
// paths produce distinct subjects.
func TestVersionedVsUnversionedSubjects(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	v0WithUID := c.sseLifecycleSubjectWithUID("/v0/sse/login")
	noVerWithUID := c.sseLifecycleSubjectWithUID("/sse/login")

	if v0WithUID == noVerWithUID {
		t.Errorf("versioned and unversioned paths produced identical subjects: %q", v0WithUID)
	}
	if v0WithUID != "battlefrontier.v0.sse.login.*.*.request" {
		t.Errorf("v0 withUID: got %q, want %q", v0WithUID, "battlefrontier.v0.sse.login.*.*.request")
	}
	if noVerWithUID != "battlefrontier.sse.login.*.*.request" {
		t.Errorf("noVer withUID: got %q, want %q", noVerWithUID, "battlefrontier.sse.login.*.*.request")
	}

	v0NoUID := c.sseLifecycleSubjectNoUID("/v0/sse/login")
	noVerNoUID := c.sseLifecycleSubjectNoUID("/sse/login")

	if v0NoUID == noVerNoUID {
		t.Errorf("versioned and unversioned paths produced identical subjects (no uid): %q", v0NoUID)
	}
	if v0NoUID != "battlefrontier.v0.sse.login.*.request" {
		t.Errorf("v0 noUID: got %q, want %q", v0NoUID, "battlefrontier.v0.sse.login.*.request")
	}
	if noVerNoUID != "battlefrontier.sse.login.*.request" {
		t.Errorf("noVer noUID: got %q, want %q", noVerNoUID, "battlefrontier.sse.login.*.request")
	}
}

// ─── Subject symmetry: relay publish ↔ SDK subscribe ─────────────────────────

// TestSubjectSymmetry_StandardRequestWithUID verifies that the relay's publish
// subject for an authenticated standard request is matched by the SDK's
// wildcard subscription.
//
// Relay publishes (BuildAPIRequestSubject):
//
//	battlefrontier.v0.api.login.user123.{conn_uuid}.{req_uuid}.request
//
// SDK subscribes (apiRequestSubjectWithUID):
//
//	battlefrontier.v0.api.login.*.*.*.request
func TestSubjectSymmetry_StandardRequestWithUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	relayPublish := "battlefrontier.v0.api.login.user123.conn-abc.req-123.request"
	sdkSubscribe := c.apiRequestSubjectWithUID("/v0/api/login")

	if !natsWildcardMatch(sdkSubscribe, relayPublish) {
		t.Errorf("SDK subscription %q does not match relay publish %q", sdkSubscribe, relayPublish)
	}
}

// TestSubjectSymmetry_StandardRequestNoUID verifies the unauthenticated case.
//
// Relay publishes (BuildAPIRequestSubject):
//
//	battlefrontier.v0.api.login.{conn_uuid}.{req_uuid}.request
//
// SDK subscribes (apiRequestSubjectNoUID):
//
//	battlefrontier.v0.api.login.*.*.request
func TestSubjectSymmetry_StandardRequestNoUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	relayPublish := "battlefrontier.v0.api.login.conn-abc.req-123.request"
	sdkSubscribe := c.apiRequestSubjectNoUID("/v0/api/login")

	if !natsWildcardMatch(sdkSubscribe, relayPublish) {
		t.Errorf("SDK subscription %q does not match relay publish %q", sdkSubscribe, relayPublish)
	}
}

// TestSubjectSymmetry_SSELifecycleWithUID verifies that the relay's lifecycle
// publish subject for an authenticated SSE connection is matched by the SDK's
// HandleSSE subscription.
//
// Relay publishes (BuildRequestSubject):
//
//	battlefrontier.v0.sse.dashboard.user123.{conn_uuid}.request
//
// SDK subscribes (sseLifecycleSubjectWithUID):
//
//	battlefrontier.v0.sse.dashboard.*.*.request
func TestSubjectSymmetry_SSELifecycleWithUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	relayPublish := "battlefrontier.v0.sse.dashboard.user123.conn-uuid-here.request"
	sdkSubscribe := c.sseLifecycleSubjectWithUID("/v0/sse/dashboard")

	if !natsWildcardMatch(sdkSubscribe, relayPublish) {
		t.Errorf("SDK subscription %q does not match relay publish %q", sdkSubscribe, relayPublish)
	}
}

// TestSubjectSymmetry_SSELifecycleNoUID verifies the unauthenticated SSE case.
//
// Relay publishes (BuildRequestSubject):
//
//	battlefrontier.v0.sse.index.{conn_uuid}.request
//
// SDK subscribes (sseLifecycleSubjectNoUID):
//
//	battlefrontier.v0.sse.index.*.request
func TestSubjectSymmetry_SSELifecycleNoUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	relayPublish := "battlefrontier.v0.sse.index.conn-uuid-here.request"
	sdkSubscribe := c.sseLifecycleSubjectNoUID("/v0/sse/index")

	if !natsWildcardMatch(sdkSubscribe, relayPublish) {
		t.Errorf("SDK subscription %q does not match relay publish %q", sdkSubscribe, relayPublish)
	}
}

// TestSubjectSymmetry_SSEResponses verifies that the backend publishes SSE
// events to the exact subject the relay subscribes to.
//
// Backend publishes (sseResponsesSubject):
//
//	battlefrontier.v0.sse.dashboard.user123.{conn_uuid}.responses
//
// Relay subscribes (BuildSSEResponsesSubject — exact, no wildcard):
//
//	battlefrontier.v0.sse.dashboard.user123.{conn_uuid}.responses
func TestSubjectSymmetry_SSEResponses(t *testing.T) {
	connUUID := "conn-abc-123"
	uid := "user123"
	path := "/v0/sse/dashboard"
	website := "battlefrontier"

	backendPublish := sseResponsesSubject(website, uid, path, connUUID)
	want := "battlefrontier.v0.sse.dashboard.user123.conn-abc-123.responses"

	if backendPublish != want {
		t.Errorf("sseResponsesSubject\n  got  %q\n  want %q", backendPublish, want)
	}
}

// TestSubjectSymmetry_StandardResponse verifies that the backend publishes
// standard responses to the correct subject.
//
// Backend publishes (responseSubject):
//
//	battlefrontier.v0.api.login.user123.{req_uuid}.response
//
// Relay subscribes (BuildResponseSubject — exact, no wildcard):
//
//	battlefrontier.v0.api.login.user123.{req_uuid}.response
func TestSubjectSymmetry_StandardResponse(t *testing.T) {
	reqUUID := "req-uuid-123"
	uid := "user123"
	path := "/v0/api/login"
	website := "battlefrontier"

	backendPublish := responseSubject(website, uid, path, reqUUID)
	want := "battlefrontier.v0.api.login.user123.req-uuid-123.response"

	if backendPublish != want {
		t.Errorf("responseSubject\n  got  %q\n  want %q", backendPublish, want)
	}
}

// ─── Test helpers ─────────────────────────────────────────────────────────────

// splitDot splits a NATS subject on ".".
func splitDot(s string) []string {
	return strings.Split(s, ".")
}

// natsWildcardMatch checks whether a NATS subject pattern (which may contain
// "*" single-token wildcards) matches a concrete subject. Simplified matcher
// for test purposes — does not support ">" multi-token wildcards.
func natsWildcardMatch(pattern, subject string) bool {
	pp := splitDot(pattern)
	sp := splitDot(subject)
	if len(pp) != len(sp) {
		return false
	}
	for i := range pp {
		if pp[i] == "*" {
			continue
		}
		if pp[i] != sp[i] {
			return false
		}
	}
	return true
}
