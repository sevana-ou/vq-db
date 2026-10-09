package alarm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// WebhookNotifier POSTs each event of a rule with a webhook as JSON, from its
// own goroutine. A failed delivery is retried once after a few seconds and
// then logged; webhooks receive raised, repeated and cleared events alike (the
// "state" field tells them apart).
type WebhookNotifier struct {
	Client    *http.Client
	UserAgent string
	queue     chan Event
	done      chan struct{}
	retryWait time.Duration
}

// NewWebhookNotifier starts the delivery worker.
func NewWebhookNotifier(userAgent string) *WebhookNotifier {
	w := &WebhookNotifier{Client: &http.Client{Timeout: 10 * time.Second}, UserAgent: userAgent,
		queue: make(chan Event, 64), done: make(chan struct{}), retryWait: 5 * time.Second}
	go w.loop()
	return w
}

// Notify queues the event; a full queue drops it with a log line.
func (w *WebhookNotifier) Notify(ev Event) {
	if ev.Rule.Webhook == "" {
		return
	}
	select {
	case w.queue <- ev:
	default:
		slog.Error("alarm webhook queue full; event dropped", "alarm", ev.Rule.Name, "kind", ev.Kind)
	}
}

// Close stops the worker after the queued deliveries.
func (w *WebhookNotifier) Close() {
	close(w.queue)
	<-w.done
}

func (w *WebhookNotifier) loop() {
	defer close(w.done)
	for ev := range w.queue {
		body, _ := json.Marshal(Payload(ev))
		err := w.post(ev.Rule.Webhook, body)
		if err != nil {
			time.Sleep(w.retryWait)
			err = w.post(ev.Rule.Webhook, body)
		}
		if err != nil {
			slog.Error("alarm webhook failed", "alarm", ev.Rule.Name, "kind", ev.Kind, "url", ev.Rule.Webhook, "err", err)
		}
	}
}

func (w *WebhookNotifier) post(url string, body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), w.Client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.UserAgent != "" {
		req.Header.Set("User-Agent", w.UserAgent)
	}
	resp, err := w.Client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// Payload is the JSON body of a webhook call.
func Payload(ev Event) map[string]any {
	s := ev.Snapshot
	kpis := map[string]any{
		"streams": s.Streams, "sevana_streams": s.SevanaStreams,
		"r_factor": s.RFactor, "sevana_mos": s.SevanaMOS, "network_mos": s.NetworkMOS,
		"packet_loss": s.PacketLoss, "jitter": s.Jitter, "duration": s.Duration,
	}
	if s.FreeDisk >= 0 {
		kpis["free_disk_space"] = s.FreeDisk
	}
	return map[string]any{
		"alarm": ev.Rule.Name, "state": string(ev.Kind), "counter": ev.Rule.Counter,
		"op": ev.Rule.Op.String(), "limit": ev.Rule.Limit, "unit": ev.Rule.Unit,
		"value": ev.Value, "samples": ev.Samples, "window_s": int(ev.Rule.Window.Seconds()),
		"filter": ev.Rule.Filter, "code": ev.Rule.Code,
		"time": ev.At.UTC().Format(time.RFC3339), "timestamp_ms": ev.At.UnixMilli(),
		"instance": map[string]any{"id": ev.Instance.ID, "name": ev.Instance.Name},
		"kpis":     kpis,
	}
}
