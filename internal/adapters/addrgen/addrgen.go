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

const outputVersion = "v1"

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
		Version string `json:"version"`
		Status  string `json:"status"`
		Address string `json:"address"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("address deriver returned invalid JSON")
	}
	if out.Version != outputVersion || out.Status != "ok" || strings.TrimSpace(out.Address) == "" {
		return "", errors.New("address derivation failed")
	}
	return strings.TrimSpace(out.Address), nil
}

func (d Deriver) DeriveBatch(ctx context.Context, ufvk string, start, count uint32) ([]string, error) {
	if count == 0 || count > 100000 || uint64(start)+uint64(count) > uint64(^uint32(0))+1 {
		return nil, errors.New("address derivation batch range is invalid")
	}
	cmd := exec.CommandContext(ctx, d.Path, "batch", "--ufvk-env", "JUNO_GATEWAY_DERIVE_UFVK", "--start", strconv.FormatUint(uint64(start), 10), "--count", strconv.FormatUint(uint64(count), 10), "--json")
	cmd.Env = append(os.Environ(), "JUNO_GATEWAY_DERIVE_UFVK="+ufvk)
	raw, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, errors.New("address batch derivation rejected UFVK or range")
		}
		return nil, fmt.Errorf("start address batch deriver: %w", err)
	}
	var out struct {
		Version   string   `json:"version"`
		Status    string   `json:"status"`
		Start     uint32   `json:"start"`
		Count     uint32   `json:"count"`
		Addresses []string `json:"addresses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("address batch deriver returned invalid JSON")
	}
	if out.Version != outputVersion || out.Status != "ok" || out.Start != start || out.Count != count || len(out.Addresses) != int(count) {
		return nil, errors.New("address batch derivation failed")
	}
	for i := range out.Addresses {
		out.Addresses[i] = strings.TrimSpace(out.Addresses[i])
		if out.Addresses[i] == "" {
			return nil, errors.New("address batch deriver returned an empty address")
		}
	}
	return out.Addresses, nil
}
