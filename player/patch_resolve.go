package main

// patch_resolve.go implements dependency resolution and the transaction
// engine behind install/upgrade/remove/rollback. Every mutation is planned
// first (PlanTransaction), previewed by the UI, then committed under a
// journal with backups; any failure rolls back in reverse order.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ResolveError reports a dependency-resolution failure.
type ResolveError struct{ Msg string }

func (e *ResolveError) Error() string { return e.Msg }

// PlanStep is one planned operation in a transaction.
type PlanStep struct {
	Op      string // install / upgrade / remove
	Pkg     string
	Version string
	Risk    string
	Files   []string
}

// TransactionPlan is the full previewable plan.
type TransactionPlan struct {
	Steps    []PlanStep
	TotalMB  int64
	Warnings []string
}

// resolveInstall builds a deterministic install plan for a package and its
// dependencies. deps maps name -> version (the full repo package set); the
// repo index provides conflicts/provides/depends metadata.
func resolveInstall(name string, repoPkgs map[string]*RepoPkg, isInstalled func(string) bool) (*TransactionPlan, error) {
	plan := &TransactionPlan{}
	seen := map[string]bool{}
	visiting := map[string]bool{}
	var order []string

	var visit func(n string) error
	visit = func(n string) error {
		if isInstalled(n) {
			return nil
		}
		if seen[n] {
			return nil
		}
		if visiting[n] {
			return &ResolveError{Msg: fmt.Sprintf("dependency cycle detected at %q", n)}
		}
		visiting[n] = true
		pkg, ok := repoPkgs[n]
		if !ok {
			delete(visiting, n)
			return &ResolveError{Msg: fmt.Sprintf("missing dependency %q", n)}
		}
		// Conflicts: reject packages that conflict with anything installed or
		// planned.
		for _, c := range pkg.Conflicts {
			if isInstalled(c) || seen[c] {
				return &ResolveError{Msg: fmt.Sprintf("%q conflicts with installed package %q", n, c)}
			}
		}
		for _, d := range pkg.Depends {
			if err := visit(d); err != nil {
				return err
			}
		}
		visiting[n] = false
		seen[n] = true
		order = append(order, n)
		return nil
	}
	if err := visit(name); err != nil {
		return nil, err
	}
	for _, n := range order {
		p := repoPkgs[n]
		plan.Steps = append(plan.Steps, PlanStep{
			Op: "install", Pkg: n, Version: p.Version, Risk: p.Risk,
		})
		plan.TotalMB += p.Compressed / (1024 * 1024)
	}
	return plan, nil
}

// checkReverseDependencies verifies that removing a package does not break
// installed packages that depend on it.
func checkReverseDependencies(name string, repoPkgs map[string]*RepoPkg, isInstalled func(string) bool) error {
	for pkgName, p := range repoPkgs {
		if pkgName == name || !isInstalled(pkgName) {
			continue
		}
		for _, d := range p.Depends {
			if d == name {
				return &ResolveError{Msg: fmt.Sprintf("cannot remove %q: installed package %q depends on it", name, pkgName)}
			}
		}
		for _, pr := range p.Provides {
			if pr == name {
				return &ResolveError{Msg: fmt.Sprintf("cannot remove %q: installed package %q provides it", name, pkgName)}
			}
		}
	}
	return nil
}

// planRemove builds a deterministic remove plan with reverse-dependency checks.
func planRemove(name string, repoPkgs map[string]*RepoPkg, isInstalled func(string) bool) (*TransactionPlan, error) {
	if !isInstalled(name) {
		return nil, &ResolveError{Msg: fmt.Sprintf("package %q is not installed", name)}
	}
	if err := checkReverseDependencies(name, repoPkgs, isInstalled); err != nil {
		return nil, err
	}
	p := dbFindInstalled(name)
	risk := p.Risk
	if risk == "" {
		risk = "low"
	}
	files := make([]string, 0, len(p.Files))
	for _, f := range p.Files {
		files = append(files, f.Path)
	}
	return &TransactionPlan{Steps: []PlanStep{{Op: "remove", Pkg: name, Version: p.Version, Risk: risk, Files: files}}}, nil
}

// --- Transaction engine ---

// txConflict describes a user-modified-file conflict during rollback/removal.
type txConflict struct {
	Path         string
	InstalledSHA string
	CurrentSHA   string
}

// txJournal wraps the low-level journal for package transactions.
type txJournal struct {
	journal   *journal
	conflicts []txConflict
}

// runTransaction executes a plan: backups first, then operations, with the
// journal recording every destructive step. On failure it rolls back.
// installFn/removeFn perform the per-package file operations.
func runTransaction(ctx context.Context, plan *TransactionPlan, backupReason string,
	installFn func(ctx context.Context, name string) error,
	removeFn func(ctx context.Context, name string) error) error {

	if len(plan.Steps) == 0 {
		return errors.New("empty transaction plan")
	}
	// Create a backup set covering all managed files before mutating.
	var toBackup []string
	for _, st := range plan.Steps {
		toBackup = append(toBackup, st.Files...)
	}
	if _, _, err := CreateBackupSet(backupReason, toBackup); err != nil {
		return fmt.Errorf("transaction backup failed: %w", err)
	}
	completed := make([]string, 0, len(plan.Steps))
	for _, st := range plan.Steps {
		if err := ctx.Err(); err != nil {
			break
		}
		var err error
		if st.Op == "remove" {
			if removeFn != nil {
				err = removeFn(ctx, st.Pkg)
			}
		} else {
			if installFn != nil {
				err = installFn(ctx, st.Pkg)
			}
		}
		if err != nil {
			// Roll back completed steps in reverse order.
			var rollbackErr error
			for i := len(completed) - 1; i >= 0; i-- {
				if e := removeFn(context.Background(), completed[i]); e != nil && rollbackErr == nil {
					rollbackErr = e
				}
			}
			if rollbackErr != nil {
				return fmt.Errorf("transaction failed (%v); rollback incomplete: %v", err, rollbackErr)
			}
			return fmt.Errorf("transaction failed at %s: %w", st.Pkg, err)
		}
		completed = append(completed, st.Pkg)
	}
	return nil
}

// sortPlanByRisk orders plan steps Low -> Medium -> System for a readable
// preview (deterministic within a risk level by name).
func sortPlanByRisk(plan *TransactionPlan) {
	rank := func(r string) int {
		switch r {
		case "low":
			return 0
		case "medium":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(plan.Steps, func(i, j int) bool {
		ri, rj := rank(plan.Steps[i].Risk), rank(plan.Steps[j].Risk)
		if ri != rj {
			return ri < rj
		}
		return strings.Compare(plan.Steps[i].Pkg, plan.Steps[j].Pkg) < 0
	})
}

// planHasRisk reports whether a plan contains a given-or-higher risk level.
func planHasRisk(plan *TransactionPlan, minRisk string) bool {
	rank := func(r string) int {
		switch r {
		case "low":
			return 0
		case "medium":
			return 1
		default:
			return 2
		}
	}
	for _, st := range plan.Steps {
		if rank(st.Risk) >= rank(minRisk) {
			return true
		}
	}
	return false
}
