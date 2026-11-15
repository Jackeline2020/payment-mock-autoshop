package mock

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ClientID     string
	ClientSecret string
	PublicURL    string        // endereço usado no link de pagamento
	PaymentDelay time.Duration // tempo até o "cliente" pagar
	TokenTTL     time.Duration
}

// Notifier avisa o sistema do cliente (webhook) que um pagamento mudou.
type Notifier interface {
	Notify(url string, paymentID int64)
}

type Server struct {
	cfg      Config
	store    *Store
	notifier Notifier
	log      *slog.Logger
}

func NewServer(cfg Config, store *Store, notifier Notifier, log *slog.Logger) *Server {
	return &Server{cfg: cfg, store: store, notifier: notifier, log: log}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("POST /checkout/preferences", s.authenticated(s.createPreference))
	mux.HandleFunc("GET /checkout/{id}", s.checkoutPage)
	mux.HandleFunc("GET /v1/payments/{id}", s.authenticated(s.getPayment))
	mux.HandleFunc("POST /v1/payments/{id}/refunds", s.authenticated(s.refund))
	return mux
}

type tokenRequest struct {
	GrantType    string `json:"grant_type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// token emite o access token no fluxo client_credentials. Aceita JSON ou
// formulário, como a API real.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "corpo inválido")
			return
		}
	} else {
		req = tokenRequest{
			GrantType:    r.FormValue("grant_type"),
			ClientID:     r.FormValue("client_id"),
			ClientSecret: r.FormValue("client_secret"),
		}
	}

	if req.GrantType != "client_credentials" {
		writeError(w, http.StatusBadRequest, "grant_type deve ser client_credentials")
		return
	}
	if req.ClientID != s.cfg.ClientID || req.ClientSecret != s.cfg.ClientSecret {
		writeError(w, http.StatusUnauthorized, "credenciais inválidas")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": s.store.IssueToken(s.cfg.TokenTTL),
		"token_type":   "Bearer",
		"expires_in":   int(s.cfg.TokenTTL.Seconds()),
	})
}

func (s *Server) createPreference(w http.ResponseWriter, r *http.Request) {
	var p Preference
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "corpo inválido")
		return
	}
	if len(p.Items) == 0 || p.ExternalReference == "" {
		writeError(w, http.StatusBadRequest, "items e external_reference são obrigatórios")
		return
	}
	for _, it := range p.Items {
		if it.Quantity <= 0 || it.UnitPrice <= 0 {
			writeError(w, http.StatusBadRequest, "quantidade e preço devem ser maiores que zero")
			return
		}
	}

	p.ID = randomHex(12)
	p.InitPoint = strings.TrimRight(s.cfg.PublicURL, "/") + "/checkout/" + p.ID
	p.SandboxInitPoint = p.InitPoint
	p.DateCreated = time.Now().UTC().Format(time.RFC3339)
	s.store.SavePreference(p)

	s.log.Info("link de pagamento criado", "preference_id", p.ID, "external_reference", p.ExternalReference, "amount", p.Total())

	// Simula o cliente pagando pelo link depois de alguns segundos
	time.AfterFunc(s.cfg.PaymentDelay, func() {
		pay := s.store.Pay(p)
		s.log.Info("pagamento simulado", "payment_id", pay.ID, "status", pay.Status, "external_reference", pay.ExternalReference)
		if p.NotificationURL != "" {
			s.notifier.Notify(p.NotificationURL, pay.ID)
		}
	})

	writeJSON(w, http.StatusCreated, p)
}

// checkoutPage representa a página de pagamento (init_point).
func (s *Server) checkoutPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.Preference(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "link de pagamento não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message":            "Página de pagamento simulada: o pagamento é feito automaticamente.",
		"preference_id":      p.ID,
		"external_reference": p.ExternalReference,
		"amount":             p.Total(),
	})
}

func (s *Server) getPayment(w http.ResponseWriter, r *http.Request) {
	id, ok := paymentID(w, r)
	if !ok {
		return
	}
	pay, err := s.store.Payment(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "pagamento não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, pay)
}

func (s *Server) refund(w http.ResponseWriter, r *http.Request) {
	id, ok := paymentID(w, r)
	if !ok {
		return
	}
	ref, err := s.store.Refund(id)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "pagamento não encontrado")
	case errors.Is(err, ErrNotRefundable):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.log.Info("pagamento estornado", "payment_id", id)
		writeJSON(w, http.StatusCreated, ref)
	}
}

// authenticated exige "Authorization: Bearer <token>" válido.
func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || !s.store.ValidToken(token) {
			writeError(w, http.StatusUnauthorized, "token inválido ou expirado")
			return
		}
		next(w, r)
	}
}

func paymentID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id de pagamento inválido")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg, "status": status})
}
