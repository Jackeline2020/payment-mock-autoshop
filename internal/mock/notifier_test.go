package mock

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func testNotifier() *HTTPNotifier {
	n := NewHTTPNotifier(slog.New(slog.NewTextHandler(io.Discard, nil)))
	n.backoff = time.Millisecond
	return n
}

func TestNotifier_SendsMercadoPagoFormat(t *testing.T) {
	var body webhookBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	testNotifier().Notify(ts.URL, 123)
	assert.Equal(t, "payment", body.Type)
	assert.Equal(t, "123", body.Data["id"])
}

func TestNotifier_RetriesOnFailure(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	testNotifier().Notify(ts.URL, 1)
	assert.Equal(t, int32(3), calls.Load())
}

func TestNotifier_GivesUp(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	testNotifier().Notify(ts.URL, 1)
	assert.Equal(t, int32(3), calls.Load())

	testNotifier().Notify("http://127.0.0.1:1/invalido", 1) // conexão recusada
}
