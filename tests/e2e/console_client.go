package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type consoleFrame struct {
	Type  string `json:"type"`
	Data  string `json:"data,omitempty"`
	Code  int    `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

func main() {
	apiURL := flag.String("api", "http://127.0.0.1:9876", "sysbox API URL")
	token := flag.String("token", "", "API bearer token")
	sessionID := flag.String("session", "", "console session id")
	stdin := flag.String("stdin", "", "stdin text to send after attach")
	expect := flag.String("expect", "", "exit successfully once stdout/stderr contains this text")
	timeout := flag.Duration("timeout", 30*time.Second, "attach timeout")
	flag.Parse()

	if *sessionID == "" {
		fmt.Fprintln(os.Stderr, "session is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	wsURL := strings.TrimRight(*apiURL, "/") + "/v1/sessions/" + *sessionID + "/attach"
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)

	headers := http.Header{}
	if *token != "" {
		headers.Set("Authorization", "Bearer "+*token)
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		fmt.Fprintf(os.Stderr, "attach console session: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if *stdin != "" {
		go func() {
			frame := consoleFrame{
				Type: "stdin",
				Data: base64.StdEncoding.EncodeToString([]byte(*stdin)),
			}
			raw, err := json.Marshal(frame)
			if err != nil {
				return
			}
			_ = conn.Write(ctx, websocket.MessageText, raw)
		}()
	}

	exitCode := 0
	var output strings.Builder
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if *expect != "" && strings.Contains(output.String(), *expect) {
				os.Exit(0)
			}
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure || err == io.EOF {
				os.Exit(exitCode)
			}
			fmt.Fprintf(os.Stderr, "read console frame: %v\n", err)
			os.Exit(1)
		}
		var frame consoleFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			fmt.Fprintf(os.Stderr, "decode console frame: %v\n", err)
			os.Exit(1)
		}
		switch frame.Type {
		case "stdout", "stderr":
			raw, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil {
				raw = []byte(frame.Data)
			}
			if frame.Type == "stderr" {
				_, _ = os.Stderr.Write(raw)
			} else {
				_, _ = os.Stdout.Write(raw)
			}
			output.Write(raw)
			if *expect != "" && strings.Contains(output.String(), *expect) {
				os.Exit(0)
			}
		case "error":
			fmt.Fprintln(os.Stderr, frame.Error)
		case "exit":
			exitCode = frame.Code
			os.Exit(exitCode)
		}
	}
}
