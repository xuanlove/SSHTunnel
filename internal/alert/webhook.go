package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Notification 一次告警事件（用于 Webhook 推送）
type Notification struct {
	History History `json:"history"`
	Rule    Rule    `json:"rule"`
}

// SendNotification 发送 Webhook 通知；format 支持 generic / wecom / feishu
func SendNotification(client *http.Client, w Webhook, n Notification) error {
	if !w.Enabled || w.URL == "" {
		return nil
	}
	var payload []byte
	var err error
	msg := n.History.Message
	switch w.Format {
	case "wecom":
		payload, err = json.Marshal(map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]string{"content": msg},
		})
	case "feishu":
		payload, err = json.Marshal(map[string]any{
			"msg_type": "text",
			"content":  map[string]string{"text": msg},
		})
	default:
		payload, err = json.Marshal(n)
	}
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 返回状态码 %d", resp.StatusCode)
	}
	return nil
}
