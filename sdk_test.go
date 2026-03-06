
package relaysdk

import (
	"testing"
)

// TestClientMessageSubjectWithUID verifies the wildcard subject used by
// HandleSSE for authenticated connections.
//
// The full path is used directly as path segments. No version extraction.
//
// Format: {website}.*.{pathSegments}.*.message
//
// Examples:
//
//	/sse/dashboard  → battlefrontier.*.sse.dashboard.*.message
//	/v0/sse/index   → battlefrontier.*.v0.sse.index.*.message
func TestClientMessageSubjectWithUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	cases := []struct {
		path string
		want string
	}{
		{
			path: "/sse/dashboard",
			want: "battlefrontier.*.sse.dashboard.*.message",
		},
		{
			path: "/sse/login",
			want: "battlefrontier.*.sse.login.*.message",
		},
		{
			path: "/sse/public",
			want: "battlefrontier.*.sse.public.*.message",
		},
		{
			path: "/sse/game/battle",
			want: "battlefrontier.*.sse.game.battle.*.message",
		},
		{
			path: "/sse/v1.2/page",
			want: "battlefrontier.*.sse.v1_2.page.*.message",
		},
		// Versioned paths — version segment is part of the path, not injected separately.
		{
			path: "/v0/sse/index",
			want: "battlefrontier.*.v0.sse.index.*.message",
		},
		{
			path: "/v1/sse/dashboard",
			want: "battlefrontier.*.v1.sse.dashboard.*.message",
		},
		{
			path: "/v2/sse/battle",
			want: "battlefrontier.*.v2.sse.battle.*.message",
		},
	}

	for _, tc := range cases {
		got := c.clientMessageSubjectWithUID(tc.path)
		if got != tc.want {
			t.Errorf("clientMessageSubjectWithUID(%q)\n  got  %q\n  want %q", tc.path, got, tc.want)
		}
	}
}

// TestClientMessageSubjectNoUID verifies the wildcard subject used by
// HandleSSE for unauthenticated connections.
//
// The full path is used directly as path segments. No version extraction.
//
// Format: {website}.{pathSegments}.*.message
//
// Examples:
//
//	/sse/dashboard  → battlefrontier.sse.dashboard.*.message
//	/v0/sse/index   → battlefrontier.v0.sse.index.*.message
func TestClientMessageSubjectNoUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	cases := []struct {
		path string
		want string
	}{
		{
			path: "/sse/dashboard",
			want: "battlefrontier.sse.dashboard.*.message",
		},
		{
			path: "/sse/login",
			want: "battlefrontier.sse.login.*.message",
		},
		{
			path: "/sse/public",
			want: "battlefrontier.sse.public.*.message",
		},
		{
			path: "/sse/game/battle",
			want: "battlefrontier.sse.game.battle.*.message",
		},
		{
			path: "/sse/v1.2/page",
			want: "battlefrontier.sse.v1_2.page.*.message",
		},
		// Versioned paths — version segment is part of the path, not injected separately.
		{
			path: "/v0/sse/index",
			want: "battlefrontier.v0.sse.index.*.message",
		},
		{
			path: "/v1/sse/dashboard",
			want: "battlefrontier.v1.sse.dashboard.*.message",
		},
		{
			path: "/v2/sse/battle",
			want: "battlefrontier.v2.sse.battle.*.message",
		},
	}

	for _, tc := range cases {
		got := c.clientMessageSubjectNoUID(tc.path)
		if got != tc.want {
			t.Errorf("clientMessageSubjectNoUID(%q)\n  got  %q\n  want %q", tc.path, got, tc.want)
		}
	}
}

// TestClientMessageSubject_NoExtraSSEToken is a regression test confirming
// that the old format with a redundant sse token is not produced.
func TestClientMessageSubject_NoExtraSSEToken(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	got := c.clientMessageSubjectWithUID("/sse/dashboard")
	wrong := "battlefrontier.*.sse.dashboard.sse.*.message"
	correct := "battlefrontier.*.sse.dashboard.*.message"

	if got == wrong {
		t.Errorf("clientMessageSubjectWithUID produced old format with redundant sse token:\n  got %q", got)
	}
	if got != correct {
		t.Errorf("clientMessageSubjectWithUID\n  got  %q\n  want %q", got, correct)
	}
}

// TestClientMessageSubject_NoExtraSSEToken_NoUID covers the unauthenticated
// variant of the same regression.
func TestClientMessageSubject_NoExtraSSEToken_NoUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	got := c.clientMessageSubjectNoUID("/sse/dashboard")
	wrong := "battlefrontier.sse.dashboard.sse.*.message"
	correct := "battlefrontier.sse.dashboard.*.message"

	if got == wrong {
		t.Errorf("clientMessageSubjectNoUID produced old format with redundant sse token:\n  got %q", got)
	}
	if got != correct {
		t.Errorf("clientMessageSubjectNoUID\n  got  %q\n  want %q", got, correct)
	}
}

// TestRequestSubjectWithUID verifies the wildcard subject for authenticated
// standard requests.
//
// The full path is used directly. No version extraction.
//
// Format: {website}.*.{pathSegments}.request.*
func TestRequestSubjectWithUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	cases := []struct {
		path string
		want string
	}{
		{
			path: "/api/login",
			want: "battlefrontier.*.api.login.request.*",
		},
		{
			path: "/api/v2/profile/update",
			want: "battlefrontier.*.api.v2.profile.update.request.*",
		},
		{
			path: "/api/v1.2/endpoint",
			want: "battlefrontier.*.api.v1_2.endpoint.request.*",
		},
		// Versioned paths — version is part of the segments, not injected.
		{
			path: "/v0/api/login",
			want: "battlefrontier.*.v0.api.login.request.*",
		},
		{
			path: "/v1/api/logout",
			want: "battlefrontier.*.v1.api.logout.request.*",
		},
	}

	for _, tc := range cases {
		got := c.requestSubjectWithUID(tc.path)
		if got != tc.want {
			t.Errorf("requestSubjectWithUID(%q)\n  got  %q\n  want %q", tc.path, got, tc.want)
		}
	}
}

// TestRequestSubjectNoUID verifies the wildcard subject for unauthenticated
// standard requests.
//
// The full path is used directly. No version extraction.
//
// Format: {website}.{pathSegments}.request.*
func TestRequestSubjectNoUID(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	cases := []struct {
		path string
		want string
	}{
		{
			path: "/api/login",
			want: "battlefrontier.api.login.request.*",
		},
		{
			path: "/api/v2/profile/update",
			want: "battlefrontier.api.v2.profile.update.request.*",
		},
		// Versioned paths — version is part of the segments, not injected.
		{
			path: "/v0/api/login",
			want: "battlefrontier.v0.api.login.request.*",
		},
		{
			path: "/v1/api/logout",
			want: "battlefrontier.v1.api.logout.request.*",
		},
	}

	for _, tc := range cases {
		got := c.requestSubjectNoUID(tc.path)
		if got != tc.want {
			t.Errorf("requestSubjectNoUID(%q)\n  got  %q\n  want %q", tc.path, got, tc.want)
		}
	}
}

// TestResponseSubject verifies the response subject builder.
func TestResponseSubject(t *testing.T) {
	cases := []struct {
		website string
		uid     string
		path    string
		uuid    string
		want    string
	}{
		{
			website: "battlefrontier",
			uid:     "user123",
			path:    "/api/login",
			uuid:    "req-1",
			want:    "battlefrontier.user123.api.login.response.req-1",
		},
		{
			website: "battlefrontier",
			uid:     "",
			path:    "/api/login",
			uuid:    "req-1",
			want:    "battlefrontier.api.login.response.req-1",
		},
		{
			website: "mysite",
			uid:     "u1",
			path:    "/api/v2/profile/update",
			uuid:    "req-2",
			want:    "mysite.u1.api.v2.profile.update.response.req-2",
		},
		// Versioned path — version stays in segments.
		{
			website: "battlefrontier",
			uid:     "user123",
			path:    "/v0/api/logout",
			uuid:    "req-3",
			want:    "battlefrontier.user123.v0.api.logout.response.req-3",
		},
	}

	for _, tc := range cases {
		got := responseSubject(tc.website, tc.uid, tc.path, tc.uuid)
		if got != tc.want {
			t.Errorf("responseSubject(%q, %q, %q, %q)\n  got  %q\n  want %q",
				tc.website, tc.uid, tc.path, tc.uuid, got, tc.want)
		}
	}
}

// TestSSEEventSubject verifies the event subject used by Conn SSE push methods.
func TestSSEEventSubject(t *testing.T) {
	cases := []struct {
		website  string
		uid      string
		connUUID string
		want     string
	}{
		{
			website:  "battlefrontier",
			uid:      "user123",
			connUUID: "conn-1",
			want:     "battlefrontier.user123.sse.conn-1.event",
		},
		{
			website:  "battlefrontier",
			uid:      "",
			connUUID: "conn-1",
			want:     "battlefrontier.sse.conn-1.event",
		},
	}

	for _, tc := range cases {
		got := sseEventSubject(tc.website, tc.uid, tc.connUUID)
		if got != tc.want {
			t.Errorf("sseEventSubject(%q, %q, %q)\n  got  %q\n  want %q",
				tc.website, tc.uid, tc.connUUID, got, tc.want)
		}
	}
}

// TestSSEDisconnectedSubject verifies the disconnected subject used by the
// legacy Handle model.
func TestSSEDisconnectedSubject(t *testing.T) {
	cases := []struct {
		website  string
		uid      string
		connUUID string
		want     string
	}{
		{
			website:  "battlefrontier",
			uid:      "user123",
			connUUID: "conn-1",
			want:     "battlefrontier.user123.sse.conn-1.disconnected",
		},
		{
			website:  "battlefrontier",
			uid:      "",
			connUUID: "conn-1",
			want:     "battlefrontier.sse.conn-1.disconnected",
		},
	}

	for _, tc := range cases {
		got := sseDisconnectedSubject(tc.website, tc.uid, tc.connUUID)
		if got != tc.want {
			t.Errorf("sseDisconnectedSubject(%q, %q, %q)\n  got  %q\n  want %q",
				tc.website, tc.uid, tc.connUUID, got, tc.want)
		}
	}
}

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
		// Versioned paths — version segment is included as-is.
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

// TestBaseSegments verifies the base segment builder.
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
			want:    []string{"battlefrontier", "user123", "sse", "dashboard"},
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
			want:    []string{"mysite", "u1", "api", "v2", "profile"},
		},
		// Versioned paths — version segment stays in position.
		{
			website: "battlefrontier",
			uid:     "user123",
			path:    "/v0/sse/index",
			want:    []string{"battlefrontier", "user123", "v0", "sse", "index"},
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

// TestAllConfiguredEndpoints_ClientMessageSubjects verifies the wildcard
// subjects the SDK would subscribe to for every SSE endpoint in
// battlefrontier.json (all versioned with /v0/).
func TestAllConfiguredEndpoints_ClientMessageSubjects(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	cases := []struct {
		path           string
		wantWithUID    string
		wantWithoutUID string
	}{
		{
			path:           "/v0/sse/index",
			wantWithUID:    "battlefrontier.*.v0.sse.index.*.message",
			wantWithoutUID: "battlefrontier.v0.sse.index.*.message",
		},
		{
			path:           "/v0/sse/dashboard",
			wantWithUID:    "battlefrontier.*.v0.sse.dashboard.*.message",
			wantWithoutUID: "battlefrontier.v0.sse.dashboard.*.message",
		},
		{
			path:           "/v0/sse/login",
			wantWithUID:    "battlefrontier.*.v0.sse.login.*.message",
			wantWithoutUID: "battlefrontier.v0.sse.login.*.message",
		},
	}

	for _, tc := range cases {
		gotWith := c.clientMessageSubjectWithUID(tc.path)
		if gotWith != tc.wantWithUID {
			t.Errorf("clientMessageSubjectWithUID(%q)\n  got  %q\n  want %q",
				tc.path, gotWith, tc.wantWithUID)
		}

		gotWithout := c.clientMessageSubjectNoUID(tc.path)
		if gotWithout != tc.wantWithoutUID {
			t.Errorf("clientMessageSubjectNoUID(%q)\n  got  %q\n  want %q",
				tc.path, gotWithout, tc.wantWithoutUID)
		}
	}
}

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

// TestVersionedVsUnversionedSubjects confirms that versioned and unversioned
// paths produce distinct subjects and that the version segment appears in the
// correct position.
func TestVersionedVsUnversionedSubjects(t *testing.T) {
	c := &Client{websiteName: "battlefrontier"}

	// /v0/sse/login and /sse/login must produce different subjects.
	v0WithUID := c.clientMessageSubjectWithUID("/v0/sse/login")
	noVerWithUID := c.clientMessageSubjectWithUID("/sse/login")

	if v0WithUID == noVerWithUID {
		t.Errorf("versioned and unversioned paths produced identical subjects: %q", v0WithUID)
	}
	if v0WithUID != "battlefrontier.*.v0.sse.login.*.message" {
		t.Errorf("v0 withUID: got %q, want %q", v0WithUID, "battlefrontier.*.v0.sse.login.*.message")
	}
	if noVerWithUID != "battlefrontier.*.sse.login.*.message" {
		t.Errorf("noVer withUID: got %q, want %q", noVerWithUID, "battlefrontier.*.sse.login.*.message")
	}

	v0NoUID := c.clientMessageSubjectNoUID("/v0/sse/login")
	noVerNoUID := c.clientMessageSubjectNoUID("/sse/login")

	if v0NoUID == noVerNoUID {
		t.Errorf("versioned and unversioned paths produced identical subjects (no uid): %q", v0NoUID)
	}
	if v0NoUID != "battlefrontier.v0.sse.login.*.message" {
		t.Errorf("v0 noUID: got %q, want %q", v0NoUID, "battlefrontier.v0.sse.login.*.message")
	}
	if noVerNoUID != "battlefrontier.sse.login.*.message" {
		t.Errorf("noVer noUID: got %q, want %q", noVerNoUID, "battlefrontier.sse.login.*.message")
	}
}
