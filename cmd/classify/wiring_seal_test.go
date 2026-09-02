package main

// Seals for the (contract x -out x -json) -> artifact set + exit code mapping
// — the mapping GO-1-1's scaffold names as RunWiring's subject (wiring.go:230)
// and that GO-1-3 has not yet wired main() to drive: main() (main.go:213)
// still calls run(parseFlags()) directly, and RunWiring itself is a stub that
// returns ErrWiringNotImplemented (wiring.go:277-280). A row that drove
// RunWiring in process would be red on the stub regardless of the mutation
// under test and would observe nothing about the binary that ships. So these
// rows build the CURRENT TREE (liveClassify, repair_seal_test.go) and drive it
// exactly as an operator would, through main()'s real spine.
//
// THE MEASURED MUTATION: emit()'s ContractV2 arm (main.go:531) rewritten from
// `return EmitV2(os.Stdout, cls)` to `return EmitV1(os.Stdout, cls)`. At
// 0cfdb57 no seal in this package reddened for it —
// TestSeal_EmitV2_RequiresAnInstalledDigestSource and its neighbours in
// contract_seal_test.go call EmitV2 directly as a library function, so none of
// them observe which of EmitV1/EmitV2 run()'s own emit() actually chose.
// TestSeal_Mapping_ContractOutJSON_ArtifactSetAndExitCode's contract-2 rows
// close that gap: they drive the live binary with -contract-version 2 -json
// and assert the V2 WRAPPER shape on stdout — response_version,
// computed_config_sha256, computed_diff_sha256, and a nested classification
// with contract_version 2 — which the mutation would silently replace with the
// bare V1 Classification.
import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeal_Mapping_ContractOutJSON_ArtifactSetAndExitCode drives the mapping
// across every (contract, -out, -json) combination the producer accepts, and
// asserts both halves of the contract: the exit code and the STDOUT shape, and
// the artifact set persist() (main.go:441) leaves on disk — the run-state file
// and the v2 sidecar. -out absent is included precisely to show the sidecar
// question does not arise without it: each row runs with its scratch dir as
// the process's own working directory (-config given as an absolute path, so
// nothing else needs cwd), then asserts os.ReadDir on that directory
// POSITIVELY — for out=false rows the only entry may be wallet.diff itself,
// which catches persist writing a run-state or sidecar to a relative default
// path when -out is empty, a defect an inside-the-if assertion never observes
// because it never runs for these rows at all.
func TestSeal_Mapping_ContractOutJSON_ArtifactSetAndExitCode(t *testing.T) {
	bin := liveClassify(t)
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	absConfigPath := filepath.Join(pkgDir, exampleConfigPath)

	type row struct {
		name     string
		contract string // "" omits -contract-version, exercising the flag default
		asJSON   bool
		withOut  bool
	}
	var rows []row
	for _, contract := range []string{"", "1", "2"} {
		contractName := contract
		if contractName == "" {
			contractName = "default"
		}
		for _, asJSON := range []bool{false, true} {
			for _, withOut := range []bool{false, true} {
				rows = append(rows, row{
					name:     "contract=" + contractName + "/json=" + boolWord(asJSON) + "/out=" + boolWord(withOut),
					contract: contract,
					asJSON:   asJSON,
					withOut:  withOut,
				})
			}
		}
	}

	for _, r := range rows {
		r := r
		t.Run(r.name, func(t *testing.T) {
			dir := t.TempDir()
			diffPath := filepath.Join(dir, "wallet.diff")
			if err := os.WriteFile(diffPath, []byte(diffFor(walletPath)), 0o600); err != nil {
				t.Fatal(err)
			}

			args := []string{"-no-git", "-config", absConfigPath}
			if r.contract != "" {
				args = append(args, "-contract-version", r.contract)
			}
			if r.asJSON {
				args = append(args, "-json")
			}
			var outPath string
			if r.withOut {
				outPath = filepath.Join(dir, "run.json")
				args = append(args, "-out", outPath)
			}
			args = append(args, diffPath)

			run := runLive(t, bin, dir, nil, args...)
			if run.exit != 0 {
				t.Fatalf("exit %d, want 0\n%s", run.exit, run.all())
			}

			effectiveContract := r.contract
			if effectiveContract == "" {
				effectiveContract = "1" // defaultContractVersion, contract.go:70
			}

			switch {
			case !r.asJSON:
				if !strings.Contains(run.stdout, "=== CLASSIFICATION ===") {
					t.Errorf("!json: stdout is not the human report:\n%s", run.stdout)
				}
				if strings.Contains(run.stdout, "response_version") {
					t.Errorf("!json: stdout carries response_version, a machine-payload key that has no business in the report:\n%s", run.stdout)
				}

			case effectiveContract == "1":
				if hasKey(t, []byte(run.stdout), "response_version") {
					t.Errorf("contract 1 + json: stdout carries response_version — that key belongs to the V2 wrapper, and contract 1 must emit the bare legacy payload:\n%s", run.stdout)
				}
				if !hasKey(t, []byte(run.stdout), "risk") {
					t.Errorf("contract 1 + json: stdout has no top-level \"risk\" key — not the V1 Classification:\n%s", run.stdout)
				}

			case effectiveContract == "2":
				// THE MUTATION-CATCHING LEG. emit()'s ContractV2 arm calling EmitV1
				// instead of EmitV2 would produce the contract-1 shape asserted
				// above instead of this one, and every assertion below would redden.
				for _, key := range []string{"response_version", "computed_config_sha256", "computed_diff_sha256", "classification"} {
					if !hasKey(t, []byte(run.stdout), key) {
						t.Errorf("contract 2 + json: stdout is missing %q — not the V2 wrapper:\n%s", key, run.stdout)
					}
				}
				if hasKey(t, []byte(run.stdout), "risk") {
					t.Errorf("contract 2 + json: \"risk\" is a top-level key — it belongs inside the nested classification, not the wrapper:\n%s", run.stdout)
				}
				var wrapper ResponseWrapper
				if err := json.Unmarshal([]byte(run.stdout), &wrapper); err != nil {
					t.Fatalf("contract 2 + json: stdout does not unmarshal as ResponseWrapper: %v\n%s", err, run.stdout)
				}
				if len(wrapper.ComputedConfigSHA256) != 64 || len(wrapper.ComputedDiffSHA256) != 64 {
					t.Errorf("contract 2 + json: digests are not 64 hex characters (config %q, diff %q)", wrapper.ComputedConfigSHA256, wrapper.ComputedDiffSHA256)
				}
				var envelope map[string]any
				if err := json.Unmarshal(wrapper.Classification, &envelope); err != nil {
					t.Fatalf("contract 2 + json: wrapper.classification is not valid JSON: %v", err)
				}
				if cv, ok := envelope["contract_version"].(float64); !ok || int(cv) != 2 {
					t.Errorf("contract 2 + json: nested classification.contract_version = %v, want 2", envelope["contract_version"])
				}
				if _, ok := envelope["risk"]; !ok {
					t.Errorf("contract 2 + json: nested classification carries no \"risk\" key")
				}
			}

			if r.withOut {
				data, err := os.ReadFile(outPath)
				if err != nil {
					t.Fatalf("-out was given but no run-state was written at %s: %v", outPath, err)
				}
				if !strings.Contains(string(data), `"classification"`) {
					t.Errorf("run-state %s carries no classification:\n%s", outPath, data)
				}

				sidecar := V2SidecarPath(outPath)
				_, statErr := os.Stat(sidecar)
				sidecarExists := statErr == nil
				wantSidecar := effectiveContract == "2"
				if sidecarExists != wantSidecar {
					t.Errorf("contract %s, -out given: v2 sidecar %s exists=%v, want %v", effectiveContract, sidecar, sidecarExists, wantSidecar)
				}
			} else {
				// THE MUTATION-CATCHING LEG for out=false. The run's cwd is this
				// row's own scratch dir (runLive above), so a positive listing of
				// it is a positive claim about persist's artifact set: entries
				// beyond wallet.diff itself mean persist wrote a run-state and/or
				// v2 sidecar despite opts.out == "" — exactly what mutating
				// persist's `if opts.out == "" { return 0 }` guard (main.go:443)
				// to fall through to a relative default path produces, and what
				// the prior version of this row, asserting nothing at all inside
				// this block, let through.
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatalf("could not list the run's own working directory %s: %v", dir, err)
				}
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				if len(names) != 1 || names[0] != "wallet.diff" {
					t.Errorf("-out absent: run's working directory %s contains %v, want exactly [wallet.diff] — persist must write nothing without -out", dir, names)
				}
			}
		})
	}
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestSeal_Mapping_ContractVersion3_ExitsInvalidNamingAcceptedSet is the other
// end of the mapping: a contract this binary does not emit. ParseContractVersion
// (contract.go:84-93) is validated in run() BEFORE resolveConfigPath, before any
// input is read and before persist runs (main.go:322-331), so the row also
// checks that -out is given and STILL receives no run-state — the artifact set
// for an invalid contract is empty, not partially written.
func TestSeal_Mapping_ContractVersion3_ExitsInvalidNamingAcceptedSet(t *testing.T) {
	bin := liveClassify(t)
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	diffPath := filepath.Join(dir, "wallet.diff")
	if err := os.WriteFile(diffPath, []byte(diffFor(walletPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "run.json")

	run := runLive(t, bin, pkgDir, nil, "-no-git", "-config", exampleConfigPath,
		"-contract-version", "3", "-json", "-out", outPath, diffPath)

	if run.exit != exitInvalid {
		t.Fatalf("exit %d, want %d (INVALID_INPUT)\n%s", run.exit, exitInvalid, run.all())
	}
	if !strings.Contains(run.stdout, "accepted values are 1 and 2") {
		t.Errorf("the INVALID_INPUT report does not name the accepted contract set {1, 2}:\n%s", run.stdout)
	}
	if strings.Contains(run.stdout, "response_version") {
		t.Errorf("a machine payload leaked onto stdout for a rejected contract:\n%s", run.stdout)
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Errorf("-contract-version 3 must fail before persist ever runs; the run-state must not exist, but %s was written", outPath)
	}
	if _, err := os.Stat(V2SidecarPath(outPath)); err == nil {
		t.Errorf("-contract-version 3 must fail before persist ever runs; no v2 sidecar may exist at %s", V2SidecarPath(outPath))
	}
}
