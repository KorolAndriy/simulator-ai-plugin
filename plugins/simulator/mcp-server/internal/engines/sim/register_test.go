package sim

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func callTool(t *testing.T, h func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any) (map[string]any, bool) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(mcp.TextContent).Text
	var out map[string]any
	if json.Unmarshal([]byte(text), &out) != nil {
		return map[string]any{"text": text}, res.IsError
	}
	return out, res.IsError
}

func TestToolsRunOfflineFromFiles(t *testing.T) {
	dir, _ := filepath.Abs("testdata/conformance/sample01")
	t.Setenv("SIMULATOR_WORK_DIR", dir)
	model, _ := os.ReadFile(filepath.Join(dir, "model.yaml"))

	out, isErr := callTool(t, handleCheck, map[string]any{"model": string(model), "graphPath": "graph.yaml", "scenariosPath": "scenarios.yaml"})
	if isErr || out["ok"] != true {
		t.Fatalf("check: %v", out)
	}

	out, isErr = callTool(t, handleRun, map[string]any{"modelPath": "model.yaml", "scenariosPath": "scenarios.yaml",
		"graphPath": "graph.yaml", "scenario": "base,price80_ai"})
	table, _ := out["table"].(string)
	if isErr || !strings.Contains(table, "| base | completed | 6 | 1 | 0 | 80 |") || !strings.Contains(table, "| 165 |") {
		t.Fatalf("run table:\n%s\n%v", table, out)
	}

	out, _ = callTool(t, handleRun, map[string]any{"modelPath": "model.yaml", "scenariosPath": "scenarios.yaml",
		"graphPath": "graph.yaml", "scenario": "b_random_80", "runs": float64(50), "goals": "{margin_120: 'margin == 120'}"})
	table, _ = out["table"].(string)
	if !strings.Contains(table, "50/50") || !strings.Contains(table, "goal: margin_120") || !strings.Contains(table, "100% (50/50)") {
		t.Fatalf("runs table:\n%s", table)
	}
}

func TestRunRefusesBrokenModelAndBadPaths(t *testing.T) {
	dir, _ := filepath.Abs("testdata/conformance/sample01")
	t.Setenv("SIMULATOR_WORK_DIR", dir)
	model, _ := os.ReadFile(filepath.Join(dir, "model.yaml"))
	broken := strings.Replace(string(model), "      - release: {target: self}\n", "      - release: {target: self}\n      - explode: {}\n", 1)
	out, _ := callTool(t, handleRun, map[string]any{"model": broken, "graphPath": "graph.yaml"})
	if out["ok"] != false || !strings.Contains(strings.Join(toStrings(out["errors"]), " "), "explode") {
		t.Fatalf("broken model ran: %v", out)
	}
	for _, p := range []string{"../../../../../../etc/passwd", "/etc/passwd"} {
		_, isErr := callTool(t, handleRun, map[string]any{"model": string(model), "graphPath": p})
		if !isErr {
			t.Errorf("path %q was accepted", p)
		}
	}
	if _, isErr := callTool(t, handleRun, map[string]any{"model": string(model), "layerId": "not-a-uuid"}); !isErr {
		t.Error("non-UUID layerId accepted")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
