package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
)

type cursorCodec struct{ key []byte }
type cursorPayload struct {
	Version  int    `json:"v"`
	WalletID string `json:"w"`
	Position int64  `json:"p"`
}

func (c cursorCodec) encode(walletID string, position int64) string {
	payload, _ := json.Marshal(cursorPayload{Version: 1, WalletID: walletID, Position: position})
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c cursorCodec) decode(raw, walletID string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	var payloadPart, signaturePart string
	for i := 0; i < len(raw); i++ {
		if raw[i] == '.' {
			payloadPart, signaturePart = raw[:i], raw[i+1:]
			break
		}
	}
	if payloadPart == "" || signaturePart == "" {
		return 0, errors.New("invalid cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return 0, errors.New("invalid cursor")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil {
		return 0, errors.New("invalid cursor")
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return 0, errors.New("invalid cursor")
	}
	var decoded cursorPayload
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.Version != 1 || decoded.WalletID != walletID || decoded.Position < 0 {
		return 0, errors.New("invalid cursor")
	}
	return decoded.Position, nil
}
