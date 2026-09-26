package wager

type Status string

const (
	Pending          Status = "PENDING"
	PendingReference Status = "PENDING_REFERENCE"
	Processed        Status = "PROCESSED"
	Rejected         Status = "REJECTED"
	Failed           Status = "FAILED"
)

func (s Status) Valid() bool {
	switch s {
	case Pending, PendingReference, Processed, Rejected, Failed:
		return true
	}
	return false
}

func (s Status) Terminal() bool {
	return s == Processed || s == Rejected || s == Failed
}

type FailureCode string

const (
	InsufficientFunds          FailureCode = "INSUFFICIENT_FUNDS"
	ReversalInsufficientFunds  FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	WalletNotFound             FailureCode = "WALLET_NOT_FOUND"
	WalletPlayerMismatch       FailureCode = "WALLET_PLAYER_MISMATCH"
	CurrencyMismatch           FailureCode = "CURRENCY_MISMATCH"
	ReferenceNotFound          FailureCode = "REFERENCE_NOT_FOUND"
	ReferenceNotSettled        FailureCode = "REFERENCE_NOT_SETTLED"
	ReferenceNotProcessed      FailureCode = "REFERENCE_NOT_PROCESSED"
	ReferenceMismatch          FailureCode = "REFERENCE_MISMATCH"
	ReferenceKindNotReversible FailureCode = "REFERENCE_KIND_NOT_REVERSIBLE"
	ReferenceAmountMismatch    FailureCode = "REFERENCE_AMOUNT_MISMATCH"
	AlreadyReversed            FailureCode = "ALREADY_REVERSED"
	BalanceOverflow            FailureCode = "BALANCE_OVERFLOW"
	ProcessingFailed           FailureCode = "PROCESSING_FAILED"
)

func (c FailureCode) Valid() bool {
	return c.Rejection() || c == ProcessingFailed
}

func (c FailureCode) Rejection() bool {
	switch c {
	case InsufficientFunds, ReversalInsufficientFunds, WalletNotFound, WalletPlayerMismatch, CurrencyMismatch,
		ReferenceNotFound, ReferenceNotSettled, ReferenceNotProcessed, ReferenceMismatch,
		ReferenceKindNotReversible, ReferenceAmountMismatch, AlreadyReversed, BalanceOverflow:
		return true
	}
	return false
}

func (c FailureCode) Correctable() bool {
	switch c {
	case WalletNotFound, WalletPlayerMismatch, CurrencyMismatch, ReferenceNotFound, ReferenceMismatch,
		ReferenceKindNotReversible, ReferenceAmountMismatch:
		return true
	}
	return false
}
