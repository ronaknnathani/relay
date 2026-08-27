package program

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// ParseContractRef parses a safe-name@vN contract reference.
func ParseContractRef(ref string) (string, int, error) {
	name, versionText, found := strings.Cut(ref, "@v")
	if !found || name == "" || versionText == "" || strings.Contains(versionText, "@") {
		return "", 0, fmt.Errorf("invalid contract ref %q: want safe-name@vN", ref)
	}
	if safeContractName(name) != name {
		return "", 0, fmt.Errorf("invalid contract ref %q: name must be lowercase letters, digits, and single hyphens", ref)
	}
	version, err := strconv.Atoi(versionText)
	if err != nil || version <= 0 || strconv.Itoa(version) != versionText {
		return "", 0, fmt.Errorf("invalid contract ref %q: version must be a positive integer", ref)
	}
	return name, version, nil
}

// PublishContract copies sourcePath into a new immutable contract version,
// records its SHA-256, and opens the corresponding CEO approval decision.
func (p *Program) PublishContract(programDir, name, sourcePath string) (Contract, error) {
	if err := p.Validate(); err != nil {
		return Contract{}, fmt.Errorf("publish contract: current program is invalid: %w", err)
	}
	if err := p.ensureMutable("publish contract"); err != nil {
		return Contract{}, err
	}
	safeName := safeContractName(name)
	if safeName == "" {
		return Contract{}, fmt.Errorf("publish contract: name %q has no safe characters", name)
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return Contract{}, fmt.Errorf("publish contract %q: read source %s: %w", safeName, sourcePath, err)
	}
	version := 1
	for _, contract := range p.Contracts {
		if contract.Name == safeName && contract.Version >= version {
			version = contract.Version + 1
		}
	}
	ref := fmt.Sprintf("%s@v%d", safeName, version)
	relativePath := filepath.Join("contracts", safeName, fmt.Sprintf("v%d.md", version))
	targetPath := filepath.Join(programDir, relativePath)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return Contract{}, fmt.Errorf("publish contract %q: create directory %s: %w", ref, filepath.Dir(targetPath), err)
	}
	file, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return Contract{}, fmt.Errorf("publish contract %q: create immutable file %s: %w", ref, targetPath, err)
	}
	if _, err := file.Write(source); err != nil {
		cleanupErr := closeAndRemove(file, targetPath, err)
		return Contract{}, fmt.Errorf("publish contract %q: write %s: %w", ref, targetPath, cleanupErr)
	}
	if err := file.Sync(); err != nil {
		cleanupErr := closeAndRemove(file, targetPath, err)
		return Contract{}, fmt.Errorf("publish contract %q: sync %s: %w", ref, targetPath, cleanupErr)
	}
	if err := file.Close(); err != nil {
		cleanupErr := removeWithCause(targetPath, err)
		return Contract{}, fmt.Errorf("publish contract %q: close %s: %w", ref, targetPath, cleanupErr)
	}

	sum := sha256.Sum256(source)
	now := timestamp()
	contract := Contract{
		Name:        safeName,
		Version:     version,
		Ref:         ref,
		Path:        filepath.ToSlash(relativePath),
		SHA256:      fmt.Sprintf("%x", sum),
		Status:      ContractPending,
		PublishedAt: now,
	}
	next := *p
	next.Contracts = append(append([]Contract(nil), p.Contracts...), contract)
	next.UpdatedAt = now
	if _, created, err := next.OpenDecision(Decision{
		Kind:        DecisionContract,
		RaisedBy:    RaisedByCTO,
		ContractRef: ref,
		Question:    "Approve contract " + ref + "?",
		Options:     []string{"approve", "reject"},
	}); err != nil {
		cleanupErr := removeWithCause(targetPath, err)
		return Contract{}, fmt.Errorf("publish contract %q: open approval decision: %w", ref, cleanupErr)
	} else if !created {
		cleanupErr := removeWithCause(targetPath, fmt.Errorf("an open approval decision already exists"))
		return Contract{}, fmt.Errorf("publish contract %q: %w", ref, cleanupErr)
	}
	if err := next.Validate(); err != nil {
		cleanupErr := removeWithCause(targetPath, err)
		return Contract{}, fmt.Errorf("publish contract %q: %w", ref, cleanupErr)
	}
	*p = next
	return contract, nil
}

// ApproveContract approves an exact contract reference and resolves its open
// contract decision.
func (p *Program) ApproveContract(ref, by string) error {
	if strings.TrimSpace(by) == "" {
		return fmt.Errorf("approve contract %q: by is required", ref)
	}
	if _, _, err := ParseContractRef(ref); err != nil {
		return fmt.Errorf("approve contract: %w", err)
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("approve contract %q: current program is invalid: %w", ref, err)
	}
	if err := p.ensureMutable("approve contract " + ref); err != nil {
		return err
	}
	contractIndex := -1
	for i := range p.Contracts {
		if p.Contracts[i].Ref == ref {
			contractIndex = i
			break
		}
	}
	if contractIndex < 0 {
		return fmt.Errorf("approve contract %q: contract not found", ref)
	}
	if p.Contracts[contractIndex].Status != ContractPending {
		return fmt.Errorf("approve contract %q: status is %q, want pending", ref, p.Contracts[contractIndex].Status)
	}
	decisionIndex := -1
	for i := range p.Decisions {
		decision := p.Decisions[i]
		if decision.Kind == DecisionContract && decision.ContractRef == ref && decision.ResolvedAt == "" {
			if decisionIndex >= 0 {
				return fmt.Errorf("approve contract %q: multiple open contract decisions", ref)
			}
			decisionIndex = i
		}
	}
	if decisionIndex < 0 {
		return fmt.Errorf("approve contract %q: open contract decision not found", ref)
	}
	now := timestamp()
	next := *p
	next.Contracts = append([]Contract(nil), p.Contracts...)
	next.Decisions = append([]Decision(nil), p.Decisions...)
	next.Contracts[contractIndex].Status = ContractApproved
	next.Contracts[contractIndex].ApprovedAt = now
	next.Contracts[contractIndex].ApprovedBy = by
	next.Decisions[decisionIndex].Answer = "approved"
	next.Decisions[decisionIndex].ResolvedBy = by
	next.Decisions[decisionIndex].ResolvedAt = now
	next.UpdatedAt = now
	if err := next.Validate(); err != nil {
		return fmt.Errorf("approve contract %q: %w", ref, err)
	}
	*p = next
	return nil
}

// RejectContract rejects an exact contract reference and resolves its open
// contract decision.
func (p *Program) RejectContract(ref, by, reason string) error {
	if strings.TrimSpace(by) == "" {
		return fmt.Errorf("reject contract %q: by is required", ref)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("reject contract %q: reason is required", ref)
	}
	if _, _, err := ParseContractRef(ref); err != nil {
		return fmt.Errorf("reject contract: %w", err)
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("reject contract %q: current program is invalid: %w", ref, err)
	}
	if err := p.ensureMutable("reject contract " + ref); err != nil {
		return err
	}
	contractIndex := -1
	for i := range p.Contracts {
		if p.Contracts[i].Ref == ref {
			contractIndex = i
			break
		}
	}
	if contractIndex < 0 {
		return fmt.Errorf("reject contract %q: contract not found", ref)
	}
	if p.Contracts[contractIndex].Status != ContractPending {
		return fmt.Errorf("reject contract %q: status is %q, want pending", ref, p.Contracts[contractIndex].Status)
	}
	decisionIndex := -1
	for i := range p.Decisions {
		decision := p.Decisions[i]
		if decision.Kind == DecisionContract && decision.ContractRef == ref && decision.ResolvedAt == "" {
			if decisionIndex >= 0 {
				return fmt.Errorf("reject contract %q: multiple open contract decisions", ref)
			}
			decisionIndex = i
		}
	}
	if decisionIndex < 0 {
		return fmt.Errorf("reject contract %q: open contract decision not found", ref)
	}
	now := timestamp()
	next := *p
	next.Contracts = append([]Contract(nil), p.Contracts...)
	next.Decisions = append([]Decision(nil), p.Decisions...)
	next.Contracts[contractIndex].Status = ContractRejected
	next.Contracts[contractIndex].RejectedAt = now
	next.Contracts[contractIndex].RejectedBy = by
	next.Contracts[contractIndex].RejectionReason = reason
	next.Decisions[decisionIndex].Answer = "rejected: " + reason
	next.Decisions[decisionIndex].ResolvedBy = by
	next.Decisions[decisionIndex].ResolvedAt = now
	next.UpdatedAt = now
	if err := next.Validate(); err != nil {
		return fmt.Errorf("reject contract %q: %w", ref, err)
	}
	*p = next
	return nil
}

// VerifyHashes verifies every recorded contract against its file under
// programDir.
func (p Program) VerifyHashes(programDir string) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("verify contract hashes: invalid program: %w", err)
	}
	var errs []error
	for _, contract := range p.Contracts {
		path := filepath.Join(programDir, filepath.FromSlash(contract.Path))
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("verify contract %q at %s: %w", contract.Ref, path, err))
			continue
		}
		sum := sha256.Sum256(data)
		actual := fmt.Sprintf("%x", sum)
		if actual != contract.SHA256 {
			errs = append(errs, fmt.Errorf("verify contract %q: sha256 mismatch: got %s, want %s", contract.Ref, actual, contract.SHA256))
		}
	}
	return errors.Join(errs...)
}

func safeContractName(name string) string {
	var result strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if r > unicode.MaxASCII {
				hyphen = result.Len() > 0
				continue
			}
			if hyphen && result.Len() > 0 {
				result.WriteByte('-')
			}
			result.WriteRune(r)
			hyphen = false
			continue
		}
		hyphen = result.Len() > 0
	}
	return strings.Trim(result.String(), "-")
}
