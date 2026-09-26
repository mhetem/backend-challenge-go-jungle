package wager

import (
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

type Result struct {
	Balance       money.Money
	WalletVersion int64
}

type Snapshot struct {
	ID                             uuid.UUID
	WalletID                       uuid.UUID
	PlayerID                       uuid.UUID
	Kind                           Kind
	Money                          money.Money
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string
	ReferenceTransactionID         uuid.UUID
	CorrelationID                  string
	Status                         Status
	FailureCode                    FailureCode
	Result                         *Result
	Attempts                       int
	NextAttemptAt                  time.Time
	ReferenceDeadlineAt            time.Time
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
	CompletedAt                    time.Time
}

type Payload struct {
	ProviderID                     string
	ExternalTransactionID          string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
}

type ExternalParams struct {
	Payload
	ID             uuid.UUID
	IdempotencyKey string
	PayloadHash    string
	CorrelationID  string
}

type Transaction struct {
	s Snapshot
}

const referenceField = "referenceExternalTransactionId"

func (p Payload) Validate() error {
	var v domain.Validation
	p.check(&v)
	return v.Err()
}

func (p Payload) check(v *domain.Validation) {
	v.Check(p.ProviderID != "", "providerId", domain.ErrRequired)
	v.Check(p.ExternalTransactionID != "", "externalTransactionId", domain.ErrRequired)
	v.Check(p.PlayerID != uuid.Nil, "playerId", domain.ErrRequired)
	v.Check(p.WalletID != uuid.Nil, "walletId", domain.ErrRequired)
	v.Check(p.RoundID != "", "roundId", domain.ErrRequired)
	v.Check(p.GameID != "", "gameId", domain.ErrRequired)
	v.Check(p.Kind.Valid(), "kind", domain.ErrInvalidValue)
	v.Check(p.Kind != Opening, "kind", domain.ErrKindNotAllowed)
	v.CheckAmount("money", p.Money, p.Kind.amountRule())
	checkReference(v, p.Kind, p.ReferenceExternalTransactionID, p.ExternalTransactionID)
}

func NewExternal(p ExternalParams, now time.Time) (*Transaction, error) {
	var v domain.Validation
	v.Check(p.ID != uuid.Nil, "id", domain.ErrRequired)
	p.check(&v)
	v.Check(p.IdempotencyKey != "", "idempotencyKey", domain.ErrRequired)
	v.Check(p.PayloadHash != "", "payloadHash", domain.ErrRequired)
	v.Check(p.CorrelationID != "", "correlationId", domain.ErrRequired)
	v.Check(!now.IsZero(), "createdAt", domain.ErrRequired)
	if err := v.Err(); err != nil {
		return nil, err
	}
	return &Transaction{s: Snapshot{
		ID:                             p.ID,
		WalletID:                       p.WalletID,
		PlayerID:                       p.PlayerID,
		Kind:                           p.Kind,
		Money:                          p.Money,
		ProviderID:                     p.ProviderID,
		ExternalTransactionID:          p.ExternalTransactionID,
		IdempotencyKey:                 p.IdempotencyKey,
		PayloadHash:                    p.PayloadHash,
		RoundID:                        p.RoundID,
		GameID:                         p.GameID,
		ReferenceExternalTransactionID: p.ReferenceExternalTransactionID,
		CorrelationID:                  p.CorrelationID,
		Status:                         Pending,
		CreatedAt:                      now,
		UpdatedAt:                      now,
	}}, nil
}

func NewOpening(walletID, playerID uuid.UUID, amount money.Money, correlationID string, now time.Time) (*Transaction, error) {
	var v domain.Validation
	v.Check(walletID != uuid.Nil, "walletId", domain.ErrRequired)
	v.Check(playerID != uuid.Nil, "playerId", domain.ErrRequired)
	v.CheckAmount("money", amount, money.Money.IsPositive)
	v.Check(correlationID != "", "correlationId", domain.ErrRequired)
	v.Check(!now.IsZero(), "createdAt", domain.ErrRequired)
	if err := v.Err(); err != nil {
		return nil, err
	}
	return &Transaction{s: Snapshot{
		ID:            wallet.OpeningTransactionID(walletID),
		WalletID:      walletID,
		PlayerID:      playerID,
		Kind:          Opening,
		Money:         amount,
		CorrelationID: correlationID,
		Status:        Processed,
		Result:        &Result{Balance: amount, WalletVersion: 1},
		CreatedAt:     now,
		UpdatedAt:     now,
		CompletedAt:   now,
	}}, nil
}

func Rehydrate(s Snapshot) (*Transaction, error) {
	var v domain.Validation
	v.Check(s.ID != uuid.Nil, "id", domain.ErrRequired)
	v.Check(s.WalletID != uuid.Nil, "walletId", domain.ErrRequired)
	v.Check(s.PlayerID != uuid.Nil, "playerId", domain.ErrRequired)
	v.Check(s.Kind.Valid(), "kind", domain.ErrInvalidValue)
	v.CheckAmount("money", s.Money, s.Kind.amountRule())
	v.Check(s.CorrelationID != "", "correlationId", domain.ErrRequired)
	v.Check(!s.CreatedAt.IsZero(), "createdAt", domain.ErrRequired)
	v.Check(!s.UpdatedAt.IsZero(), "updatedAt", domain.ErrRequired)
	v.Check(s.Attempts >= 0, "attempts", domain.ErrInvalidValue)
	if s.Kind.Origin() == Internal {
		checkInternal(&v, s)
	} else {
		checkExternal(&v, s)
	}
	checkState(&v, s)
	if err := v.Err(); err != nil {
		return nil, err
	}
	return &Transaction{s: s.clone()}, nil
}

func checkReference(v *domain.Validation, k Kind, ref, self string) {
	switch {
	case ref == "":
		v.Check(!k.Reversal(), referenceField, domain.ErrRequired)
	case !k.acceptsReference(), ref == self:
		v.Add(referenceField, domain.ErrInvalidValue)
	}
}

func checkInternal(v *domain.Validation, s Snapshot) {
	external := s.ProviderID + s.ExternalTransactionID + s.IdempotencyKey + s.PayloadHash + s.RoundID + s.GameID +
		s.ReferenceExternalTransactionID
	v.Check(external == "" && s.ReferenceTransactionID == uuid.Nil, "origin", domain.ErrInvalidValue)
	v.Check(s.ID == wallet.OpeningTransactionID(s.WalletID), "id", domain.ErrInvalidValue)
	v.Check(s.Status == Processed, "status", domain.ErrInvalidValue)
}

func checkExternal(v *domain.Validation, s Snapshot) {
	v.Check(s.ProviderID != "", "providerId", domain.ErrRequired)
	v.Check(s.ExternalTransactionID != "", "externalTransactionId", domain.ErrRequired)
	v.Check(s.IdempotencyKey != "", "idempotencyKey", domain.ErrRequired)
	v.Check(s.PayloadHash != "", "payloadHash", domain.ErrRequired)
	v.Check(s.RoundID != "", "roundId", domain.ErrRequired)
	v.Check(s.GameID != "", "gameId", domain.ErrRequired)
	checkReference(v, s.Kind, s.ReferenceExternalTransactionID, s.ExternalTransactionID)
	v.Check(s.ReferenceTransactionID == uuid.Nil || s.ReferenceExternalTransactionID != "", "referenceTransactionId", domain.ErrInvalidValue)
}

func checkState(v *domain.Validation, s Snapshot) {
	v.Check(s.Status.Valid() && s.Status != Pending, "status", domain.ErrInvalidValue)
	switch s.Status {
	case Rejected:
		v.Check(s.FailureCode.Rejection(), "failureCode", domain.ErrInvalidValue)
	case Failed:
		v.Check(s.FailureCode == ProcessingFailed, "failureCode", domain.ErrInvalidValue)
	default:
		v.Check(s.FailureCode == "", "failureCode", domain.ErrInvalidValue)
	}
	switch {
	case s.Result != nil:
		checkResult(v, *s.Result, s.Money)
		v.Check(s.Status == Processed || s.Status == Rejected, "result", domain.ErrInvalidValue)
	case s.Status == Processed:
		v.Add("result", domain.ErrRequired)
	}
	if s.Status == PendingReference {
		v.Check(s.ReferenceExternalTransactionID != "", referenceField, domain.ErrRequired)
		v.Check(s.Attempts >= 1, "attempts", domain.ErrInvalidValue)
		v.Check(!s.NextAttemptAt.IsZero(), "nextAttemptAt", domain.ErrRequired)
		v.Check(!s.ReferenceDeadlineAt.IsZero(), "referenceDeadlineAt", domain.ErrRequired)
	}
	v.Check(s.Status.Terminal() != s.CompletedAt.IsZero(), "completedAt", domain.ErrInvalidValue)
}

func checkResult(v *domain.Validation, r Result, m money.Money) {
	v.CheckAmount("result.balance", r.Balance, domain.NonNegative)
	if cur, err := r.Balance.Currency(); err == nil {
		want, _ := m.Currency()
		v.Check(cur == want, "result.balance", domain.ErrInvalidValue)
	}
	v.Check(r.WalletVersion >= 1, "result.walletVersion", domain.ErrInvalidValue)
}

func (s Snapshot) clone() Snapshot {
	if s.Result != nil {
		r := *s.Result
		s.Result = &r
	}
	return s
}

func (t *Transaction) Snapshot() Snapshot {
	return t.s.clone()
}

func (t *Transaction) ID() uuid.UUID {
	return t.s.ID
}

func (t *Transaction) WalletID() uuid.UUID {
	return t.s.WalletID
}

func (t *Transaction) Kind() Kind {
	return t.s.Kind
}

func (t *Transaction) Status() Status {
	return t.s.Status
}

func (t *Transaction) FailureCode() FailureCode {
	return t.s.FailureCode
}

func (t *Transaction) MarkProcessed(r Result, now time.Time) error {
	if err := t.transition(Processed, now, Pending); err != nil {
		return err
	}
	var v domain.Validation
	checkResult(&v, r, t.s.Money)
	if err := v.Err(); err != nil {
		return err
	}
	t.s.Result = &r
	t.complete(Processed, "", now)
	return nil
}

func (t *Transaction) Reject(code FailureCode, r *Result, now time.Time) error {
	if err := t.transition(Rejected, now, Pending, PendingReference); err != nil {
		return err
	}
	var v domain.Validation
	v.Check(code.Rejection(), "failureCode", domain.ErrInvalidValue)
	if r != nil {
		checkResult(&v, *r, t.s.Money)
	}
	if err := v.Err(); err != nil {
		return err
	}
	if r != nil {
		copied := *r
		t.s.Result = &copied
	}
	t.complete(Rejected, code, now)
	return nil
}

func (t *Transaction) Fail(code FailureCode, now time.Time) error {
	if err := t.transition(Failed, now, Pending, PendingReference); err != nil {
		return err
	}
	if code != ProcessingFailed {
		return domain.NewFieldError("failureCode", domain.ErrInvalidValue)
	}
	t.complete(Failed, code, now)
	return nil
}

func (t *Transaction) AwaitReference(next, deadline, now time.Time) error {
	if err := t.transition(PendingReference, now, Pending); err != nil {
		return err
	}
	if t.s.ReferenceExternalTransactionID == "" {
		return fmt.Errorf("%w: %s has no reference to await", domain.ErrInvalidTransition, t.s.Kind)
	}
	var v domain.Validation
	v.Check(!next.Before(now), "nextAttemptAt", domain.ErrInvalidValue)
	v.Check(deadline.After(now), "referenceDeadlineAt", domain.ErrInvalidValue)
	if err := v.Err(); err != nil {
		return err
	}
	t.s.Status = PendingReference
	t.s.Attempts++
	t.s.NextAttemptAt, t.s.ReferenceDeadlineAt, t.s.UpdatedAt = next, deadline, now
	return nil
}

func (t *Transaction) Reschedule(next, now time.Time) error {
	if err := t.transition(PendingReference, now, PendingReference); err != nil {
		return err
	}
	if next.Before(now) {
		return domain.NewFieldError("nextAttemptAt", domain.ErrInvalidValue)
	}
	t.s.Attempts++
	t.s.NextAttemptAt, t.s.UpdatedAt = next, now
	return nil
}

func (t *Transaction) ResumeProcessing(now time.Time) error {
	if err := t.transition(Pending, now, PendingReference); err != nil {
		return err
	}
	t.s.Status, t.s.UpdatedAt = Pending, now
	return nil
}

func (t *Transaction) transition(to Status, now time.Time, from ...Status) error {
	switch {
	case t == nil || !t.s.Status.Valid():
		return domain.ErrUninitialized
	case t.s.Status.Terminal():
		return fmt.Errorf("%w: transaction %s is %s", domain.ErrTerminalState, t.s.ID, t.s.Status)
	case !slices.Contains(from, t.s.Status):
		return fmt.Errorf("%w: %s to %s", domain.ErrInvalidTransition, t.s.Status, to)
	case now.IsZero():
		return domain.NewFieldError("now", domain.ErrRequired)
	}
	return nil
}

func (t *Transaction) complete(status Status, code FailureCode, now time.Time) {
	t.s.Status, t.s.FailureCode, t.s.UpdatedAt, t.s.CompletedAt = status, code, now, now
}

func (t *Transaction) linkReference(id uuid.UUID) error {
	if t.s.ReferenceTransactionID != uuid.Nil && t.s.ReferenceTransactionID != id {
		return domain.NewFieldError("referenceTransactionId", domain.ErrInvalidValue)
	}
	t.s.ReferenceTransactionID = id
	return nil
}
