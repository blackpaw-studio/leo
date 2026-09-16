package hosts

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

func (c *connection) fanInLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		_ = c.fanInOnce(ctx)
		if ctx.Err() != nil || !waitCtx(ctx, c.hub.deps.clock.After(backoff)) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

func (c *connection) fanInOnce(ctx context.Context) error {
	if c.hub.bus == nil {
		return nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://daemon/events?scope=local", nil)
	resp, err := (&http.Client{Transport: c.transport}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	s := bufio.NewScanner(resp.Body)
	event := ""
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") || event == "hello" {
			continue
		}
		if event == string(observe.EventHostStateChanged) {
			continue
		}
		var fields map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &fields) != nil {
			continue
		}
		if host, ok := fields["host"].(string); ok && host != "" && host != "localhost" {
			continue
		}
		fields["host"] = c.name
		delete(fields, "seq")
		delete(fields, "at")
		c.hub.bus.Publish(observe.Event{Type: observe.EventType(event), Payload: &observe.RawPayload{Fields: fields}})
	}
	return s.Err()
}
