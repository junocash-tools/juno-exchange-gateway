package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/junocash-tools/juno-exchange-gateway/internal/storage"
)

const attemptColumns = `attempt_id,scoped_idempotency_key,request_digest,principal_name,wallet_id,approval_reference,request_json,state,change_address,plan_json,plan_digest,fee_zat,expiry_height,selected_note_ids_json,txid,raw_tx_hex,output_action_indices_json,change_action_index,error_code,error_message,error_retryable,created_at,updated_at`

func (s *Store) ClaimAttempt(ctx context.Context, candidate storage.TransactionAttempt) (storage.AttemptClaimResult, error) {
	if candidate.AttemptID == "" || candidate.ScopedIdempotencyKey == "" || candidate.RequestDigest == "" || candidate.PrincipalName == "" || candidate.WalletID == "" || candidate.ApprovalReference == "" || len(candidate.RequestJSON) == 0 {
		return storage.AttemptClaimResult{}, errors.New("transaction attempt claim is incomplete")
	}
	now := candidate.CreatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.AttemptClaimResult{}, fmt.Errorf("begin transaction attempt claim: %w", err)
	}
	defer tx.Rollback()
	existing, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM transaction_attempts WHERE scoped_idempotency_key=?`, candidate.ScopedIdempotencyKey))
	if err == nil {
		if existing.RequestDigest != candidate.RequestDigest {
			return storage.AttemptClaimResult{State: storage.ClaimConflict, Attempt: existing}, nil
		}
		return storage.AttemptClaimResult{State: storage.ClaimReplay, Attempt: existing}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.AttemptClaimResult{}, err
	}
	stamp := now.Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO transaction_attempts(attempt_id,scoped_idempotency_key,request_digest,principal_name,wallet_id,approval_reference,request_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'planning',?,?)`,
		candidate.AttemptID, candidate.ScopedIdempotencyKey, candidate.RequestDigest, candidate.PrincipalName, candidate.WalletID, candidate.ApprovalReference, append([]byte(nil), candidate.RequestJSON...), stamp, stamp)
	if err != nil {
		return storage.AttemptClaimResult{}, fmt.Errorf("insert transaction attempt: %w", err)
	}
	if err := insertAttemptEvent(ctx, tx, candidate.AttemptID, "planning", nil, now); err != nil {
		return storage.AttemptClaimResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return storage.AttemptClaimResult{}, fmt.Errorf("commit transaction attempt claim: %w", err)
	}
	candidate.State = "planning"
	candidate.CreatedAt = now
	candidate.UpdatedAt = now
	return storage.AttemptClaimResult{State: storage.ClaimAcquired, Attempt: candidate}, nil
}

func (s *Store) Attempt(ctx context.Context, attemptID string) (storage.TransactionAttempt, bool, error) {
	attempt, err := scanAttempt(s.db.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM transaction_attempts WHERE attempt_id=?`, attemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return storage.TransactionAttempt{}, false, nil
	}
	if err != nil {
		return storage.TransactionAttempt{}, false, err
	}
	return attempt, true, nil
}

func (s *Store) RecoverableAttempts(ctx context.Context, limit int) ([]storage.TransactionAttempt, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("recoverable attempt limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+attemptColumns+` FROM transaction_attempts WHERE state IN ('planning','reserved','signing','signing_unknown','signed','broadcast','mined','expired_pending_reconciliation','orphaned') ORDER BY created_at,attempt_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recoverable transaction attempts: %w", err)
	}
	defer rows.Close()
	out := make([]storage.TransactionAttempt, 0)
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recoverable transaction attempts: %w", err)
	}
	return out, nil
}

func (s *Store) SetAttemptChangeAddress(ctx context.Context, attemptID, address string, now time.Time) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return errors.New("change address is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var state string
	var existing sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT state,change_address FROM transaction_attempts WHERE attempt_id=?`, attemptID).Scan(&state, &existing); err != nil {
		return fmt.Errorf("read transaction attempt change address: %w", err)
	}
	if existing.Valid && existing.String != "" {
		if existing.String == address {
			return nil
		}
		return storage.ErrAttemptStateConflict
	}
	if state != "planning" {
		return storage.ErrAttemptStateConflict
	}
	res, err := s.db.ExecContext(ctx, `UPDATE transaction_attempts SET change_address=?,updated_at=? WHERE attempt_id=? AND state='planning' AND change_address IS NULL`, address, now.UTC().Format(time.RFC3339Nano), attemptID)
	if err != nil {
		return fmt.Errorf("store transaction attempt change address: %w", err)
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return storage.ErrAttemptStateConflict
	}
	return nil
}

func (s *Store) ActiveNoteIDs(ctx context.Context, network, walletID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT note_id FROM active_note_reservations WHERE network=? AND wallet_id=? ORDER BY note_id`, network, walletID)
	if err != nil {
		return nil, fmt.Errorf("list active note reservations: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var noteID string
		if err := rows.Scan(&noteID); err != nil {
			return nil, fmt.Errorf("read active note reservation: %w", err)
		}
		out = append(out, noteID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list active note reservations: %w", err)
	}
	return out, nil
}

func (s *Store) ReserveAttemptPlan(ctx context.Context, attemptID, network string, planJSON []byte, planDigest, feeZat string, expiryHeight int64, noteIDs []string, now time.Time) error {
	if len(planJSON) == 0 || planDigest == "" || feeZat == "" || expiryHeight < 0 || len(noteIDs) == 0 {
		return errors.New("transaction plan reservation is incomplete")
	}
	noteIDs = append([]string(nil), noteIDs...)
	sort.Strings(noteIDs)
	for i := range noteIDs {
		if noteIDs[i] == "" || (i > 0 && noteIDs[i] == noteIDs[i-1]) {
			return errors.New("transaction plan note IDs must be non-empty and unique")
		}
	}
	noteJSON, err := json.Marshal(noteIDs)
	if err != nil {
		return errors.New("encode transaction plan note IDs")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction plan reservation: %w", err)
	}
	defer tx.Rollback()
	var state string
	var existingDigest sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT state,plan_digest FROM transaction_attempts WHERE attempt_id=?`, attemptID).Scan(&state, &existingDigest); err != nil {
		return fmt.Errorf("read transaction attempt before reservation: %w", err)
	}
	if state == "reserved" && existingDigest.Valid && existingDigest.String == planDigest {
		return nil
	}
	if state != "planning" {
		return storage.ErrAttemptStateConflict
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	for _, noteID := range noteIDs {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO active_note_reservations(network,wallet_id,note_id,attempt_id,plan_digest,created_at) SELECT ?,wallet_id,?,?,?,? FROM transaction_attempts WHERE attempt_id=?`, network, noteID, attemptID, planDigest, stamp, attemptID)
		if err != nil {
			return fmt.Errorf("reserve transaction plan note: %w", err)
		}
		if changed, _ := res.RowsAffected(); changed != 1 {
			return storage.ErrNoteReservation
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE transaction_attempts SET state='reserved',plan_json=?,plan_digest=?,fee_zat=?,expiry_height=?,selected_note_ids_json=?,error_code=NULL,error_message=NULL,error_retryable=0,updated_at=? WHERE attempt_id=? AND state='planning'`, append([]byte(nil), planJSON...), planDigest, feeZat, expiryHeight, noteJSON, stamp, attemptID)
	if err != nil {
		return fmt.Errorf("store reserved transaction plan: %w", err)
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return storage.ErrAttemptStateConflict
	}
	if err := insertAttemptEvent(ctx, tx, attemptID, "reserved", nil, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction plan reservation: %w", err)
	}
	return nil
}

func (s *Store) BeginAttemptSigning(ctx context.Context, attemptID string, now time.Time) (storage.TransactionAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.TransactionAttempt{}, fmt.Errorf("begin signing transition: %w", err)
	}
	defer tx.Rollback()
	attempt, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM transaction_attempts WHERE attempt_id=?`, attemptID))
	if err != nil {
		return storage.TransactionAttempt{}, err
	}
	switch attempt.State {
	case "signing":
		return attempt, nil
	case "reserved", "signing_unknown":
	default:
		return storage.TransactionAttempt{}, storage.ErrAttemptStateConflict
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `UPDATE transaction_attempts SET state='signing',error_code=NULL,error_message=NULL,error_retryable=0,updated_at=? WHERE attempt_id=? AND state=?`, stamp, attemptID, attempt.State)
	if err != nil {
		return storage.TransactionAttempt{}, fmt.Errorf("transition transaction attempt to signing: %w", err)
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return storage.TransactionAttempt{}, storage.ErrAttemptStateConflict
	}
	if err := insertAttemptEvent(ctx, tx, attemptID, "signing", nil, now); err != nil {
		return storage.TransactionAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return storage.TransactionAttempt{}, fmt.Errorf("commit signing transition: %w", err)
	}
	attempt.State = "signing"
	attempt.UpdatedAt = now.UTC()
	return attempt, nil
}

func (s *Store) CompleteAttemptSigning(ctx context.Context, attemptID, txid, rawTxHex, feeZat string, outputIndices []uint32, changeIndex *uint32, now time.Time) error {
	indicesJSON, err := json.Marshal(outputIndices)
	if err != nil {
		return errors.New("encode transaction output action indices")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin signing completion: %w", err)
	}
	defer tx.Rollback()
	var state string
	var existingTxID, existingRaw sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT state,txid,raw_tx_hex FROM transaction_attempts WHERE attempt_id=?`, attemptID).Scan(&state, &existingTxID, &existingRaw); err != nil {
		return fmt.Errorf("read transaction attempt before signing completion: %w", err)
	}
	if state == "signed" && existingTxID.String == txid && existingRaw.String == rawTxHex {
		return nil
	}
	if state != "signing" && state != "signing_unknown" {
		return storage.ErrAttemptStateConflict
	}
	var change any
	if changeIndex != nil {
		change = int64(*changeIndex)
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `UPDATE transaction_attempts SET state='signed',txid=?,raw_tx_hex=?,fee_zat=?,output_action_indices_json=?,change_action_index=?,error_code=NULL,error_message=NULL,error_retryable=0,updated_at=? WHERE attempt_id=? AND state IN ('signing','signing_unknown')`, txid, rawTxHex, feeZat, indicesJSON, change, stamp, attemptID)
	if err != nil {
		return fmt.Errorf("store signed transaction result: %w", err)
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return storage.ErrAttemptStateConflict
	}
	if err := insertAttemptEvent(ctx, tx, attemptID, "signed", nil, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit signing completion: %w", err)
	}
	return nil
}

func (s *Store) MarkAttemptState(ctx context.Context, attemptID, state, code, message string, retryable, releaseReservations bool, now time.Time) error {
	if state == "" {
		return errors.New("transaction attempt state is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction attempt transition: %w", err)
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM transaction_attempts WHERE attempt_id=?`, attemptID).Scan(&current); err != nil {
		return fmt.Errorf("read transaction attempt state: %w", err)
	}
	if !allowedAttemptTransition(current, state) {
		return storage.ErrAttemptStateConflict
	}
	if releaseReservations && !safeReservationReleaseTransition(current, state) {
		return storage.ErrAttemptStateConflict
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	if releaseReservations {
		if _, err := tx.ExecContext(ctx, `DELETE FROM active_note_reservations WHERE attempt_id=?`, attemptID); err != nil {
			return fmt.Errorf("release transaction attempt notes: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE transaction_attempts SET state=?,error_code=?,error_message=?,error_retryable=?,updated_at=? WHERE attempt_id=?`, state, nullableString(code), nullableString(message), retryable, stamp, attemptID); err != nil {
		return fmt.Errorf("update transaction attempt state: %w", err)
	}
	detail, _ := json.Marshal(map[string]any{"error_code": code, "retryable": retryable})
	if err := insertAttemptEvent(ctx, tx, attemptID, state, detail, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction attempt transition: %w", err)
	}
	return nil
}

func (s *Store) CancelAttempt(ctx context.Context, attemptID string, now time.Time) (storage.TransactionAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.TransactionAttempt{}, fmt.Errorf("begin transaction attempt cancellation: %w", err)
	}
	defer tx.Rollback()
	attempt, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM transaction_attempts WHERE attempt_id=?`, attemptID))
	if err != nil {
		return storage.TransactionAttempt{}, err
	}
	if attempt.State == "cancelled" {
		return attempt, nil
	}
	if attempt.State != "planning" && attempt.State != "reserved" {
		return storage.TransactionAttempt{}, storage.ErrAttemptStateConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_note_reservations WHERE attempt_id=?`, attemptID); err != nil {
		return storage.TransactionAttempt{}, fmt.Errorf("release cancelled transaction attempt notes: %w", err)
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `UPDATE transaction_attempts SET state='cancelled',error_code=NULL,error_message=NULL,error_retryable=0,updated_at=? WHERE attempt_id=? AND state=?`, stamp, attemptID, attempt.State)
	if err != nil {
		return storage.TransactionAttempt{}, fmt.Errorf("cancel transaction attempt: %w", err)
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return storage.TransactionAttempt{}, storage.ErrAttemptStateConflict
	}
	if err := insertAttemptEvent(ctx, tx, attemptID, "cancelled", nil, now); err != nil {
		return storage.TransactionAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return storage.TransactionAttempt{}, fmt.Errorf("commit transaction attempt cancellation: %w", err)
	}
	attempt.State = "cancelled"
	attempt.UpdatedAt = now.UTC()
	return attempt, nil
}

type attemptRow interface{ Scan(...any) error }

func scanAttempt(row attemptRow) (storage.TransactionAttempt, error) {
	var out storage.TransactionAttempt
	var changeAddress, planDigest, feeZat, txid, rawTx, errorCode, errorMessage sql.NullString
	var planJSON, noteIDsJSON, indicesJSON []byte
	var expiry, changeIndex sql.NullInt64
	var retryable int
	var created, updated string
	if err := row.Scan(&out.AttemptID, &out.ScopedIdempotencyKey, &out.RequestDigest, &out.PrincipalName, &out.WalletID, &out.ApprovalReference, &out.RequestJSON, &out.State,
		&changeAddress, &planJSON, &planDigest, &feeZat, &expiry, &noteIDsJSON, &txid, &rawTx, &indicesJSON, &changeIndex, &errorCode, &errorMessage, &retryable, &created, &updated); err != nil {
		return storage.TransactionAttempt{}, err
	}
	out.RequestJSON = append([]byte(nil), out.RequestJSON...)
	out.ChangeAddress = changeAddress.String
	out.PlanJSON = append([]byte(nil), planJSON...)
	out.PlanDigest = planDigest.String
	out.FeeZat = feeZat.String
	if expiry.Valid {
		out.ExpiryHeight = expiry.Int64
	}
	if len(noteIDsJSON) > 0 && json.Unmarshal(noteIDsJSON, &out.SelectedNoteIDs) != nil {
		return storage.TransactionAttempt{}, errors.New("stored transaction attempt note IDs are invalid")
	}
	out.TxID = txid.String
	out.RawTxHex = rawTx.String
	if len(indicesJSON) > 0 && json.Unmarshal(indicesJSON, &out.OrchardOutputActionIndices) != nil {
		return storage.TransactionAttempt{}, errors.New("stored transaction output action indices are invalid")
	}
	if changeIndex.Valid {
		if changeIndex.Int64 < 0 || changeIndex.Int64 > int64(^uint32(0)) {
			return storage.TransactionAttempt{}, errors.New("stored transaction change action index is invalid")
		}
		value := uint32(changeIndex.Int64)
		out.OrchardChangeActionIndex = &value
	}
	out.ErrorCode = errorCode.String
	out.ErrorMessage = errorMessage.String
	out.ErrorRetryable = retryable != 0
	var err error
	out.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return storage.TransactionAttempt{}, errors.New("stored transaction attempt creation time is invalid")
	}
	out.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return storage.TransactionAttempt{}, errors.New("stored transaction attempt update time is invalid")
	}
	return out, nil
}

func insertAttemptEvent(ctx context.Context, tx *sql.Tx, attemptID, state string, detail []byte, now time.Time) error {
	if len(detail) == 0 {
		detail = nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transaction_attempt_events(attempt_id,state,detail_json,created_at) VALUES(?,?,?,?)`, attemptID, state, detail, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record transaction attempt event: %w", err)
	}
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func terminalAttemptState(state string) bool {
	switch state {
	case "cancelled", "failed_unsigned", "released", "final":
		return true
	default:
		return false
	}
}

func allowedAttemptTransition(current, next string) bool {
	if current == next {
		return true
	}
	if terminalAttemptState(current) {
		return false
	}
	switch current {
	case "planning":
		return next == "reserved" || next == "failed_unsigned"
	case "reserved":
		return next == "signing" || next == "failed_unsigned"
	case "signing":
		return next == "reserved" || next == "signing_unknown" || next == "signed" || next == "failed_unsigned"
	case "signing_unknown":
		return next == "signing" || next == "signed"
	case "signed":
		return next == "broadcast" || next == "mined" || next == "orphaned" || next == "expired_pending_reconciliation" || next == "final" || next == "released"
	case "broadcast":
		return next == "mined" || next == "orphaned" || next == "expired_pending_reconciliation" || next == "final" || next == "released"
	case "mined":
		return next == "broadcast" || next == "orphaned" || next == "expired_pending_reconciliation" || next == "final" || next == "released"
	case "orphaned":
		return next == "broadcast" || next == "mined" || next == "expired_pending_reconciliation" || next == "final" || next == "released"
	case "expired_pending_reconciliation":
		return next == "broadcast" || next == "mined" || next == "orphaned" || next == "final" || next == "released"
	default:
		return false
	}
}

func safeReservationReleaseTransition(current, next string) bool {
	switch next {
	case "failed_unsigned":
		return current == "planning" || current == "reserved" || current == "signing"
	case "final", "released":
		return current == "signed" || current == "broadcast" || current == "mined" || current == "orphaned" || current == "expired_pending_reconciliation" || current == next
	default:
		return false
	}
}
