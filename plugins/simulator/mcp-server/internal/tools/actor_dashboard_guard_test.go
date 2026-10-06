package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corezoid/simulator-ai-plugin/plugins/simulator/mcp-server/internal/apiclient"
)

// guardClient builds a client pointed at a mock backend. Each mock gets a unique
// httptest URL, so the package-global formTitleCache (keyed by base URL +
// workspace) never leaks between subtests.
func guardClient(t *testing.T, h http.HandlerFunc) *apiclient.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return apiclient.New(srv.URL, "WS", func() (string, error) { return "t", nil }, false)
}

// formsHandler serves the forms-template list used to resolve "Dashboards". When
// withDashboards is false the workspace has no Dashboards form (the guards must
// then fail open). actorBody, if non-empty, is returned for /actors/ GETs;
// actorStatus>0 forces that status instead (to exercise the fail-open path).
func formsHandler(withDashboards bool, actorBody string, actorStatus int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/forms/templates/"):
			if withDashboards {
				_, _ = w.Write([]byte(`{"data":[{"id":243,"title":"Dashboards"},{"id":100,"title":"Clients"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"data":[{"id":100,"title":"Clients"}]}`))
			}
		case strings.HasPrefix(r.URL.Path, "/actors/"):
			if actorStatus != 0 {
				w.WriteHeader(actorStatus)
			}
			_, _ = w.Write([]byte(actorBody))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}
}

func TestGuardActorCreate_BlocksDashboardsByID(t *testing.T) {
	c := guardClient(t, formsHandler(true, "", 0))
	args := map[string]any{"formId": float64(243), "data": map[string]any{"source": "{}"}}
	err := guardActorCreate(context.Background(), args, c)
	if err == nil {
		t.Fatal("expected createActor on the Dashboards form to be blocked")
	}
	if !strings.Contains(err.Error(), "createChart") {
		t.Errorf("block message should steer to createChart, got: %v", err)
	}
}

func TestGuardActorCreate_BlocksDashboardsByName(t *testing.T) {
	c := guardClient(t, formsHandler(true, "", 0))
	args := map[string]any{"formName": "Dashboards", "data": map[string]any{}}
	if err := guardActorCreate(context.Background(), args, c); err == nil {
		t.Fatal("expected createActor with formName=Dashboards to be blocked")
	}
}

func TestGuardActorCreate_AllowsOrdinaryForm(t *testing.T) {
	c := guardClient(t, formsHandler(true, "", 0))
	args := map[string]any{"formId": float64(100), "data": map[string]any{}}
	if err := guardActorCreate(context.Background(), args, c); err != nil {
		t.Fatalf("ordinary form create must pass, got: %v", err)
	}
}

func TestGuardActorCreate_FailsOpenWithoutDashboardsForm(t *testing.T) {
	c := guardClient(t, formsHandler(false, "", 0))
	args := map[string]any{"formId": float64(243), "data": map[string]any{}}
	if err := guardActorCreate(context.Background(), args, c); err != nil {
		t.Fatalf("no resolvable Dashboards form must fail open, got: %v", err)
	}
}

func TestGuardActorUpdate_AllowsOrdinaryForm(t *testing.T) {
	// formId != Dashboards: must return before any actor fetch.
	c := guardClient(t, formsHandler(true, "", 0))
	args := map[string]any{"formId": float64(100), "actorId": "a1", "data": map[string]any{}}
	if err := guardActorUpdate(context.Background(), args, c); err != nil {
		t.Fatalf("ordinary form update must pass, got: %v", err)
	}
}

func TestGuardActorUpdate_AllowsEditingExistingChart(t *testing.T) {
	// A createChart dashboard: Dashboards form + non-empty data.source → editable.
	actor := `{"data":{"formId":243,"data":{"source":"{\"chartType\":\"line\"}"}}}`
	c := guardClient(t, formsHandler(true, actor, 0))
	args := map[string]any{"formId": float64(243), "actorId": "chart-1", "data": map[string]any{"source": "{\"chartType\":\"bar\"}"}}
	if err := guardActorUpdate(context.Background(), args, c); err != nil {
		t.Fatalf("editing an existing chart must pass, got: %v", err)
	}
}

func TestGuardActorUpdate_BlocksIntroducingDashboard(t *testing.T) {
	// Target is on the Dashboards form but has no existing data.source → the
	// update would manufacture a chart by hand.
	actor := `{"data":{"formId":243,"data":{}}}`
	c := guardClient(t, formsHandler(true, actor, 0))
	args := map[string]any{"formId": float64(243), "actorId": "x1", "data": map[string]any{"source": "{}"}}
	err := guardActorUpdate(context.Background(), args, c)
	if err == nil {
		t.Fatal("expected update that introduces a dashboard to be blocked")
	}
	if !strings.Contains(err.Error(), "createChart") {
		t.Errorf("block message should steer to createChart, got: %v", err)
	}
}

func TestGuardActorUpdate_BlocksEmptySource(t *testing.T) {
	// data.source present but empty string — not a rendered chart → blocked.
	actor := `{"data":{"formId":243,"data":{"source":""}}}`
	c := guardClient(t, formsHandler(true, actor, 0))
	args := map[string]any{"formId": float64(243), "actorId": "x1", "data": map[string]any{}}
	if err := guardActorUpdate(context.Background(), args, c); err == nil {
		t.Fatal("expected update of a source-less Dashboards actor to be blocked")
	}
}

func TestGuardActorUpdate_FailsOpenWhenActorUnreadable(t *testing.T) {
	// Can't read the current actor → don't block a potentially legitimate edit.
	c := guardClient(t, formsHandler(true, `{"error":"boom"}`, http.StatusInternalServerError))
	args := map[string]any{"formId": float64(243), "actorId": "x1", "data": map[string]any{}}
	if err := guardActorUpdate(context.Background(), args, c); err != nil {
		t.Fatalf("unreadable actor must fail open, got: %v", err)
	}
}
