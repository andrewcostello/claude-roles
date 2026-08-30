// GO-4-1 SCAFFOLD — the door a produced run-state passes through on its way to
// disk. Contract and one stub; nothing calls it yet. The measurements behind
// every constraint here are in features/dogfood-go/GO-4-1-contract.md.
//
// CONSTRAINTS:
//
//   - admitForWrite is the ONLY path from ApplyGateResults' output to
//     os.WriteFile. GO-4-3 wires mergeGates as LoadRunStateDocument ->
//     ApplyGateResults -> admitForWrite -> os.WriteFile. A second route to the
//     file, or a caller that writes on a non-nil error, is a finding.
//   - It decides by VerifyPreservation(original, produced, edits,
//     FidelityPathwise) and by nothing else. The level is fixed, not a
//     parameter: a parameter is a way to pass FidelityKeySet and certify a
//     document that re-attached every lost key with a null.
//   - The ONLY admitting result is (produced, nil, nil), with produced
//     byte-identical. Every other outcome returns nil bytes and a non-nil
//     error wrapping errUnverifiedWrite — so a caller that checks only err
//     never writes what the checker refused.
//   - A refusal keeps VerifyPreservation's two channels apart. A check that
//     RAN and found violations returns them, unchanged, as the second value,
//     with err wrapping errUnverifiedWrite only. A check that COULD NOT run
//     returns nil violations and err wrapping BOTH errUnverifiedWrite and
//     VerifyPreservation's own error. Neither may be reported as the other.
//   - On refusal the run-state on disk is untouched, because the write comes
//     after the decision. The refusal's exit code is the caller's rule
//     (finish; GO-3-3 owns it) and is not decided here.
//   - No type with methods is added here: an in-tree `Error()` method is
//     attributed to every seal that calls `.Error()` on an interface value
//     and would add findings to the reachability report this unit is judged
//     by. Keep the seam method-free.
//   - No exported surface changes. The stub and the sentinel are unexported.
package main

import (
	"errors"
	"fmt"
)

// errUnverifiedWrite is wrapped by every refusal, so a caller can distinguish
// "the checker said no" from an I/O error with errors.Is and never by prose.
var errUnverifiedWrite = errors.New("run-state write refused: preservation not verified")

// admitForWrite returns (produced, nil, nil), byte-identical, iff
// VerifyPreservation(original, produced, edits, FidelityPathwise) returns an
// empty violation list and a nil error. Otherwise it returns nil bytes, the
// violations the check found (nil when the check could not run), and an error
// wrapping errUnverifiedWrite.
//
// HOLE — GO-4-3 fills it and routes mergeGates through it.
func admitForWrite(original, produced []byte, edits []Edit) (admitted []byte, violations []Divergence, err error) { //nolint:unused // GO-4 scaffold: wired by GO-4-3
	return nil, nil, fmt.Errorf("%w: admitForWrite must decide by VerifyPreservation(original, produced, edits, FidelityPathwise) and admit only an empty violation list with a nil error: %w", errUnverifiedWrite, errNotImplemented)
}
