package agentexec

import (
	"context"
	"encoding/json"

	"github.com/coder/websocket"
)

type consoleFrame struct {
	Type  string `json:"type"`
	Data  string `json:"data,omitempty"`
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

func writeConsoleFrame(ctx context.Context, ws *websocket.Conn, frame consoleFrame) error {
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, raw)
}
