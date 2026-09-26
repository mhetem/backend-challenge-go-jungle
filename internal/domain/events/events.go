package events

import (
	"time"

	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/internal/domain/money"
)

type Type string

const (
	TypeWagerTransactionProcessed        Type = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         Type = "WagerTransactionRejected"
	TypeWagerTransactionPendingReference Type = "WagerTransactionPendingReference"
	TypeWalletBalanceChanged             Type = "WalletBalanceChanged"
)

func (t Type) AggregateType() string {
	switch t {
	case TypeWagerTransactionProcessed, TypeWagerTransactionRejected, TypeWagerTransactionPendingReference:
		return "WagerTransaction"
	case TypeWalletBalanceChanged:
		return "Wallet"
	}
	return ""
}

type Event interface {
	EventHeader() Header
	PartitionKey() uuid.UUID
}

type Meta struct {
	EventID       uuid.UUID
	CorrelationID string
	CausationID   string
	OccurredAt    time.Time
}

type Header struct {
	EventID       uuid.UUID `json:"eventId"`
	EventType     Type      `json:"eventType"`
	AggregateID   uuid.UUID `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
}

func (h Header) EventHeader() Header {
	return h
}

func (m Meta) header(t Type, version int, aggregateID uuid.UUID) Header {
	return Header{
		EventID:       m.EventID,
		EventType:     t,
		AggregateID:   aggregateID,
		CorrelationID: m.CorrelationID,
		CausationID:   m.CausationID,
		OccurredAt:    m.OccurredAt.UTC(),
		Version:       version,
	}
}

type TransactionData struct {
	TransactionID                  uuid.UUID   `json:"transactionId"`
	Origin                         string      `json:"origin"`
	ProviderID                     string      `json:"providerId,omitempty"`
	ExternalTransactionID          string      `json:"externalTransactionId,omitempty"`
	WalletID                       uuid.UUID   `json:"walletId"`
	PlayerID                       uuid.UUID   `json:"playerId"`
	RoundID                        string      `json:"roundId,omitempty"`
	GameID                         string      `json:"gameId,omitempty"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         uuid.UUID   `json:"referenceTransactionId,omitzero"`
}

type WagerTransactionProcessedData struct {
	TransactionData
	Balance       money.Money `json:"balance"`
	WalletVersion int64       `json:"walletVersion"`
}

type WagerTransactionProcessed struct {
	Header
	Data WagerTransactionProcessedData `json:"data"`
}

func NewWagerTransactionProcessed(m Meta, d WagerTransactionProcessedData) WagerTransactionProcessed {
	return WagerTransactionProcessed{Header: m.header(TypeWagerTransactionProcessed, 1, d.TransactionID), Data: d}
}

func (e WagerTransactionProcessed) PartitionKey() uuid.UUID {
	return e.Data.WalletID
}

type WagerTransactionRejectedData struct {
	TransactionData
	FailureCode   string      `json:"failureCode"`
	Balance       money.Money `json:"balance,omitzero"`
	WalletVersion int64       `json:"walletVersion,omitzero"`
}

type WagerTransactionRejected struct {
	Header
	Data WagerTransactionRejectedData `json:"data"`
}

func NewWagerTransactionRejected(m Meta, d WagerTransactionRejectedData) WagerTransactionRejected {
	return WagerTransactionRejected{Header: m.header(TypeWagerTransactionRejected, 1, d.TransactionID), Data: d}
}

func (e WagerTransactionRejected) PartitionKey() uuid.UUID {
	return e.Data.WalletID
}

type WagerTransactionPendingReferenceData struct {
	TransactionData
	ReferenceDeadlineAt time.Time `json:"referenceDeadlineAt"`
}

type WagerTransactionPendingReference struct {
	Header
	Data WagerTransactionPendingReferenceData `json:"data"`
}

func NewWagerTransactionPendingReference(m Meta, d WagerTransactionPendingReferenceData) WagerTransactionPendingReference {
	d.ReferenceDeadlineAt = d.ReferenceDeadlineAt.UTC()
	return WagerTransactionPendingReference{Header: m.header(TypeWagerTransactionPendingReference, 1, d.TransactionID), Data: d}
}

func (e WagerTransactionPendingReference) PartitionKey() uuid.UUID {
	return e.Data.WalletID
}

type WalletBalanceChangedData struct {
	WalletID      uuid.UUID   `json:"walletId"`
	TransactionID uuid.UUID   `json:"transactionId"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
}

type WalletBalanceChanged struct {
	Header
	Data WalletBalanceChangedData `json:"data"`
}

func NewWalletBalanceChanged(m Meta, d WalletBalanceChangedData) WalletBalanceChanged {
	return WalletBalanceChanged{Header: m.header(TypeWalletBalanceChanged, 1, d.WalletID), Data: d}
}

func (e WalletBalanceChanged) PartitionKey() uuid.UUID {
	return e.Data.WalletID
}
