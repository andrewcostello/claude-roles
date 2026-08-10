package main

// This file exists ONLY because unit B1 is currently a scaffold, and it must be
// DELETED IN ITS ENTIRETY by the unit's body author. It contains no contract.
//
// Why it is here: `cmd/classify` is lint-clean at baseline (golangci-lint,
// 0 issues), and the project treats that gate as required — the gates config
// carries the note "Never trust an agent's self-reported gofmt/vet — PR 1294
// shipped a gofmt failure that way." A scaffold whose declarations nothing
// calls yet trips staticcheck's `unused` on every unexported constant, variable
// and function it introduces, which would hand the seal author a red module and
// train everyone to ignore the lint gate.
//
// The alternatives were worse. A `.golangci.yml` with an `unused` exclusion
// would weaken the gate for the whole repository, permanently, to buy one
// unit's convenience. Seventeen scattered `//nolint:unused` directives would be
// seventeen things to remember to remove. This is one file, at one site, and it
// stops compiling the moment a name it references is renamed or removed — so it
// cannot silently rot into a lie about what the contract surface contains.
//
// It is deliberately NOT a mechanism for keeping the scaffold alive. When the
// body wires the flag, the probe subcommand and the emitters, every name below
// acquires a real caller and this file becomes redundant. If deleting it then
// produces an `unused` finding, that finding is correct and names something the
// body forgot to wire.
//
// Note that staticcheck does not flag the exported-form names in this package
// even though `package main` exports nothing; only the unexported ones need an
// anchor, which is why this list is shorter than the contract surface.
var _ = []any{
	// contract.go
	flagContractVersion,
	defaultContractVersion,
	responseVersion,
	v2SidecarSchemaVersion,

	// capability.go — the honesty registry and the probe's wire constants.
	contractFlagRegistrar,
	digestSource,
	framedStdinReader,
	probeSubcommand,
	probeVersion,
	exitCapabilityIncomplete,
	requiredCapabilities,
	probeCapabilities,
	buildCapabilityReport,
	cmdCapabilities,
	writeCapabilityReport,

	// readset.go
	frozenConsumers,
	volatileV1Fields,
}
