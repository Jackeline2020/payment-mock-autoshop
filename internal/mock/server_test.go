package mock_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"payment-mock-autoshop/internal/mock"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type notification struct {
	url       string
	paymentID int64
}

type fakeNotifier struct{ ch chan notification }

func (f *fakeNotifier) Notify(url string, id int64) { f.ch <- notification{url, id} }

func setup(t *testing.T) (*httptest.Server, *fakeNotifier) {
	t.Helper()
	notifier := &fakeNotifier{ch: make(chan notification, 10)}
	cfg := mock.Config{ClientID: "cid", ClientSecret: "secret", PublicURL: "http://mock", TokenTTL: time.Hour}
	srv := mock.NewServer(cfg, mock.NewStore(), notifier, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, notifier
}

func call(t *testing.T, method, url, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func token(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	resp, out := call(t, http.MethodPost, ts.URL+"/oauth/token", "", map[string]string{
		"grant_type": "client_credentials", "client_id": "cid", "client_secret": "secret",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return out["access_token"].(string)
}

func preference(price float64) map[string]any {
	return map[string]any{
		"items":              []map[string]any{{"title": "OS 1", "quantity": 1, "unit_price": price}},
		"external_reference": "os-1",
		"notification_url":   "http://billing/webhook",
	}
}

// pay cria o link e espera o pagamento simulado, devolvendo o pagamento.
func pay(t *testing.T, ts *httptest.Server, n *fakeNotifier, tok string, price float64) map[string]any {
	t.Helper()
	resp, pref := call(t, http.MethodPost, ts.URL+"/checkout/preferences", tok, preference(price))
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, "http://mock/checkout/"+pref["id"].(string), pref["init_point"])

	select {
	case got := <-n.ch:
		assert.Equal(t, "http://billing/webhook", got.url)
		resp, payment := call(t, http.MethodGet, ts.URL+"/v1/payments/"+strconv.FormatInt(got.paymentID, 10), tok, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return payment
	case <-time.After(2 * time.Second):
		t.Fatal("webhook não foi chamado")
		return nil
	}
}

func TestHealth(t *testing.T) {
	ts, _ := setup(t)
	resp, out := call(t, http.MethodGet, ts.URL+"/health", "", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok", out["status"])
}

func TestToken_JSONAndForm(t *testing.T) {
	ts, _ := setup(t)
	assert.NotEmpty(t, token(t, ts))

	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {"cid"}, "client_secret": {"secret"}}
	resp, err := http.Post(ts.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestToken_Errors(t *testing.T) {
	ts, _ := setup(t)
	resp, _ := call(t, http.MethodPost, ts.URL+"/oauth/token", "", map[string]string{"grant_type": "client_credentials", "client_id": "cid", "client_secret": "errado"})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	resp, _ = call(t, http.MethodPost, ts.URL+"/oauth/token", "", map[string]string{"grant_type": "password"})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/oauth/token", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	r.Body.Close()
	assert.Equal(t, http.StatusBadRequest, r.StatusCode)
}

func TestProtectedRoutesRequireToken(t *testing.T) {
	ts, _ := setup(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/checkout/preferences"},
		{http.MethodGet, "/v1/payments/1"},
		{http.MethodPost, "/v1/payments/1/refunds"},
	} {
		resp, _ := call(t, tc.method, ts.URL+tc.path, "token-invalido", nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, tc.path)
	}
}

func TestPayment_Approved(t *testing.T) {
	ts, n := setup(t)
	tok := token(t, ts)

	payment := pay(t, ts, n, tok, 350.00)
	assert.Equal(t, "approved", payment["status"])
	assert.Equal(t, "os-1", payment["external_reference"])
	assert.Equal(t, 350.0, payment["transaction_amount"])
}

func TestPayment_RejectedWhenCentsAre13(t *testing.T) {
	ts, n := setup(t)
	tok := token(t, ts)

	payment := pay(t, ts, n, tok, 350.13)
	assert.Equal(t, "rejected", payment["status"])
}

func TestPreference_Invalid(t *testing.T) {
	ts, _ := setup(t)
	tok := token(t, ts)

	resp, _ := call(t, http.MethodPost, ts.URL+"/checkout/preferences", tok, map[string]any{"items": []any{}})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	bad := preference(0)
	resp, _ = call(t, http.MethodPost, ts.URL+"/checkout/preferences", tok, bad)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/checkout/preferences", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer "+tok)
	r, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	r.Body.Close()
	assert.Equal(t, http.StatusBadRequest, r.StatusCode)
}

func TestCheckoutPage(t *testing.T) {
	ts, n := setup(t)
	tok := token(t, ts)
	_, pref := call(t, http.MethodPost, ts.URL+"/checkout/preferences", tok, preference(100))
	<-n.ch

	resp, out := call(t, http.MethodGet, ts.URL+"/checkout/"+pref["id"].(string), "", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 100.0, out["amount"])

	resp, _ = call(t, http.MethodGet, ts.URL+"/checkout/nao-existe", "", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestRefund(t *testing.T) {
	ts, n := setup(t)
	tok := token(t, ts)
	payment := pay(t, ts, n, tok, 200)
	id := strconv.FormatInt(int64(payment["id"].(float64)), 10)

	resp, refund := call(t, http.MethodPost, ts.URL+"/v1/payments/"+id+"/refunds", tok, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, 200.0, refund["amount"])

	// estornar de novo devolve o mesmo estorno
	resp, again := call(t, http.MethodPost, ts.URL+"/v1/payments/"+id+"/refunds", tok, nil)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, refund["id"], again["id"])

	_, updated := call(t, http.MethodGet, ts.URL+"/v1/payments/"+id, tok, nil)
	assert.Equal(t, "refunded", updated["status"])
}

func TestRefund_Errors(t *testing.T) {
	ts, n := setup(t)
	tok := token(t, ts)

	rejected := pay(t, ts, n, tok, 10.13)
	id := strconv.FormatInt(int64(rejected["id"].(float64)), 10)
	resp, _ := call(t, http.MethodPost, ts.URL+"/v1/payments/"+id+"/refunds", tok, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, _ = call(t, http.MethodPost, ts.URL+"/v1/payments/999/refunds", tok, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, _ = call(t, http.MethodGet, ts.URL+"/v1/payments/999", tok, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, _ = call(t, http.MethodGet, ts.URL+"/v1/payments/abc", tok, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestExpiredToken(t *testing.T) {
	store := mock.NewStore()
	tok := store.IssueToken(-time.Second)
	assert.False(t, store.ValidToken(tok))
}
