// Capability probe (unit B1).
//
// SCAFFOLD — CONTRACTS ONLY. Bodies panic. The doc comments are the spec.
//
// The dispatcher's preflight must learn what this producer can do WITHOUT
// scraping help text. This file defines the contractual invocation: a
// subcommand with a stable JSON output and a stable exit code, both of which
// are part of the wire contract and neither of which may be changed for
// cosmetic reasons.
package main

import (
	"flag"
	"io"
	"os"
)

// ─── the honesty mechanism ───────────────────────────────────────────────────
//
// The probe must report truthfully BEFORE the units that implement the probed
// capabilities have landed. The mechanism is not discipline and not a TODO: a
// capability is reported present IFF its implementation object is installed in
// the registry below. There is no boolean literal anywhere in the probe, so
// the probe cannot disagree with reality, and no edit to this file is needed
// when a later unit lands — installing the implementation IS the flip.
//
// At B1 scaffold time every hook is nil, so the probe answers "absent" for
// everything, truthfully. B1's body installs contractFlagRegistrar and
// digestSource. B2's body installs framedStdinReader. Until it does, the probe
// reports framed_authoritative_stdin: false and exits
// exitCapabilityIncomplete, which correctly fails REQUIRED preflight.
//
// SEAL HOOK: the honesty property is directly assertable — for each capability,
// probeCapabilities() reports true if and only if the corresponding package
// variable is non-nil. A seal that sets a hook and observes no change in the
// probe output has found the exact drift this design forbids.

// ContractFlagRegistrar installs the -contract-version flag. Its presence in
// the registry is what makes the contract_version_flag capability true.
//
// Registering the flag through this interface rather than calling
// flag.String directly in parseFlags is deliberate: it means the capability is
// observable before flag.Parse runs, which the probe subcommand requires
// because it dispatches ahead of flag parsing (main.go:176-187).
type ContractFlagRegistrar interface {
	// RegisterContractVersionFlag registers flagContractVersion on fs and
	// returns the pointer the parsed value lands in. It must register under
	// exactly flagContractVersion and must default to
	// defaultContractVersion.String().
	RegisterContractVersionFlag(fs *flag.FlagSet) *string
}

// FramedStdinReader reads the framed -authoritative-stdin request: the policy
// bytes and the diff bytes, in one frame, from the authoritative channel.
//
// UNIT B2 OWNS THE FRAME FORMAT. B1 names only the tuple the design's probe
// covers — policy plus diff — and deliberately does not invent B2's framing,
// length prefixes, or error taxonomy. B2 may define whatever concrete type it
// needs, provided it satisfies this interface.
type FramedStdinReader interface {
	// ReadFramedRequest consumes the whole frame from r. Both byte slices are
	// the EXACT bytes consumed, unmodified, because they are the preimages the
	// digests are computed over.
	ReadFramedRequest(r *os.File) (policy []byte, diff []byte, err error)
}

// DigestSource yields the dual digest echo for the response wrapper.
//
// The digests are over the bytes THIS PROCESS CONSUMED. B1's body installs a
// source over the unframed path (the config file it read at main.go:417 and
// the diff it read at main.go:486). B2 replaces it with one over the framed
// preimages. Either way the digests are computed, never copied from an input
// field — the request carries no digests, so a copy-back is impossible by
// construction, and that impossibility must be preserved.
type DigestSource interface {
	// ConsumedDigests returns lowercase hex SHA-256 of the policy bytes and
	// the diff bytes. It errors rather than returning an empty string for a
	// channel it did not consume.
	ConsumedDigests() (configSHA256 string, diffSHA256 string, err error)
}

// The registry. Every hook is nil at scaffold time. A hook is installed by
// assignment from the owning unit's body, once, during package init or early
// in main. Nothing reads these except probeCapabilities and the code paths that
// use them; nothing may re-derive a capability by any other means.
var (
	// contractFlagRegistrar is installed by unit B1's body.
	contractFlagRegistrar ContractFlagRegistrar
	// digestSource is installed by unit B1's body (unframed) and replaced by
	// unit B2's body (framed).
	digestSource DigestSource
	// framedStdinReader is installed by unit B2's body. nil in B1.
	framedStdinReader FramedStdinReader
)

// ─── the probe's wire ────────────────────────────────────────────────────────

// probeSubcommand is the contractual invocation. It is a SUBCOMMAND, alongside
// "init" and "help" (main.go:176-187), not a flag, so that it dispatches before
// flag parsing and cannot be perturbed by any other argv the caller passes.
//
// The full contractual invocation is exactly:
//
//	classify capabilities
//
// It reads no stdin, touches no config, resolves no repo, and writes nothing
// but its JSON to stdout. A probe that needed a working config could not run at
// preflight, which is where it is called.
const probeSubcommand = "capabilities"

// probeVersion versions the probe's own output shape, independently of
// ContractVersion and of the sidecar's schema_version. A consumer that does not
// recognise probe_version must treat the probe as FAILED, not as absent
// capabilities — "I cannot read the answer" and "the answer is no" are
// different states.
const probeVersion = 1

// exitCapabilityIncomplete is returned when the probe answered truthfully and
// at least one REQUIRED capability is absent.
//
// CHOICE (the design says the exit code is contractual but does not fix the
// value): 4. classify currently uses only 0 and 3 (main.go:40-45), and 1 and 2
// carry meanings in the sibling tools' convention (cmd/iterate: 1 ITERATE,
// 2 ESCALATE), so 4 is the first value that is unambiguous across the family.
// Rejected alternative: reusing 3/INVALID_INPUT, which would conflate "your
// argv is wrong" with "this binary is too old" — the second is the operator's
// signal to rebuild, and it must be distinguishable.
const exitCapabilityIncomplete = 4

// CapabilityReport is the probe's stdout. Field names are contractual.
//
// Every capability is a NON-OMITTED boolean. An absent key must never mean
// false here: a consumer parsing this report has to be able to tell "this
// producer says no" from "this producer is so old it has never heard of the
// question", and the second case is a missing report, not a missing key.
type CapabilityReport struct {
	ProbeVersion int `json:"probe_version"`
	// Producer identifies the binary, e.g. "cmd/classify". Informational.
	Producer string `json:"producer"`

	// Capabilities is the probed tuple. Exactly these three keys, always all
	// three present. Adding a key is a probe_version bump.
	Capabilities Capabilities `json:"capabilities"`

	// ContractVersions lists every contract this binary can EMIT, ascending.
	// It is derived from the same closed set ParseContractVersion accepts, not
	// written out by hand.
	ContractVersions []int `json:"contract_versions"`

	// Missing names every REQUIRED capability that is false, so the operator
	// gets the reason without re-deriving it from the booleans. Empty slice,
	// never null, when nothing is missing. When it is non-empty the exit code
	// is exitCapabilityIncomplete; when it is empty the exit code is 0. That
	// biconditional is part of the contract — a report and an exit code that
	// disagree is a broken probe.
	Missing []string `json:"missing"`
}

// Capabilities is the probed tuple of §3.3: framed -authoritative-stdin
// (policy and diff), the dual digest echo, and the contract flag.
type Capabilities struct {
	// FramedAuthoritativeStdin is true iff framedStdinReader is installed.
	// It covers BOTH channels — policy and diff — because a producer that
	// framed only one of them could not compute both digests over consumed
	// bytes, so a partial answer here would be a lie in a useful direction.
	FramedAuthoritativeStdin bool `json:"framed_authoritative_stdin"`
	// DualDigestEcho is true iff digestSource is installed.
	DualDigestEcho bool `json:"dual_digest_echo"`
	// ContractVersionFlag is true iff contractFlagRegistrar is installed.
	ContractVersionFlag bool `json:"contract_version_flag"`
}

// requiredCapabilities names the capabilities a producer must have to pass
// REQUIRED preflight. All three are required; the constant exists so the
// Missing list and the exit code are computed from one enumeration rather than
// from three hand-written conditions.
//
// The operator's ONLY fallback for a producer that fails this probe is an
// explicit LEGACY declaration. There is no partial-REQUIRED mode, and this
// list must not grow a "recommended" tier — that would be a soft default over
// an authority decision.
var requiredCapabilities = []string{
	"framed_authoritative_stdin",
	"dual_digest_echo",
	"contract_version_flag",
}

// probeCapabilities reports what is installed. It is the ONLY place a
// capability boolean is produced, and each boolean must be derived solely from
// whether the corresponding registry variable is non-nil. It must not consult
// build tags, version strings, flag registration state, or anything else.
func probeCapabilities() Capabilities {
	panic("B1: not implemented")
}

// buildCapabilityReport assembles the full report, including ContractVersions
// (from the closed contract set) and Missing (from requiredCapabilities
// intersected with the false booleans, in requiredCapabilities order so the
// output is deterministic and goldenable).
func buildCapabilityReport() CapabilityReport {
	panic("B1: not implemented")
}

// cmdCapabilities implements the probe subcommand.
//
// Contract, in full:
//   - args must be empty. Any argument is INVALID_INPUT: exit 3, message on
//     stderr, NO report on stdout. A probe that tolerated stray argv would let
//     a caller believe it had asked something it had not.
//   - On success it writes exactly one JSON object to stdout, encoded with the
//     same two-space indent and trailing newline as the classification output,
//     and writes nothing to stdout on any other path.
//   - Exit 0 iff Missing is empty. Exit exitCapabilityIncomplete iff Missing is
//     non-empty — and the report is STILL written to stdout in that case,
//     because naming which capability is absent is the point.
//   - It never exits 0 with a non-empty Missing, and never exits
//     exitCapabilityIncomplete with an empty Missing.
//   - It must not call log.Fatalf. The exit code is the contract; a panic or an
//     os.Exit(1) from a logger would be an uncontracted fourth outcome.
//
// Wiring: main's subcommand switch (main.go:176-187) gains
// `case probeSubcommand: os.Exit(cmdCapabilities(os.Args[2:]))`. That wiring is
// the body author's, not the scaffold's.
func cmdCapabilities(args []string) int {
	panic("B1: not implemented")
}

// writeCapabilityReport encodes r to w. Split out so a seal can assert the
// exact bytes without running the exit-code path.
func writeCapabilityReport(w io.Writer, r CapabilityReport) error {
	panic("B1: not implemented")
}
