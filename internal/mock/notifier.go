package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// HTTPNotifier envia o webhook no formato do Mercado Pago, com algumas
// tentativas em caso de falha.
type HTTPNotifier struct {
	client   *http.Client
	attempts int
	backoff  time.Duration
	log      *slog.Logger
}

func NewHTTPNotifier(log *slog.Logger) *HTTPNotifier {
	return &HTTPNotifier{client: &http.Client{Timeout: 5 * time.Second}, attempts: 3, backoff: time.Second, log: log}
}

type webhookBody struct {
	Action      string            `json:"action"`
	APIVersion  string            `json:"api_version"`
	Type        string            `json:"type"`
	DateCreated string            `json:"date_created"`
	LiveMode    bool              `json:"live_mode"`
	Data        map[string]string `json:"data"`
}

func (n *HTTPNotifier) Notify(url string, paymentID int64) {
	body, _ := json.Marshal(webhookBody{
		Action:      "payment.created",
		APIVersion:  "v1",
		Type:        "payment",
		DateCreated: time.Now().UTC().Format(time.RFC3339),
		Data:        map[string]string{"id": strconv.FormatInt(paymentID, 10)},
	})

	for attempt := 1; attempt <= n.attempts; attempt++ {
		err := n.send(url, body)
		if err == nil {
			n.log.Info("webhook entregue", "url", url, "payment_id", paymentID)
			return
		}
		n.log.Warn("falha ao entregar webhook", "url", url, "payment_id", paymentID, "attempt", attempt, "error", err.Error())
		if attempt < n.attempts {
			time.Sleep(n.backoff * time.Duration(attempt))
		}
	}
}

func (n *HTTPNotifier) send(url string, body []byte) error {
	resp, err := n.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
