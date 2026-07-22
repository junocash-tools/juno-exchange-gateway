package domain

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestValidNoteID(t *testing.T) {
	txid := strings.Repeat("a", 64)
	for _, noteID := range []string{txid + ":0", txid + ":1", txid + ":4294967295"} {
		if !ValidNoteID(noteID) {
			t.Fatalf("valid note ID rejected: %q", noteID)
		}
	}
	for _, noteID := range []string{
		"", txid, txid + ":", txid + ":00", txid + ":01", txid + ":-1", txid + ":1.0", txid + ":1:2",
		strings.Repeat("A", 64) + ":0", strings.Repeat("a", 63) + ":0", txid + ":4294967296",
	} {
		if ValidNoteID(noteID) {
			t.Fatalf("invalid note ID accepted: %q", noteID)
		}
	}
}

func TestWalletNoteStatusesValidForAllStates(t *testing.T) {
	noteIDs := []string{
		strings.Repeat("a", 64) + ":0",
		strings.Repeat("b", 64) + ":1",
		strings.Repeat("c", 64) + ":2",
		strings.Repeat("d", 64) + ":4294967295",
	}
	sourceHeight, value := int64(10), int64(25)
	expiry := int64(140)
	pendingAt := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	pendingTxID, spentTxID := strings.Repeat("e", 64), strings.Repeat("f", 64)
	spentHeight, confirmedHeight := int64(50), int64(100)
	valid := WalletNoteStatuses{
		WalletID: "hot", EventEpoch: strings.Repeat("1", 64), AsOfScannerHeight: 100, AsOfScannerHash: strings.Repeat("2", 64),
		Statuses: []NoteStatus{
			{NoteID: noteIDs[0], State: "unknown"},
			{NoteID: noteIDs[1], State: "unspent", SourceHeight: &sourceHeight, ValueZat: &value},
			{NoteID: noteIDs[2], State: "pending", SourceHeight: &sourceHeight, ValueZat: &value, PendingSpentTxID: &pendingTxID, PendingSpentAt: &pendingAt, PendingSpentExpiryHeight: &expiry},
			{NoteID: noteIDs[3], State: "spent", SourceHeight: &sourceHeight, ValueZat: &value, SpentTxID: &spentTxID, SpentHeight: &spentHeight, SpentConfirmedHeight: &confirmedHeight},
		},
	}
	if !valid.ValidFor("hot", noteIDs) {
		t.Fatal("valid note statuses rejected")
	}

	tests := map[string]func(*WalletNoteStatuses){
		"wrong wallet":             func(s *WalletNoteStatuses) { s.WalletID = "cold" },
		"bad epoch":                func(s *WalletNoteStatuses) { s.EventEpoch = strings.Repeat("A", 64) },
		"bad snapshot hash":        func(s *WalletNoteStatuses) { s.AsOfScannerHash = strings.Repeat("z", 64) },
		"negative snapshot height": func(s *WalletNoteStatuses) { s.AsOfScannerHeight = -1 },
		"missing status":           func(s *WalletNoteStatuses) { s.Statuses = s.Statuses[:3] },
		"reordered status": func(s *WalletNoteStatuses) {
			s.Statuses[0], s.Statuses[1] = s.Statuses[1], s.Statuses[0]
		},
		"unknown with source": func(s *WalletNoteStatuses) {
			s.Statuses[0].SourceHeight, s.Statuses[0].ValueZat = &sourceHeight, &value
		},
		"unspent missing value": func(s *WalletNoteStatuses) { s.Statuses[1].ValueZat = nil },
		"unspent with pending":  func(s *WalletNoteStatuses) { s.Statuses[1].PendingSpentTxID = &pendingTxID },
		"pending missing time":  func(s *WalletNoteStatuses) { s.Statuses[2].PendingSpentAt = nil },
		"pending bad txid": func(s *WalletNoteStatuses) {
			bad := strings.Repeat("E", 64)
			s.Statuses[2].PendingSpentTxID = &bad
		},
		"pending expired": func(s *WalletNoteStatuses) {
			expired := int64(99)
			s.Statuses[2].PendingSpentExpiryHeight = &expired
		},
		"pending with spent":   func(s *WalletNoteStatuses) { s.Statuses[2].SpentTxID = &spentTxID },
		"spent missing height": func(s *WalletNoteStatuses) { s.Statuses[3].SpentHeight = nil },
		"spent before source": func(s *WalletNoteStatuses) {
			before := int64(9)
			s.Statuses[3].SpentHeight = &before
		},
		"spent after snapshot": func(s *WalletNoteStatuses) {
			after := int64(101)
			s.Statuses[3].SpentHeight = &after
		},
		"confirmation before spend": func(s *WalletNoteStatuses) {
			before := int64(49)
			s.Statuses[3].SpentConfirmedHeight = &before
		},
		"unknown state": func(s *WalletNoteStatuses) { s.Statuses[1].State = "available" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneWalletNoteStatuses(valid)
			mutate(&candidate)
			if candidate.ValidFor("hot", noteIDs) {
				t.Fatal("invalid note statuses accepted")
			}
		})
	}
}

func TestWalletNoteStatusesRejectsDuplicateRequests(t *testing.T) {
	noteID := strings.Repeat("a", 64) + ":0"
	statuses := WalletNoteStatuses{
		WalletID: "hot", EventEpoch: strings.Repeat("b", 64), AsOfScannerHash: strings.Repeat("c", 64),
		Statuses: []NoteStatus{{NoteID: noteID, State: "unknown"}, {NoteID: noteID, State: "unknown"}},
	}
	if statuses.ValidFor("hot", []string{noteID, noteID}) {
		t.Fatal("duplicate request IDs accepted")
	}
}

func TestWalletNoteStatusesAcceptsMaximumBatch(t *testing.T) {
	noteIDs := make([]string, 200)
	statuses := make([]NoteStatus, 200)
	for i := range noteIDs {
		noteIDs[i] = strings.Repeat("a", 64) + ":" + fmt.Sprint(i)
		statuses[i] = NoteStatus{NoteID: noteIDs[i], State: "unknown"}
	}
	snapshot := WalletNoteStatuses{
		WalletID: "hot", EventEpoch: strings.Repeat("b", 64), AsOfScannerHash: strings.Repeat("c", 64), Statuses: statuses,
	}
	if !snapshot.ValidFor("hot", noteIDs) {
		t.Fatal("maximum batch rejected")
	}
}

func cloneWalletNoteStatuses(input WalletNoteStatuses) WalletNoteStatuses {
	input.Statuses = append([]NoteStatus(nil), input.Statuses...)
	return input
}
