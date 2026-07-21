package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
)

type principal struct {
	name    string
	scopes  map[string]struct{}
	wallets map[string]struct{}
	all     bool
}

func (p principal) hasScope(scope string) bool {
	if p.all {
		return true
	}
	_, ok := p.scopes[scope]
	if !ok {
		_, ok = p.scopes["admin"]
	}
	return ok
}

func (p principal) hasWallet(walletID string) bool {
	if p.all {
		return true
	}
	_, all := p.wallets["*"]
	_, exact := p.wallets[walletID]
	return all || exact
}

type authenticator struct {
	credentials    []config.Credential
	allowAnonymous bool
}

func (a authenticator) authenticate(r *http.Request) (principal, bool) {
	if a.allowAnonymous && len(a.credentials) == 0 {
		return principal{name: "regtest-anonymous", all: true}, true
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) < 8 || !strings.EqualFold(header[:7], "Bearer ") {
		return principal{}, false
	}
	token := strings.TrimSpace(header[7:])
	if token == "" {
		return principal{}, false
	}
	hash := sha256.Sum256([]byte(token))
	for _, credential := range a.credentials {
		if subtle.ConstantTimeCompare(hash[:], credential.TokenHash[:]) != 1 {
			continue
		}
		p := principal{name: credential.Name, scopes: map[string]struct{}{}, wallets: map[string]struct{}{}}
		for _, v := range credential.Scopes {
			p.scopes[v] = struct{}{}
		}
		for _, v := range credential.Wallets {
			p.wallets[v] = struct{}{}
		}
		return p, true
	}
	return principal{}, false
}

type principalKey struct{}

func withPrincipal(ctx context.Context, p principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}
func principalFrom(ctx context.Context) principal {
	p, _ := ctx.Value(principalKey{}).(principal)
	return p
}
