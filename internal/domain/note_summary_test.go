package domain

import (
	"strings"
	"testing"
)

func TestWalletNoteSummaryValidFor(t *testing.T) {
	smallest, largest := int64(10), int64(20)
	next, last := int64(1040), int64(1045)
	valid := WalletNoteSummary{
		WalletID: "hot", MinConfirmations: 100, MinNoteZat: 10, AsOfScannerHeight: 1000, AsOfScannerHash: strings.Repeat("a", 64),
		TotalUnspent: NoteValueSummary{NoteCount: 5, ValueZat: 51},
		Spendable:    SpendableNoteSummary{NoteValueSummary: NoteValueSummary{NoteCount: 2, ValueZat: 30}, SmallestNoteZat: &smallest, LargestNoteZat: &largest},
		Immature:     NoteValueSummary{NoteCount: 1, ValueZat: 9},
		PendingSpend: PendingSpendNoteSummary{NoteValueSummary: NoteValueSummary{NoteCount: 1, ValueZat: 11}, KnownExpiryCount: 1, NextExpiryHeight: &next, LastExpiryHeight: &last},
		BelowMinNote: NoteValueSummary{NoteCount: 1, ValueZat: 1},
	}
	if !valid.ValidFor("hot", 100, 10, 5) {
		t.Fatal("valid summary rejected")
	}

	tests := map[string]func(*WalletNoteSummary){
		"wrong identity":         func(s *WalletNoteSummary) { s.WalletID = "cold" },
		"invalid snapshot hash":  func(s *WalletNoteSummary) { s.AsOfScannerHash = strings.Repeat("A", 64) },
		"over cap":               func(s *WalletNoteSummary) { s.TotalUnspent.NoteCount = 6 },
		"broken partition":       func(s *WalletNoteSummary) { s.Immature.ValueZat++ },
		"missing extrema":        func(s *WalletNoteSummary) { s.Spendable.SmallestNoteZat = nil },
		"below policy floor":     func(s *WalletNoteSummary) { v := int64(9); s.Spendable.SmallestNoteZat = &v },
		"bad expiry count":       func(s *WalletNoteSummary) { s.PendingSpend.KnownExpiryCount = 2 },
		"expired pending marker": func(s *WalletNoteSummary) { v := int64(999); s.PendingSpend.NextExpiryHeight = &v },
		"bad expiry order":       func(s *WalletNoteSummary) { v := int64(1046); s.PendingSpend.NextExpiryHeight = &v },
		"unexpected null data":   func(s *WalletNoteSummary) { s.PendingSpend.KnownExpiryCount = 0 },
		"value without notes": func(s *WalletNoteSummary) {
			s.WitnessUnavailable.ValueZat = 1
			s.TotalUnspent.ValueZat++
		},
		"below floor aggregate too large": func(s *WalletNoteSummary) {
			s.BelowMinNote.ValueZat = 10
			s.TotalUnspent.ValueZat += 9
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if candidate.ValidFor("hot", 100, 10, 5) {
				t.Fatal("invalid summary accepted")
			}
		})
	}
}

func TestWalletNoteSummaryAcceptsEmptyBucketsWithExplicitNilExtrema(t *testing.T) {
	summary := WalletNoteSummary{WalletID: "hot", MinConfirmations: 0, MinNoteZat: 0, AsOfScannerHeight: 0, AsOfScannerHash: strings.Repeat("a", 64)}
	if !summary.ValidFor("hot", 0, 0, 1) {
		t.Fatal("empty summary rejected")
	}
}
