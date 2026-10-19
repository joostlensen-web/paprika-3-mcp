package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postRPC sends one JSON-RPC request the way a remote MCP client does and returns the decoded response
func postRPC(t *testing.T, url string, id int, method string, params interface{}) map[string]interface{} {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	require.Nil(t, out["error"], string(raw))
	return out
}

func TestHTTPHandler(t *testing.T) {
	ts := httptest.NewServer(newTestServer(newFakeAccount()).HTTPHandler())
	defer ts.Close()

	// Stateless: each request stands alone, without a session id from initialize
	init := postRPC(t, ts.URL, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "test", "version": "0"},
	})
	assert.Equal(t, "paprika-3-mcp", init["result"].(map[string]interface{})["serverInfo"].(map[string]interface{})["name"])

	list := postRPC(t, ts.URL, 2, "tools/list", map[string]interface{}{})
	var names []string
	for _, tool := range list["result"].(map[string]interface{})["tools"].([]interface{}) {
		names = append(names, tool.(map[string]interface{})["name"].(string))
	}
	assert.ElementsMatch(t, []string{"create_paprika_recipe", "update_paprika_recipe", "search_recipes", "get_recipe", "list_categories", "list_meals", "add_meal"}, names)

	call := postRPC(t, ts.URL, 3, "tools/call", map[string]interface{}{
		"name":      "search_recipes",
		"arguments": map[string]interface{}{"ingredient": "prei"},
	})
	text := call["result"].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	assert.Contains(t, text, "Pasta met prei")
	assert.Contains(t, text, "Preisoep")
}
