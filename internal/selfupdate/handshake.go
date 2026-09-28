package selfupdate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// MCPHandshakeResult is what a started server answered.
type MCPHandshakeResult struct {
	ServerVersion string   `json:"serverVersion,omitempty"`
	Tools         []string `json:"tools"`
}

// MCPHandshake starts `node server` (PCBPILOT_BIN=bin), sends initialize +
// tools/list over stdio, and returns the server version and tool names. The
// process is always killed afterwards.
func MCPHandshake(ctx context.Context, node, server, bin string) (MCPHandshakeResult, error) {
	var res MCPHandshakeResult
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, server)
	cmd.Env = append(os.Environ(), "PCBPILOT_BIN="+bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return res, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return res, err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	if err := cmd.Start(); err != nil {
		return res, err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lines := make(chan map[string]any, 8)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				lines <- m
			}
		}
		close(lines)
	}()
	send := func(v any) error {
		raw, _ := json.Marshal(v)
		_, err := stdin.Write(append(raw, '\n'))
		return err
	}
	recv := func(id float64) (map[string]any, error) {
		for {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("no MCP response (id %v): %v %s", id, ctx.Err(), strings.TrimSpace(stderr.String()))
			case m, ok := <-lines:
				if !ok {
					return nil, fmt.Errorf("MCP server exited: %s", strings.TrimSpace(stderr.String()))
				}
				if got, _ := m["id"].(float64); got == id {
					if e, bad := m["error"]; bad {
						return nil, fmt.Errorf("MCP error: %v", e)
					}
					return m, nil
				}
			}
		}
	}
	if err := send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "pcbpilot-update-verify", "version": "1"}}}); err != nil {
		return res, err
	}
	init, err := recv(1)
	if err != nil {
		return res, err
	}
	if r, ok := init["result"].(map[string]any); ok {
		if si, ok := r["serverInfo"].(map[string]any); ok {
			res.ServerVersion, _ = si["version"].(string)
		}
	}
	_ = send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if err := send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}); err != nil {
		return res, err
	}
	list, err := recv(2)
	if err != nil {
		return res, err
	}
	r, _ := list["result"].(map[string]any)
	tools, _ := r["tools"].([]any)
	for _, t := range tools {
		if m, ok := t.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				res.Tools = append(res.Tools, n)
			}
		}
	}
	hasOurs := false
	for _, n := range res.Tools {
		if strings.HasPrefix(n, "pcbpilot_") {
			hasOurs = true
		}
	}
	if !hasOurs {
		return res, fmt.Errorf("MCP handshake returned no pcbpilot tools: %v", res.Tools)
	}
	return res, nil
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	_, _ = l.w.Write(q)
	return len(p), nil
}
