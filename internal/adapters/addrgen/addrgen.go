package addrgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type Deriver struct{ Path string }

func (d Deriver) Derive(ctx context.Context, ufvk string, index uint32) (string, error) {
	cmd := exec.CommandContext(ctx, d.Path, "derive", "--ufvk-env", "JUNO_GATEWAY_DERIVE_UFVK", "--index", strconv.FormatUint(uint64(index), 10), "--json")
	cmd.Env = append(os.Environ(), "JUNO_GATEWAY_DERIVE_UFVK="+ufvk)
	raw, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", errors.New("address derivation rejected UFVK or index")
		}
		return "", fmt.Errorf("start address deriver: %w", err)
	}
	var out struct {
		Status  string `json:"status"`
		Address string `json:"address"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("address deriver returned invalid JSON")
	}
	if out.Status != "ok" || strings.TrimSpace(out.Address) == "" {
		return "", errors.New("address derivation failed")
	}
	return strings.TrimSpace(out.Address), nil
}
