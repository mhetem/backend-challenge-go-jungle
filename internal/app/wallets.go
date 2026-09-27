package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/ledger"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wager"
	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/wallet"
)

const (
	DefaultLedgerLimit = 50
	MaxLedgerLimit     = 200
)

type LedgerPage struct {
	Entries    []ledger.Snapshot
	NextCursor string
}

type Reconciliation struct {
	WalletID           uuid.UUID
	StoredBalance      money.Money
	CalculatedBalance  money.Money
	PostedBalance      money.Money
	Difference         money.Money
	CheckedEntries     int64
	ContinuousVersions bool
	Consistent         bool
}

type TrialBalance struct {
	Ledger         ledger.TrialBalance
	Wallets        int64
	WalletBalances money.Money
	Consistent     bool
}

type WalletService struct {
	tx      TxRunner
	clock   Clock
	ids     IDs
	log     *slog.Logger
	metrics Metrics
}

func NewWalletService(tx TxRunner, clock Clock, ids IDs, log *slog.Logger, metrics Metrics) *WalletService {
	return &WalletService{tx: tx, clock: clock, ids: ids, log: log, metrics: metrics}
}

func (s *WalletService) Open(ctx context.Context, cmd OpenWallet) (snapshot wallet.Snapshot, err error) {
	ctx, span := startSpan(ctx, "WalletService.Open")
	defer func() {
		if snapshot.ID != uuid.Nil {
			span.SetAttributes(attribute.String("wallet.id", snapshot.ID.String()))
		}
		endSpan(span, err)
	}()
	opened, err := wager.Open(s.ids(), cmd.PlayerID, cmd.InitialBalance, cmd.CorrelationID, s.clock())
	if err != nil {
		return wallet.Snapshot{}, err
	}
	err = s.tx.InTx(ctx, func(ctx context.Context, st Store) error {
		if err := st.Wallets().Insert(ctx, opened.Wallet); err != nil || opened.Transaction == nil {
			return err
		}
		if err := st.Transactions().Insert(ctx, opened.Transaction); err != nil {
			return err
		}
		if err := st.Ledger().Insert(ctx, *opened.Entry); err != nil {
			return err
		}
		if err := st.Ledger().Post(ctx, opened.Journal); err != nil {
			return err
		}
		return st.Outbox().Insert(ctx, opened.Events...)
	})
	switch {
	case errors.Is(err, domain.ErrWalletExists):
		return wallet.Snapshot{}, s.existing(ctx, cmd.PlayerID, opened.Wallet.Currency())
	case err != nil:
		return wallet.Snapshot{}, err
	}
	return opened.Wallet.Snapshot(), nil
}

func (s *WalletService) existing(ctx context.Context, playerID uuid.UUID, cur money.Currency) error {
	var id uuid.UUID
	err := s.tx.InReadOnlySnapshot(ctx, func(ctx context.Context, st Store) error {
		w, err := st.Wallets().GetByPlayer(ctx, playerID, cur)
		if err != nil {
			return err
		}
		id = w.ID()
		return nil
	})
	if err != nil {
		return err
	}
	return &WalletExistsError{WalletID: id}
}

func (s *WalletService) Get(ctx context.Context, id uuid.UUID) (wallet.Snapshot, error) {
	var snapshot wallet.Snapshot
	err := s.tx.InReadOnlySnapshot(ctx, func(ctx context.Context, st Store) error {
		w, err := st.Wallets().Get(ctx, id)
		if err != nil {
			return err
		}
		snapshot = w.Snapshot()
		return nil
	})
	return snapshot, err
}

func (s *WalletService) Ledger(ctx context.Context, walletID uuid.UUID, cursor string, limit int) (LedgerPage, error) {
	var v domain.Validation
	after := decodeCursor(&v, cursor)
	v.Check(limit >= 0 && limit <= MaxLedgerLimit, "limit", domain.ErrInvalidValue)
	if err := v.Err(); err != nil {
		return LedgerPage{}, err
	}
	if limit == 0 {
		limit = DefaultLedgerLimit
	}
	var entries []ledger.Entry
	err := s.tx.InReadOnlySnapshot(ctx, func(ctx context.Context, st Store) error {
		if _, err := st.Wallets().Get(ctx, walletID); err != nil {
			return err
		}
		var err error
		entries, err = st.Ledger().Page(ctx, walletID, after, limit+1)
		return err
	})
	if err != nil {
		return LedgerPage{}, err
	}
	page := LedgerPage{Entries: make([]ledger.Snapshot, 0, min(len(entries), limit))}
	for _, e := range entries[:min(len(entries), limit)] {
		page.Entries = append(page.Entries, e.Snapshot())
	}
	if len(entries) > limit {
		page.NextCursor = encodeCursor(page.Entries[limit-1].WalletVersion)
	}
	return page, nil
}

func (s *WalletService) Reconcile(ctx context.Context, walletID uuid.UUID) (result Reconciliation, err error) {
	ctx, span := startSpan(ctx, "WalletService.Reconcile", attribute.String("wallet.id", walletID.String()))
	defer func() {
		span.SetAttributes(attribute.Bool("wallet.consistent", result.Consistent))
		endSpan(span, err)
	}()
	var w *wallet.Wallet
	var summary LedgerSummary
	err = s.tx.InReadOnlySnapshot(ctx, func(ctx context.Context, st Store) error {
		var err error
		if w, err = st.Wallets().Get(ctx, walletID); err != nil {
			return err
		}
		summary, err = st.Ledger().Summarize(ctx, walletID, w.Currency())
		return err
	})
	if err != nil {
		return Reconciliation{}, err
	}
	difference, err := w.Balance().Sub(summary.Net)
	if err != nil {
		return Reconciliation{}, fmt.Errorf("%w: wallet %s: %w", ErrPermanent, walletID, err)
	}
	balanced, _ := difference.IsZero()
	r := Reconciliation{
		WalletID:           walletID,
		StoredBalance:      w.Balance(),
		CalculatedBalance:  summary.Net,
		PostedBalance:      summary.Posted,
		Difference:         difference,
		CheckedEntries:     summary.Entries,
		ContinuousVersions: continuous(w.Version(), summary),
	}
	r.Consistent = balanced && r.ContinuousVersions && r.PostedBalance == r.StoredBalance
	if !r.Consistent {
		s.log.WarnContext(ctx, "wallet diverges from its ledger",
			"walletId", walletID,
			"storedBalance", r.StoredBalance.String(),
			"calculatedBalance", r.CalculatedBalance.String(),
			"postedBalance", r.PostedBalance.String(),
			"difference", r.Difference.String(),
			"checkedEntries", r.CheckedEntries,
			"walletVersion", w.Version(),
			"firstEntryVersion", summary.FirstVersion,
			"lastEntryVersion", summary.LastVersion,
			"continuousVersions", r.ContinuousVersions,
		)
		s.metrics.ReconciliationDiverged()
	}
	return r, nil
}

func (s *WalletService) TrialBalance(ctx context.Context) (balances []TrialBalance, err error) {
	ctx, span := startSpan(ctx, "WalletService.TrialBalance")
	defer func() { endSpan(span, err) }()
	var accounts []ledger.AccountTotals
	var wallets []WalletTotals
	err = s.tx.InReadOnlySnapshot(ctx, func(ctx context.Context, st Store) error {
		var err error
		if accounts, err = st.Ledger().Totals(ctx); err != nil {
			return err
		}
		wallets, err = st.Wallets().Totals(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	byCurrency := map[money.Currency][]ledger.AccountTotals{}
	for _, a := range accounts {
		cur, _ := a.Debits.Currency()
		byCurrency[cur] = append(byCurrency[cur], a)
	}
	stored := map[money.Currency]WalletTotals{}
	for _, w := range wallets {
		cur, _ := w.Balance.Currency()
		stored[cur] = w
		if _, ok := byCurrency[cur]; !ok {
			byCurrency[cur] = nil
		}
	}
	balances = []TrialBalance{}
	for _, cur := range slices.Sorted(maps.Keys(byCurrency)) {
		tb, err := s.trialBalance(ctx, cur, byCurrency[cur], stored[cur])
		if err != nil {
			return nil, err
		}
		balances = append(balances, tb)
	}
	return balances, nil
}

func (s *WalletService) trialBalance(ctx context.Context, cur money.Currency, accounts []ledger.AccountTotals, wallets WalletTotals) (TrialBalance, error) {
	books, err := ledger.NewTrialBalance(cur, accounts)
	if err != nil {
		return TrialBalance{}, fmt.Errorf("%w: %s trial balance: %w", ErrPermanent, cur, err)
	}
	tb := TrialBalance{Ledger: books, Wallets: wallets.Wallets, WalletBalances: wallets.Balance}
	if tb.Wallets == 0 {
		tb.WalletBalances, _ = money.Zero(cur)
	}
	players := books.Account(ledger.PlayerBalances).Balance
	tb.Consistent = books.Balanced() && players == tb.WalletBalances
	if !tb.Consistent {
		s.log.WarnContext(ctx, "ledger trial balance diverges",
			"currency", cur,
			"debits", books.Debits.String(),
			"credits", books.Credits.String(),
			"playerBalances", players.String(),
			"walletBalances", tb.WalletBalances.String(),
			"wallets", tb.Wallets,
		)
		s.metrics.ReconciliationDiverged()
	}
	return tb, nil
}

func continuous(version int64, s LedgerSummary) bool {
	if s.Entries == 0 {
		return version == 1
	}
	return s.LastVersion == version && s.LastVersion-s.FirstVersion+1 == s.Entries && s.FirstVersion <= 2
}

func encodeCursor(version int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(version, 10)))
}

func decodeCursor(v *domain.Validation, cursor string) int64 {
	if cursor == "" {
		return 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	version, parseErr := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || parseErr != nil || version < 1 || encodeCursor(version) != cursor {
		v.Add("cursor", domain.ErrInvalidValue)
		return 0
	}
	return version
}
