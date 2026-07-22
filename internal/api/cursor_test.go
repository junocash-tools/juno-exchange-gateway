package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCursorV2BindsWalletEpochAndPosition(t *testing.T) {
	codec := cursorCodec{key: []byte("01234567890123456789012345678901")}
	epoch := strings.Repeat("a", 64)
	filter := strings.Repeat("c", 64)
	raw := codec.encode("hot", epoch, filter, 42)
	position, err := codec.decode(raw, "hot", epoch, filter)
	if err != nil || position != 42 {
		t.Fatalf("position=%d err=%v", position, err)
	}
	if _, err := codec.decode(raw, "cold", epoch, filter); err == nil || errors.Is(err, errCursorResetRequired) {
		t.Fatalf("wallet mismatch err=%v", err)
	}
	if _, err := codec.decode(raw, "hot", strings.Repeat("b", 64), filter); !errors.Is(err, errCursorResetRequired) {
		t.Fatalf("epoch mismatch err=%v", err)
	}
	if _, err := codec.decode(raw, "hot", epoch, strings.Repeat("d", 64)); !errors.Is(err, errCursorFilterMismatch) {
		t.Fatalf("filter mismatch err=%v", err)
	}
}

func TestLegacySignedCursorRequiresExplicitReset(t *testing.T) {
	codec := cursorCodec{key: []byte("01234567890123456789012345678901")}
	payload, err := json.Marshal(cursorPayload{Version: 1, WalletID: "hot", Position: 7})
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(payload)
	raw := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := codec.decode(raw, "hot", strings.Repeat("a", 64), strings.Repeat("b", 64)); !errors.Is(err, errCursorResetRequired) {
		t.Fatalf("err=%v", err)
	}
}

func TestCursorFromDifferentSigningKeyIsInvalid(t *testing.T) {
	oldCodec := cursorCodec{key: []byte("01234567890123456789012345678901")}
	newCodec := cursorCodec{key: []byte("abcdefghijklmnopqrstuvwxyzABCDEF")}
	epoch := strings.Repeat("a", 64)
	filter := strings.Repeat("b", 64)
	raw := oldCodec.encode("hot", epoch, filter, 7)
	if _, err := newCodec.decode(raw, "hot", epoch, filter); err == nil || errors.Is(err, errCursorResetRequired) {
		t.Fatalf("err=%v", err)
	}
}
