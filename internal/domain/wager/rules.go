package wager

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/events"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

type Rules struct {
	referenceTTL time.Duration
	maxAttempts  int
	backoff      func(attempt int) time.Duration
}

type Reference struct {
	Transaction *Transaction
	Reversed    bool
}

type Outcome struct {
	Entry   *ledger.Entry
	Journal ledger.Journal
	Events  []events.Event
}

func NewRules(referenceTTL time.Duration, maxAttempts int, backoff func(attempt int) time.Duration) (Rules, error) {
	var v domain.Validation
	v.Check(referenceTTL > 0, "referenceTTL", domain.ErrInvalidValue)
	v.Check(maxAttempts > 0, "maxAttempts", domain.ErrInvalidValue)
	v.Check(backoff != nil, "backoff", domain.ErrRequired)
	if err := v.Err(); err != nil {
		return Rules{}, err
	}
	return Rules{referenceTTL: referenceTTL, maxAttempts: maxAttempts, backoff: backoff}, nil
}

func (r Rules) Apply(tx *Transaction, w *wallet.Wallet, ref *Reference, now time.Time) (Outcome, error) {
	if err := r.check(tx, w, ref, now); err != nil {
		return Outcome{}, err
	}
	if code := checkWallet(tx, w); code != "" {
		return reject(tx, nil, ref, code, now)
	}
	result := &Result{Balance: w.Balance(), WalletVersion: w.Version()}
	if tx.s.ReferenceExternalTransactionID != "" {
		if ref == nil {
			return r.unavailable(tx, result, nil, ReferenceNotFound, now)
		}
		switch code := checkReferenced(tx, ref.Transaction); {
		case code != "":
			return reject(tx, result, ref, code, now)
		case !ref.Transaction.s.Status.Terminal():
			return r.unavailable(tx, result, ref, ReferenceNotSettled, now)
		case ref.Transaction.s.Status != Processed:
			return reject(tx, result, ref, ReferenceNotProcessed, now)
		case tx.s.Kind.Reversal() && ref.Reversed:
			return reject(tx, result, ref, AlreadyReversed, now)
		}
	}
	if tx.s.Status == PendingReference {
		if err := tx.ResumeProcessing(now); err != nil {
			return Outcome{}, err
		}
	}
	dir, moves := direction(tx, ref)
	if !moves {
		return process(tx, w, ref, nil, now)
	}
	entry, err := move(w, dir, tx, now)
	switch {
	case errors.Is(err, domain.ErrInsufficientFunds) && tx.s.Kind == Rollback:
		return reject(tx, result, ref, ReversalInsufficientFunds, now)
	case errors.Is(err, domain.ErrInsufficientFunds):
		return reject(tx, result, ref, InsufficientFunds, now)
	case errors.Is(err, domain.ErrBalanceOverflow):
		return reject(tx, result, ref, BalanceOverflow, now)
	case err != nil:
		return Outcome{}, err
	}
	return process(tx, w, ref, &entry, now)
}

func (r Rules) check(tx *Transaction, w *wallet.Wallet, ref *Reference, now time.Time) error {
	switch {
	case r.backoff == nil, tx == nil, !tx.s.Status.Valid():
		return domain.ErrUninitialized
	case tx.s.Status.Terminal():
		return fmt.Errorf("%w: transaction %s is %s", domain.ErrTerminalState, tx.s.ID, tx.s.Status)
	case now.IsZero():
		return domain.NewFieldError("now", domain.ErrRequired)
	case w != nil && w.ID() != tx.s.WalletID:
		return domain.NewFieldError("wallet", domain.ErrInvalidValue)
	case ref == nil:
		return nil
	case ref.Transaction == nil, !ref.Transaction.s.Status.Valid():
		return domain.NewFieldError("reference", domain.ErrUninitialized)
	case tx.s.ReferenceExternalTransactionID == "",
		ref.Transaction.s.ID == tx.s.ID,
		tx.s.ReferenceTransactionID != uuid.Nil && tx.s.ReferenceTransactionID != ref.Transaction.s.ID:
		return domain.NewFieldError("reference", domain.ErrInvalidValue)
	}
	return nil
}

func checkWallet(tx *Transaction, w *wallet.Wallet) FailureCode {
	switch {
	case w == nil:
		return WalletNotFound
	case w.PlayerID() != tx.s.PlayerID:
		return WalletPlayerMismatch
	case !sameCurrency(w.Balance(), tx.s.Money):
		return CurrencyMismatch
	}
	return ""
}

func checkReferenced(tx, ref *Transaction) FailureCode {
	switch {
	case !tx.s.Kind.canReference(ref.s.Kind) && tx.s.Kind == Win:
		return ReferenceMismatch
	case !tx.s.Kind.canReference(ref.s.Kind):
		return ReferenceKindNotReversible
	case ref.s.ProviderID != tx.s.ProviderID,
		ref.s.PlayerID != tx.s.PlayerID,
		ref.s.WalletID != tx.s.WalletID,
		ref.s.RoundID != tx.s.RoundID,
		!sameCurrency(ref.s.Money, tx.s.Money):
		return ReferenceMismatch
	case tx.s.Kind.Reversal() && ref.s.Money != tx.s.Money:
		return ReferenceAmountMismatch
	}
	return ""
}

func (r Rules) unavailable(tx *Transaction, result *Result, ref *Reference, code FailureCode, now time.Time) (Outcome, error) {
	attempts := tx.s.Attempts + 1
	deadline := tx.s.ReferenceDeadlineAt
	if tx.s.Status == Pending {
		deadline = now.Add(r.referenceTTL)
	}
	if attempts >= r.maxAttempts || !now.Before(deadline) {
		return reject(tx, result, ref, code, now)
	}
	next := now.Add(max(r.backoff(attempts), 0))
	if next.After(deadline) {
		next = deadline
	}
	if err := link(tx, ref); err != nil {
		return Outcome{}, err
	}
	if tx.s.Status == PendingReference {
		return Outcome{}, tx.Reschedule(next, now)
	}
	if err := tx.AwaitReference(next, deadline, now); err != nil {
		return Outcome{}, err
	}
	return Outcome{Events: []events.Event{tx.pendingReferenceEvent(now)}}, nil
}

func direction(tx *Transaction, ref *Reference) (ledger.Direction, bool) {
	switch tx.s.Kind {
	case Bet:
		return ledger.Debit, true
	case Win, Refund:
		return ledger.Credit, true
	case Rollback:
		if ref.Transaction.s.Kind == Bet {
			return ledger.Credit, true
		}
		return ledger.Debit, true
	}
	return "", false
}

func move(w *wallet.Wallet, dir ledger.Direction, tx *Transaction, now time.Time) (ledger.Entry, error) {
	if dir == ledger.Debit {
		return w.Debit(tx.s.ID, tx.s.Money, now)
	}
	return w.Credit(tx.s.ID, tx.s.Money, now)
}

func process(tx *Transaction, w *wallet.Wallet, ref *Reference, entry *ledger.Entry, now time.Time) (Outcome, error) {
	var journal ledger.Journal
	if entry != nil {
		var err error
		if journal, err = ledger.Transfer(*entry, tx.s.Kind.counterparty()); err != nil {
			return Outcome{}, err
		}
	}
	if err := link(tx, ref); err != nil {
		return Outcome{}, err
	}
	if err := tx.MarkProcessed(Result{Balance: w.Balance(), WalletVersion: w.Version()}, now); err != nil {
		return Outcome{}, err
	}
	return Outcome{Entry: entry, Journal: journal, Events: tx.processedEvents(entry, now)}, nil
}

func reject(tx *Transaction, result *Result, ref *Reference, code FailureCode, now time.Time) (Outcome, error) {
	if err := link(tx, ref); err != nil {
		return Outcome{}, err
	}
	if err := tx.Reject(code, result, now); err != nil {
		return Outcome{}, err
	}
	return Outcome{Events: []events.Event{tx.rejectedEvent(now)}}, nil
}

func link(tx *Transaction, ref *Reference) error {
	if ref == nil {
		return nil
	}
	return tx.linkReference(ref.Transaction.s.ID)
}

func sameCurrency(a, b money.Money) bool {
	ca, errA := a.Currency()
	cb, errB := b.Currency()
	return errA == nil && errB == nil && ca == cb
}

func eventID(transactionID uuid.UUID, t events.Type) uuid.UUID {
	return domain.DeriveID("event", transactionID.String(), string(t))
}

func (t *Transaction) meta(eventType events.Type, causationID string, now time.Time) events.Meta {
	return events.Meta{
		EventID:       eventID(t.s.ID, eventType),
		CorrelationID: t.s.CorrelationID,
		CausationID:   causationID,
		OccurredAt:    now,
	}
}

func (t *Transaction) eventData() events.TransactionData {
	return events.TransactionData{
		TransactionID:                  t.s.ID,
		Origin:                         string(t.s.Kind.Origin()),
		ProviderID:                     t.s.ProviderID,
		ExternalTransactionID:          t.s.ExternalTransactionID,
		WalletID:                       t.s.WalletID,
		PlayerID:                       t.s.PlayerID,
		RoundID:                        t.s.RoundID,
		GameID:                         t.s.GameID,
		Kind:                           string(t.s.Kind),
		Money:                          t.s.Money,
		ReferenceExternalTransactionID: t.s.ReferenceExternalTransactionID,
		ReferenceTransactionID:         t.s.ReferenceTransactionID,
	}
}

func (t *Transaction) processedEvents(entry *ledger.Entry, now time.Time) []events.Event {
	evs := []events.Event{events.NewWagerTransactionProcessed(t.meta(events.TypeWagerTransactionProcessed, "", now), events.WagerTransactionProcessedData{
		TransactionData: t.eventData(),
		Balance:         t.s.Result.Balance,
		WalletVersion:   t.s.Result.WalletVersion,
	})}
	if entry == nil {
		return evs
	}
	e := entry.Snapshot()
	return append(evs, events.NewWalletBalanceChanged(t.meta(events.TypeWalletBalanceChanged, t.s.ID.String(), now), events.WalletBalanceChangedData{
		WalletID:      e.WalletID,
		TransactionID: e.TransactionID,
		Direction:     string(e.Direction),
		Money:         e.Amount,
		BalanceBefore: e.BalanceBefore,
		BalanceAfter:  e.BalanceAfter,
		WalletVersion: e.WalletVersion,
	}))
}

func (t *Transaction) rejectedEvent(now time.Time) events.Event {
	d := events.WagerTransactionRejectedData{TransactionData: t.eventData(), FailureCode: string(t.s.FailureCode)}
	if t.s.Result != nil {
		d.Balance, d.WalletVersion = t.s.Result.Balance, t.s.Result.WalletVersion
	}
	return events.NewWagerTransactionRejected(t.meta(events.TypeWagerTransactionRejected, "", now), d)
}

func (t *Transaction) pendingReferenceEvent(now time.Time) events.Event {
	return events.NewWagerTransactionPendingReference(t.meta(events.TypeWagerTransactionPendingReference, "", now), events.WagerTransactionPendingReferenceData{
		TransactionData:     t.eventData(),
		ReferenceDeadlineAt: t.s.ReferenceDeadlineAt,
	})
}
