package apps

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
)

const AppBrokerVersion = "hank.app.broker.v1"
const maxBrokerBytes = 1 << 20

type BrokerRequest struct {
	Version    string            `json:"version"`
	ID         string            `json:"id"`
	Permission string            `json:"permission"`
	Operation  string            `json:"operation"`
	Path       string            `json:"path,omitempty"`
	URL        string            `json:"url,omitempty"`
	Method     string            `json:"method,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Data       []byte            `json:"data,omitempty"`
	Offset     int64             `json:"offset,omitempty"`
}
type BrokerHandler func(context.Context, BrokerRequest) (any, error)

func serveAppBroker(ctx context.Context, conn io.ReadWriter, handler BrokerHandler) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), maxBrokerBytes)
	for count := 0; count < 1024 && scanner.Scan(); count++ {
		var request BrokerRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return
		}
		var output any
		err := error(nil)
		if request.Version != AppBrokerVersion || request.ID == "" || len(request.ID) > 128 || handler == nil {
			err = ErrPermissionRefused
		} else {
			output, err = handler(ctx, request)
		}
		result := map[string]any{"id": request.ID, "ok": err == nil}
		if err != nil {
			code := "app_broker_failed"
			if errors.Is(err, ErrPermissionRefused) {
				code = "app_permission_refused"
			}
			result["error"] = map[string]string{"code": code, "message": "App resource request was refused or failed"}
		} else {
			result["output"] = output
		}
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil || len(encoded) >= maxBrokerBytes {
			encoded, _ = json.Marshal(map[string]any{"id": request.ID, "ok": false, "error": map[string]string{"code": "app_broker_limit", "message": "App resource response exceeds the broker limit"}})
		}
		if _, err := conn.Write(append(encoded, '\n')); err != nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}
