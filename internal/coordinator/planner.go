package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
)

const plannerOutputLimit = 64 << 10

type Planner interface {
	Plan(context.Context, CreateRequest, config.Wallet, string, []string) (planResult, error)
}

type ExecPlanner struct {
	cfg            config.Config
	commandContext func(context.Context, string, ...string) *exec.Cmd
}

func NewExecPlanner(cfg config.Config) *ExecPlanner {
	return &ExecPlanner{cfg: cfg, commandContext: exec.CommandContext}
}

func (p *ExecPlanner) Plan(ctx context.Context, request CreateRequest, wallet config.Wallet, changeAddress string, excludedNoteIDs []string) (planResult, error) {
	if err := os.MkdirAll(p.cfg.CoordinatorWorkDir, 0o700); err != nil {
		return planResult{}, wrapOpError("planner_unavailable", "planner work directory is unavailable", true, false, err)
	}
	if err := os.Chmod(p.cfg.CoordinatorWorkDir, 0o700); err != nil {
		return planResult{}, wrapOpError("planner_unavailable", "planner work directory permissions could not be secured", false, false, err)
	}
	dir, err := os.MkdirTemp(p.cfg.CoordinatorWorkDir, "attempt-")
	if err != nil {
		return planResult{}, wrapOpError("planner_unavailable", "planner workspace could not be created", true, false, err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return planResult{}, wrapOpError("planner_unavailable", "planner workspace permissions could not be secured", false, false, err)
	}

	outputsPath := filepath.Join(dir, "outputs.json")
	planPath := filepath.Join(dir, "txplan.json")
	outputs, err := json.Marshal(request.Outputs)
	if err != nil {
		return planResult{}, opError("invalid_request", "outputs could not be encoded", false)
	}
	if err := os.WriteFile(outputsPath, outputs, 0o600); err != nil {
		return planResult{}, wrapOpError("planner_unavailable", "planner outputs could not be written", true, false, err)
	}

	args := []string{
		"send-many",
		"--wallet-id", wallet.WalletID,
		"--coin-type", "0",
		"--account", strconv.FormatUint(uint64(wallet.Account), 10),
		"--outputs-file", outputsPath,
		"--change-address", changeAddress,
		"--fee-multiplier", strconv.FormatInt(p.cfg.CoordinatorFeeMultiplier, 10),
		"--fee-add-zat", strconv.FormatInt(p.cfg.CoordinatorFeeAddZat, 10),
		"--min-change-zat", strconv.FormatInt(p.cfg.CoordinatorMinChangeZat, 10),
		"--min-note-zat", strconv.FormatInt(p.cfg.CoordinatorMinNoteZat, 10),
		"--minconf", strconv.FormatInt(p.cfg.DefaultConfirmations, 10),
		"--expiry-offset", strconv.FormatInt(p.cfg.CoordinatorExpiryOffset, 10),
		"--out", planPath,
		"--json",
	}
	for _, noteID := range excludedNoteIDs {
		args = append(args, "--exclude-note-id", noteID)
	}

	commandContext := p.commandContext
	if commandContext == nil {
		commandContext = exec.CommandContext
	}
	cmd := commandContext(ctx, p.cfg.CoordinatorTxbuildPath, args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"LD_LIBRARY_PATH=" + os.Getenv("LD_LIBRARY_PATH"),
		"JUNO_RPC_URL=" + p.cfg.NodeRPCURL,
		"JUNO_RPC_USER=" + p.cfg.NodeRPCUser,
		"JUNO_RPC_PASS=" + p.cfg.NodeRPCPassword,
		"JUNO_SCAN_URL=" + p.cfg.ScannerURL,
		"JUNO_SCAN_BEARER_TOKEN=" + p.cfg.ScannerToken,
	}
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = plannerOutputLimit, plannerOutputLimit
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return planResult{}, opError("planner_timeout", "transaction planning timed out", true)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return planResult{}, opError("planner_cancelled", "transaction planning was cancelled", true)
		}
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(stdout.Bytes(), &envelope) == nil && envelope.Error.Code != "" {
			return planResult{}, plannerCodedError(envelope.Error.Code, envelope.Error.Message)
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = "transaction planner failed"
		}
		return planResult{}, wrapOpError("planner_unavailable", message, true, false, err)
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return planResult{}, wrapOpError("planner_invalid_response", "planner did not durably return a TxPlan", true, false, err)
	}
	if len(raw) > 3<<20 {
		return planResult{}, opError("planner_invalid_response", "planner TxPlan exceeds the 3 MiB limit", false)
	}
	result, err := validatePlan(raw, request, wallet, p.cfg.Network, changeAddress)
	if err != nil {
		return planResult{}, wrapOpError("planner_invalid_response", err.Error(), false, false, err)
	}
	return result, nil
}

func plannerCodedError(code, message string) error {
	if strings.TrimSpace(message) == "" {
		message = "transaction planning failed"
	}
	switch code {
	case "insufficient_balance", "too_many_inputs", "no_liquidity_in_hot", "invalid_request":
		return opError(code, message, false)
	case "not_found":
		return opError(code, message, true)
	default:
		return opError("planner_unavailable", message, true)
	}
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.Buffer.Write(value)
	}
	return original, nil
}

func (b *cappedBuffer) String() string {
	return fmt.Sprintf("%s", b.Bytes())
}
