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
	result, err := planner.Plan(context.Background(), request, config.Wallet{WalletID: "hot", Account: 7}, "jregtest1change", []string{noteExcluded})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SelectedNoteIDs) != 1 || result.SelectedNoteIDs[0] != noteSelected || result.FeeZat != "200000" || result.ExpiryHeight != 140 {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecPlannerHelperProcess(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		return
	}
	args := os.Args[separator+1:]
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
	for flag, expected := range want {
		if actual := value(flag); actual != expected {
			t.Fatalf("%s=%q want %q; args=%v", flag, actual, expected, args)
		}
	}
	if len(args) == 0 || args[0] != "send-many" || !slices.Contains(args, "--json") {
		t.Fatalf("unexpected planner args=%v", args)
	}
	outputs, err := os.ReadFile(value("--outputs-file"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded []Output
	if json.Unmarshal(outputs, &decoded) != nil || len(decoded) != 1 || decoded[0].AmountZat != "100000" {
		t.Fatalf("outputs=%s", outputs)
	}
	raw, _ := json.Marshal(txPlan{
		Version: "v0", Kind: "withdrawal", WalletID: "hot", CoinType: 8135, Account: 7, Chain: "regtest", ExpiryHeight: 140,
		Outputs: decoded, ChangeAddress: "jregtest1change", FeeZat: "200000", Notes: []planNote{{NoteID: fmt.Sprintf("%064x:0", 2)}},
	})
	if err := os.WriteFile(value("--out"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(os.Stdout, `{"version":"v1","status":"ok","data":{}}`)
}
