// Package mock simula a API do Mercado Pago usada pelo Billing Service:
// autenticação por client_id/client_secret, Checkout Pro (link de
// pagamento), consulta de pagamento, estorno e notificação por webhook.
package mock

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"sync"
	"time"
)

const (
	StatusApproved = "approved"
	StatusRejected = "rejected"
	StatusRefunded = "refunded"
)

var (
	ErrNotFound      = errors.New("não encontrado")
	ErrNotRefundable = errors.New("somente pagamentos aprovados podem ser estornados")
)

type Item struct {
	Title      string  `json:"title"`
	Quantity   int     `json:"quantity"`
	UnitPrice  float64 `json:"unit_price"`
	CurrencyID string  `json:"currency_id,omitempty"`
}

type Preference struct {
	ID                string `json:"id"`
	Items             []Item `json:"items"`
	ExternalReference string `json:"external_reference"`
	NotificationURL   string `json:"notification_url"`
	InitPoint         string `json:"init_point"`
	SandboxInitPoint  string `json:"sandbox_init_point"`
	DateCreated       string `json:"date_created"`
}

func (p Preference) Total() float64 {
	total := 0.0
	for _, it := range p.Items {
		total += it.UnitPrice * float64(it.Quantity)
	}
	return math.Round(total*100) / 100
}

type Payment struct {
	ID                int64   `json:"id"`
	Status            string  `json:"status"`
	StatusDetail      string  `json:"status_detail"`
	TransactionAmount float64 `json:"transaction_amount"`
	ExternalReference string  `json:"external_reference"`
	PreferenceID      string  `json:"preference_id"`
	DateCreated       string  `json:"date_created"`
	DateLastUpdated   string  `json:"date_last_updated"`
}

type Refund struct {
	ID        int64   `json:"id"`
	PaymentID int64   `json:"payment_id"`
	Amount    float64 `json:"amount"`
	Status    string  `json:"status"`
}

// Store guarda tokens, preferências e pagamentos em memória.
type Store struct {
	mu          sync.Mutex
	tokens      map[string]time.Time
	preferences map[string]Preference
	payments    map[int64]*Payment
	refunds     map[int64]Refund
	nextID      int64
}

func NewStore() *Store {
	return &Store{
		tokens:      map[string]time.Time{},
		preferences: map[string]Preference{},
		payments:    map[int64]*Payment{},
		refunds:     map[int64]Refund{},
		nextID:      1000000000,
	}
}

func (s *Store) IssueToken(ttl time.Duration) string {
	token := "APP_USR-" + randomHex(16)
	s.mu.Lock()
	s.tokens[token] = time.Now().Add(ttl)
	s.mu.Unlock()
	return token
}

func (s *Store) ValidToken(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tokens[token]
	return ok && time.Now().Before(exp)
}

func (s *Store) SavePreference(p Preference) {
	s.mu.Lock()
	s.preferences[p.ID] = p
	s.mu.Unlock()
}

func (s *Store) Preference(id string) (Preference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.preferences[id]
	if !ok {
		return Preference{}, ErrNotFound
	}
	return p, nil
}

// Pay simula o pagamento do cliente: aprova, ou recusa quando os centavos
// do valor são 13 (regra usada para demonstrar a compensação na Saga).
func (s *Store) Pay(p Preference) Payment {
	amount := p.Total()
	status, detail := StatusApproved, "accredited"
	if int64(math.Round(amount*100))%100 == 13 {
		status, detail = StatusRejected, "cc_rejected_other_reason"
	}

	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	pay := &Payment{
		ID:                s.nextID,
		Status:            status,
		StatusDetail:      detail,
		TransactionAmount: amount,
		ExternalReference: p.ExternalReference,
		PreferenceID:      p.ID,
		DateCreated:       now,
		DateLastUpdated:   now,
	}
	s.payments[pay.ID] = pay
	return *pay
}

func (s *Store) Payment(id int64) (Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[id]
	if !ok {
		return Payment{}, ErrNotFound
	}
	return *p, nil
}

// Refund estorna um pagamento aprovado. Pedir o estorno de novo devolve o
// mesmo estorno, sem erro.
func (s *Store) Refund(paymentID int64) (Refund, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[paymentID]
	if !ok {
		return Refund{}, ErrNotFound
	}
	if r, done := s.refunds[paymentID]; done {
		return r, nil
	}
	if p.Status != StatusApproved {
		return Refund{}, ErrNotRefundable
	}

	s.nextID++
	r := Refund{ID: s.nextID, PaymentID: paymentID, Amount: p.TransactionAmount, Status: StatusApproved}
	s.refunds[paymentID] = r
	p.Status = StatusRefunded
	p.StatusDetail = "refunded"
	p.DateLastUpdated = time.Now().UTC().Format(time.RFC3339)
	return r, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
