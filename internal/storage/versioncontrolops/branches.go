package versioncontrolops

import (
	"context"
	"fmt"
)

// ListBranches returns the names of all Dolt branches, sorted by name.
func ListBranches(ctx context.Context, db DBConn) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM dolt_branches ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}
	defer rows.Close()

	var branches []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan branch: %w", err)
		}
		branches = append(branches, name)
	}
	return branches, rows.Err()
}

// CurrentBranch returns the name of the active branch.
func CurrentBranch(ctx context.Context, db DBConn) (string, error) {
	var branch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch); err != nil {
		return "", fmt.Errorf("get current branch: %w", err)
	}
	return branch, nil
}

// CreateBranch creates a new Dolt branch from the current HEAD.
func CreateBranch(ctx context.Context, db DBConn, name string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?)", name); err != nil {
		return fmt.Errorf("create branch %s: %w", name, err)
	}
	return nil
}

// DeleteBranch force-deletes a Dolt branch.
func DeleteBranch(ctx context.Context, db DBConn, name string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", name); err != nil {
		return fmt.Errorf("delete branch %s: %w", name, err)
	}
	return nil
}

// CheckoutBranch switches the active session to the named branch.
func CheckoutBranch(ctx context.Context, db DBConn, name string) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", name); err != nil {
		return fmt.Errorf("checkout branch %s: %w", name, err)
	}
	return nil
}
