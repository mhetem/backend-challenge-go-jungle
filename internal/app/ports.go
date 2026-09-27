package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/events"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

type TxRunner interface {
	InTx(ctx context.Context, fn func(context.Context, Store) error) error
	InReadOnlySnapshot(ctx context.Context, fn func(context.Context, Store) error) error
}

type Store interface {
	Wallets() Wallets
	Transactions() Transactions
	Ledger() Ledger
	Outbox() Outbox
	Inbox() Inbox
}

type Wallets interface {
	Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	GetByPlayer(ctx context.Context, playerID uuid.UUID, cur money.Currency) (*wallet.Wallet, error)
	Insert(ctx context.Context, w *wallet.Wallet) error
	Update(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error
}

type Transactions interface {
	Get(ctx context.Context, id uuid.UUID) (*wager.Transaction, error)
	GetByIdempotencyKey(ctx context.Context, providerID, key string) (*wager.Transaction, error)
	GetByExternalID(ctx context.Context, providerID, externalID string) (*wager.Transaction, error)
	Reversed(ctx context.Context, id uuid.UUID) (bool, error)
	Insert(ctx context.Context, tx *wager.Transaction) error
	UpdateState(ctx context.Context, tx *wager.Transaction) error
	WakeDependents(ctx context.Context, providerID, externalID string, walletID uuid.UUID, now time.Time) (int64, error)
}

type Ledger interface {
	Insert(ctx context.Context, e ledger.Entry) error
	Page(ctx context.Context, walletID uuid.UUID, afterVersion int64, limit int) ([]ledger.Entry, error)
	Summarize(ctx context.Context, walletID uuid.UUID, cur money.Currency) (LedgerSummary, error)
}

type LedgerSummary struct {
	Entries      int64
	FirstVersion int64
	LastVersion  int64
	Net          money.Money
}

type Outbox interface {
	Insert(ctx context.Context, evs ...events.Event) error
}

type InboxMessage struct {
	Consumer      string
	MessageID     string
	PayloadHash   string
	ReceivedAt    time.Time
	CompletedAt   time.Time
	Outcome       string
	TransactionID uuid.UUID
}

type Inbox interface {
	Receive(ctx context.Context, m InboxMessage) (*InboxMessage, error)
	Complete(ctx context.Context, m InboxMessage) error
}

type Metrics interface {
	ReconciliationDiverged()
}
