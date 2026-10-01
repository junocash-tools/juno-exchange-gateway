package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
	"github.com/junocash-tools/juno-exchange-gateway/internal/domain"
)

func TestExecPlannerPassesPolicyAndActiveReservationExclusions(t *testing.T) {
	t.Setenv("LD_LIBRARY_PATH", "/test/juno-libs")
	noteExcluded := fmt.Sprintf("%064x:0", 1)
	noteSelected := fmt.Sprintf("%064x:0", 2)
	cfg := config.Config{
		Network: domain.Regtest, NodeRPCURL: "http://node:8232", NodeRPCUser: "rpc-user", NodeRPCPassword: "rpc-password",
		ScannerURL: "http://scanner:8080", ScannerToken: "scanner-token", DefaultConfirmations: 100,
		CoordinatorTxbuildPath: "/ignored/juno-txbuild", CoordinatorWorkDir: t.TempDir(), CoordinatorFeeMultiplier: 20,
		CoordinatorFeeAddZat: 3, CoordinatorMinChangeZat: 4, CoordinatorMinNoteZat: 5, CoordinatorExpiryOffset: 40,
	}
	planner := &ExecPlanner{cfg: cfg, commandContext: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=TestExecPlannerHelperProcess", "--"}, args...)
		return exec.CommandContext(ctx, os.Args[0], helperArgs...)
	}}
	request := CreateRequest{WalletID: "hot", ApprovalReference: "withdrawal-1", Outputs: []Output{{ToAddress: "jregtest1destination", AmountZat: "100000"}}}
	result, err := planner.Plan(context.Background(), request, config.Wallet{WalletID: "hot", Account: 7}, "jregtest1change", []string{noteExcluded}, planOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SelectedNoteIDs) != 1 || result.SelectedNoteIDs[0] != noteSelected || result.FeeZat != "200000" || result.ExpiryHeight != 140 || result.OutputCount != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecPlannerPassesSplitChangeForWithdrawals(t *testing.T) {
	t.Setenv("LD_LIBRARY_PATH", "/test/juno-libs")
	cfg := config.Config{
		Network: domain.Regtest, DefaultConfirmations: 100, CoordinatorTxbuildPath: "/ignored/juno-txbuild",
		CoordinatorWorkDir: t.TempDir(), CoordinatorFeeMultiplier: 20, CoordinatorFeeAddZat: 3, CoordinatorMinChangeZat: 4,
		CoordinatorMinNoteZat: 5, CoordinatorExpiryOffset: 40, CoordinatorSplitMinNoteZat: 7,
	}
	planner := &ExecPlanner{cfg: cfg, commandContext: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=TestExecPlannerHelperProcess", "expect-split=3", "--"}, args...)
		return exec.CommandContext(ctx, os.Args[0], helperArgs...)
	}}
	request := CreateRequest{WalletID: "hot", ApprovalReference: "withdrawal-1", Outputs: []Output{{ToAddress: "jregtest1destination", AmountZat: "100000"}}}
	result, err := planner.Plan(context.Background(), request, config.Wallet{WalletID: "hot", Account: 7}, "jregtest1change", []string{fmt.Sprintf("%064x:0", 1)}, planOptions{SplitChange: 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.OutputCount != 3 {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecPlannerUsesRebalanceForNoteSplits(t *testing.T) {
	t.Setenv("LD_LIBRARY_PATH", "/test/juno-libs")
	cfg := config.Config{
		Network: domain.Regtest, DefaultConfirmations: 100, CoordinatorTxbuildPath: "/ignored/juno-txbuild",
		CoordinatorWorkDir: t.TempDir(), CoordinatorFeeMultiplier: 20, CoordinatorFeeAddZat: 3, CoordinatorMinChangeZat: 4,
		CoordinatorMinNoteZat: 5, CoordinatorExpiryOffset: 40,
	}
	planner := &ExecPlanner{cfg: cfg, commandContext: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=TestExecPlannerHelperProcess", "expect-kind=rebalance", "--"}, args...)
		return exec.CommandContext(ctx, os.Args[0], helperArgs...)
	}}
	request := CreateRequest{WalletID: "hot", ApprovalReference: "split-1", Split: &NoteSplit{NoteCount: 4, NoteZat: "25000"}}
	// Split change must never be combined with an explicit split request.
	result, err := planner.Plan(context.Background(), request, config.Wallet{WalletID: "hot", Account: 7}, "jregtest1change", []string{fmt.Sprintf("%064x:0", 1)}, planOptions{SplitChange: 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.OutputCount != 4 {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecPlannerHelperProcess(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		return
	}
	args := os.Args[separator+1:]
	if got := os.Getenv("LD_LIBRARY_PATH"); got != "/test/juno-libs" {
		t.Fatalf("LD_LIBRARY_PATH=%q want %q", got, "/test/juno-libs")
	}
	value := func(flag string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == flag {
				return args[i+1]
			}
		}
		return ""
	}
	want := map[string]string{
		"--wallet-id": "hot", "--account": "7", "--minconf": "100", "--expiry-offset": "40", "--fee-multiplier": "20",
		"--fee-add-zat": "3", "--min-change-zat": "4", "--min-note-zat": "5", "--change-address": "jregtest1change",
		"--exclude-note-id": fmt.Sprintf("%064x:0", 1),
	}
	split := ""
	if slices.Contains(os.Args[:separator], "expect-split=3") {
		split = "3"
	}
	if split != "" {
		want["--split-change"] = split
		want["--split-change-min-zat"] = "7"
	} else if slices.Contains(args, "--split-change") || slices.Contains(args, "--split-change-min-zat") {
		t.Fatalf("unexpected split change args=%v", args)
	}
	for flag, expected := range want {
		if actual := value(flag); actual != expected {
			t.Fatalf("%s=%q want %q; args=%v", flag, actual, expected, args)
		}
	}
	kind, command := "withdrawal", "send-many"
	if slices.Contains(os.Args[:separator], "expect-kind=rebalance") {
		kind, command = "rebalance", "rebalance"
	}
	if len(args) == 0 || args[0] != command || !slices.Contains(args, "--json") {
		t.Fatalf("unexpected planner args=%v", args)
	}
	outputs, err := os.ReadFile(value("--outputs-file"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded []Output
	if json.Unmarshal(outputs, &decoded) != nil {
		t.Fatalf("outputs=%s", outputs)
	}
	if kind == "rebalance" {
		if len(decoded) != 4 {
			t.Fatalf("outputs=%s", outputs)
		}
		for _, output := range decoded {
			if output.ToAddress != "jregtest1change" || output.AmountZat != "25000" || output.MemoHex != "" {
				t.Fatalf("outputs=%s", outputs)
			}
		}
	} else if len(decoded) != 1 || decoded[0].AmountZat != "100000" {
		t.Fatalf("outputs=%s", outputs)
	}
	if split == "3" {
		decoded = append(decoded, Output{ToAddress: "jregtest1change", AmountZat: "30000"}, Output{ToAddress: "jregtest1change", AmountZat: "30000"})
	}
	raw, _ := json.Marshal(txPlan{
		Version: "v0", Kind: kind, WalletID: "hot", CoinType: 8135, Account: 7, Chain: "regtest", ExpiryHeight: 140,
		Outputs: decoded, ChangeAddress: "jregtest1change", FeeZat: "200000", Notes: []planNote{{NoteID: fmt.Sprintf("%064x:0", 2)}},
	})
	if err := os.WriteFile(value("--out"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(os.Stdout, `{"version":"v1","status":"ok","data":{}}`)
}
