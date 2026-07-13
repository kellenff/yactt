package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kellenff/yactt/internal/mcp"
)

func TestRequestRoundTrip(t *testing.T) {
	// Encode → JSON → Decode → Encode should be stable.
	original := mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`42`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"x"}`),
	}
	first, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded mcp.Request
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	second, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-Marshal: %v", err)
	}

	if string(first) != string(second) {
		t.Errorf("round-trip differs:\n first=%s\nsecond=%s", first, second)
	}
}

func TestEncodeErrorCodes(t *testing.T) {
	cases := []struct {
		code int
		name string
	}{
		{mcp.CodeParse, "parse"},
		{mcp.CodeInvalidRequest, "invalid_request"},
		{mcp.CodeMethodNotFound, "method_not_found"},
		{mcp.CodeInvalidParams, "invalid_params"},
		{mcp.CodeInternalError, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := mcp.EncodeError(json.RawMessage(`7`), tc.code, "msg", nil)
			if resp.Error == nil {
				t.Fatal("Error field nil")
			}
			if resp.Error.Code != tc.code {
				t.Errorf("Code = %d, want %d", resp.Error.Code, tc.code)
			}
			if resp.JSONRPC != "2.0" {
				t.Errorf("JSONRPC = %q, want 2.0", resp.JSONRPC)
			}
			if string(resp.ID) != "7" {
				t.Errorf("ID = %s, want 7", resp.ID)
			}
			// Wire encoding must carry the code.
			b, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			var back map[string]any
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatal(err)
			}
			errObj, _ := back["error"].(map[string]any)
			if errObj == nil {
				t.Fatalf("no error object in %s", b)
			}
			codeF, _ := errObj["code"].(float64)
			if int(codeF) != tc.code {
				t.Errorf("wire code = %v, want %d", codeF, tc.code)
			}
		})
	}
}

func TestEncodeErrorPropagatesData(t *testing.T) {
	resp := mcp.EncodeError(json.RawMessage(`"x"`), mcp.CodeParse, "parse error", "raw details")
	b, _ := json.Marshal(resp)
	var back map[string]any
	_ = json.Unmarshal(b, &back)
	errObj := back["error"].(map[string]any)
	if got, _ := errObj["data"].(string); got != "raw details" {
		t.Errorf("data = %q, want raw details", got)
	}
}

func TestTextContentShape(t *testing.T) {
	c := mcp.TextContent("hello world")
	if len(c) != 1 {
		t.Fatalf("len = %d, want 1", len(c))
	}
	if c[0].Type != "text" {
		t.Errorf("Type = %q, want text", c[0].Type)
	}
	if c[0].Text != "hello world" {
		t.Errorf("Text = %q", c[0].Text)
	}
	// Wire shape.
	b, _ := json.Marshal(c)
	if !strings.Contains(string(b), `"type":"text"`) {
		t.Errorf("wire missing type=text: %s", b)
	}
	if !strings.Contains(string(b), `"text":"hello world"`) {
		t.Errorf("wire missing text: %s", b)
	}
}

func TestTextContentEmpty(t *testing.T) {
	c := mcp.TextContent("")
	if len(c) != 1 {
		t.Fatalf("len = %d, want 1", len(c))
	}
	if c[0].Type != "text" {
		t.Errorf("Type = %q, want text", c[0].Type)
	}
}

func TestEncodeResponseWithResult(t *testing.T) {
	resp := mcp.Response{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Result:  map[string]any{"ok": true},
	}
	b, err := mcp.EncodeResponse(resp)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v", back["jsonrpc"])
	}
	if _, ok := back["result"]; !ok {
		t.Error("result key missing")
	}
	if _, ok := back["error"]; ok {
		t.Error("error key should be absent")
	}
}

func TestInitializeResultShape(t *testing.T) {
	ir := mcp.InitializeResult{
		ProtocolVersion: mcp.ProtocolVersion,
		ServerInfo:      mcp.ServerInfo{Name: "yactt", Version: "0.1.0"},
		Capabilities:    mcp.Capabilities{Tools: map[string]bool{"listChanged": false}},
	}
	b, err := json.Marshal(ir)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["protocolVersion"] != mcp.ProtocolVersion {
		t.Errorf("protocolVersion = %v", back["protocolVersion"])
	}
	si, _ := back["serverInfo"].(map[string]any)
	if si == nil || si["name"] != "yactt" {
		t.Errorf("serverInfo = %v", si)
	}
}
