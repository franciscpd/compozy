package globaldb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	looppkg "github.com/compozy/compozy/internal/loop"
)

var _ looppkg.NestedRecoveryRuntimeReader = (*LoopRepo)(nil)

// GetNestedRecoveryRuntime reads an ephemeral override by its complete child cell coordinate.
func (g *LoopRepo) GetNestedRecoveryRuntime(
	ctx context.Context,
	key looppkg.NestedRecoveryRuntimeKey,
) (looppkg.RuntimeSpec, bool, error) {
	if err := g.checkReady(ctx, "get nested recovery runtime"); err != nil {
		return looppkg.RuntimeSpec{}, false, err
	}
	var raw string
	err := g.db.QueryRowContext(ctx, `SELECT runtime_json
		FROM loop_nested_recoveries
		WHERE workspace_id = ? AND child_run_id = ? AND child_generation = ?
			AND child_node_id = ? AND child_item_index = ?`,
		key.WorkspaceID, key.RunID, key.Generation, key.NodeID, key.ItemIndex,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return looppkg.RuntimeSpec{}, false, nil
	}
	if err != nil {
		return looppkg.RuntimeSpec{}, false, fmt.Errorf("store: get nested recovery runtime: %w", err)
	}
	var resolved looppkg.ResolvedRuntime
	if err := json.Unmarshal([]byte(raw), &resolved); err != nil {
		return looppkg.RuntimeSpec{}, false, fmt.Errorf("store: decode nested recovery runtime: %w", err)
	}
	return resolved.Runtime, true, nil
}
