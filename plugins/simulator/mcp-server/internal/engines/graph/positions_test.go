package graph

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corezoid/simulator-ai-plugin/plugins/simulator/mcp-server/internal/apiclient"
	"github.com/corezoid/simulator-ai-plugin/plugins/simulator/mcp-server/internal/engines/ecore"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestUpdatePositionsSendsBareArray guards the wire contract of
// PUT /graph_layers/actors/{layerId}: the body must be a bare JSON array (the
// server schema is `type: array`), and each item's `id` must be a string — not
// the {"items":[...]} object or a numeric id that shipped the original bug.
func TestUpdatePositionsSendsBareArray(t *testing.T) {
	const layer = "11111111-1111-1111-1111-111111111111"

	var gotBody []byte
	var gotMethod, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/graph_layers/actors/") {
			gotMethod = r.Method
			gotCT = r.Header.Get("Content-Type")
			gotBody, _ = io.ReadAll(r.Body)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	s := newGraphSyncer(srv.URL, "t", "ws-positions-test")
	// id is passed as an int on purpose, to prove it is normalised to a string.
	updates := []map[string]interface{}{
		{"id": 7, "position": map[string]int{"x": 150, "y": 200}},
		{"id": "9", "position": map[string]int{"x": 300, "y": 400}},
	}
	if err := s.updatePositions(context.Background(), layer, updates); err != nil {
		t.Fatalf("updatePositions: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotCT)
	}
	if trimmed := strings.TrimSpace(string(gotBody)); !strings.HasPrefix(trimmed, "[") {
		t.Fatalf("body is not a bare array: %s", trimmed)
	}
	var arr []map[string]interface{}
	if err := json.Unmarshal(gotBody, &arr); err != nil {
		t.Fatalf("body does not decode as an array: %v (body: %s)", err, gotBody)
	}
	if len(arr) != 2 {
		t.Fatalf("len(array) = %d, want 2", len(arr))
	}
	for i, it := range arr {
		if _, ok := it["id"].(string); !ok {
			t.Errorf("item %d id is %T, want string", i, it["id"])
		}
	}
	if arr[0]["id"] != "7" {
		t.Errorf("numeric id not normalised to string: got %v", arr[0]["id"])
	}
}

// TestCompactGraphLayoutSendsBareArray drives the compactGraphLayout handler
// end-to-end against a stub backend and asserts it applies positions as a single
// bare JSON array with string ids — the shape pong-server validates.
func TestCompactGraphLayoutSendsBareArray(t *testing.T) {
	const layer = "11111111-1111-1111-1111-111111111111"

	ecore.SetStateless(true)
	t.Cleanup(func() { ecore.SetStateless(false) })

	var putBody []byte
	var putMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/graph_layers/paginated/"):
			if r.URL.Query().Get("type") == "edges" || r.URL.Query().Get("offset") != "0" {
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			}
			// Three nodes, all currently at (0,0) so all three must move.
			_, _ = w.Write([]byte(`{"data":[
				{"id":"a1","laId":1,"title":"A","position":{"x":0,"y":0}},
				{"id":"a2","laId":2,"title":"B","position":{"x":0,"y":0}},
				{"id":"a3","laId":3,"title":"C","position":{"x":0,"y":0}}
			]}`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/graph_layers/actors/"):
			putMethod = r.Method
			putBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	ctx := apiclient.WithBaseURL(
		apiclient.WithAuthorization(context.Background(), "Simulator t"), srv.URL)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"layerId": layer}

	res, err := handleCompactGraphLayout(ctx, req)
	if err != nil {
		t.Fatalf("handleCompactGraphLayout: %v", err)
	}
	if res != nil && res.IsError {
		t.Fatalf("tool returned an error result: %+v", res)
	}
	if putMethod != http.MethodPut {
		t.Fatalf("no PUT reached the server (method=%q)", putMethod)
	}
	if trimmed := strings.TrimSpace(string(putBody)); !strings.HasPrefix(trimmed, "[") {
		t.Fatalf("compactGraphLayout body is not a bare array: %s", trimmed)
	}
	var arr []map[string]interface{}
	if err := json.Unmarshal(putBody, &arr); err != nil {
		t.Fatalf("body does not decode as an array: %v (body: %s)", err, putBody)
	}
	if len(arr) == 0 {
		t.Fatal("expected at least one placement in the PUT body")
	}
	for i, it := range arr {
		if _, ok := it["id"].(string); !ok {
			t.Errorf("item %d id is %T, want string", i, it["id"])
		}
	}
}
