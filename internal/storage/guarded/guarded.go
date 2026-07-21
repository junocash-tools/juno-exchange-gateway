package guarded

import (
	"context"
	"errors"
	"fmt"

	"github.com/junocash-tools/juno-exchange-gateway/internal/installation"
	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
)

type exactAddressStore interface {
	AllocateAddressAt(context.Context, string, uint32, string, storage.DeriveFunc) (storage.Address, error)
}

// Store guards address allocation with an external crash-safe high-water
// ledger. It embeds all other storage behavior unchanged.
type Store struct {
	storage.Store
	exact  exactAddressStore
	ledger *installation.State
}

func New(base storage.Store, ledger *installation.State) (*Store, error) {
	if base == nil {
		return nil, errors.New("base storage is required")
	}
	if ledger == nil {
		return nil, errors.New("installation state is required")
	}
	exact, ok := base.(exactAddressStore)
	if !ok {
		return nil, errors.New("base storage does not support exact address allocation")
	}
	return &Store{Store: base, exact: exact, ledger: ledger}, nil
}

func (s *Store) AllocateAddress(ctx context.Context, walletID, label string, derive storage.DeriveFunc) (storage.Address, error) {
	if derive == nil {
		return storage.Address{}, errors.New("derive callback is required")
	}
	index, err := s.ledger.ReserveAddressIndex(walletID)
	if err != nil {
		return storage.Address{}, err
	}
	address, err := s.exact.AllocateAddressAt(ctx, walletID, index, label, derive)
	if err == nil {
		return address, nil
	}
	if markErr := s.ledger.MarkAddressIndexSkipped(walletID, index); markErr != nil {
		return storage.Address{}, fmt.Errorf("address allocation failed after reserving index %d: %v; recording the skipped index also failed: %w", index, err, markErr)
	}
	return storage.Address{}, err
}
