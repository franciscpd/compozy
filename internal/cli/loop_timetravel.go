package cli

import (
	"fmt"
	"strings"

	"github.com/compozy/compozy/internal/api/contract"
	"github.com/spf13/cobra"
)

func newLoopDiffCommand(deps commandDeps) *cobra.Command {
	var workspaceRef, runID, againstRunID string
	var generation, againstGeneration int64
	cmd := &cobra.Command{
		Use: loopDiffKey, Short: "Compare Loop generations or runs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, workspaceID, err := loopClientAndWorkspace(cmd, deps, workspaceRef)
			if err != nil {
				return err
			}
			response, err := client.DiffLoopRun(cmd.Context(), workspaceID, strings.TrimSpace(runID),
				generation, againstGeneration, strings.TrimSpace(againstRunID))
			if err != nil {
				return err
			}
			return writeCommandOutput(cmd, loopOutputBundle(response,
				fmt.Sprintf("%s diff · %d node changes", response.Kind, len(response.Nodes))))
		},
	}
	addLoopTimeTravelRunFlags(cmd, &workspaceRef, &runID)
	cmd.Flags().Int64Var(&generation, "generation", 0, "Base generation")
	cmd.Flags().Int64Var(&againstGeneration, "against-generation", 0, "Generation to compare")
	cmd.Flags().StringVar(&againstRunID, "against-run", "", "Run to compare")
	return cmd
}

func newLoopRerunCommand(deps commandDeps) *cobra.Command {
	var workspaceRef, runID, fromNode, reason, requestID string
	var itemIndex int
	cmd := &cobra.Command{
		Use: loopRerunKey, Short: "Rerun from one settled Loop node", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, workspaceID, err := loopClientAndWorkspace(cmd, deps, workspaceRef)
			if err != nil {
				return err
			}
			var item *int
			if cmd.Flags().Changed("item") {
				item = &itemIndex
			}
			response, err := client.RerunLoopRun(cmd.Context(), workspaceID, strings.TrimSpace(runID),
				contract.RerunLoopRequest{FromNode: strings.TrimSpace(fromNode), ItemIndex: item,
					Reason: strings.TrimSpace(reason), RequestID: strings.TrimSpace(requestID)},
				agentCredentialsFromEnv(deps))
			if err != nil {
				return err
			}
			return writeCommandOutput(cmd, loopOutputBundle(response,
				fmt.Sprintf("rerun · %s · generation %d", response.RunID, response.Generation)))
		},
	}
	addLoopTimeTravelRunFlags(cmd, &workspaceRef, &runID)
	cmd.Flags().StringVar(&fromNode, "from-node", "", "Settled node to rerun")
	cmd.Flags().IntVar(&itemIndex, "item", 0, "Fan-out lane index")
	cmd.Flags().StringVar(&reason, "reason", "", "Rerun reason")
	cmd.Flags().StringVar(&requestID, "request-id", "", "Idempotency key")
	mustMarkFlagRequired(cmd, "from-node")
	return cmd
}

func newLoopForkCommand(deps commandDeps) *cobra.Command {
	var workspaceRef, runID, reason, requestID string
	var generation int64
	var inputs []string
	cmd := &cobra.Command{
		Use: loopForkKey, Short: "Fork a new Loop run from history", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, workspaceID, err := loopClientAndWorkspace(cmd, deps, workspaceRef)
			if err != nil {
				return err
			}
			values, err := parseLoopInputFlags(inputs)
			if err != nil {
				return err
			}
			response, err := client.ForkLoopRun(cmd.Context(), workspaceID, strings.TrimSpace(runID),
				contract.ForkLoopRequest{Generation: generation, Inputs: values,
					Reason: strings.TrimSpace(reason), RequestID: strings.TrimSpace(requestID)},
				agentCredentialsFromEnv(deps))
			if err != nil {
				return err
			}
			return writeCommandOutput(cmd, loopOutputBundle(response,
				fmt.Sprintf("fork · %s · from %s generation %d", response.Run.ID, runID, generation)))
		},
	}
	addLoopTimeTravelRunFlags(cmd, &workspaceRef, &runID)
	cmd.Flags().Int64Var(&generation, "generation", 0, "Source generation")
	cmd.Flags().StringArrayVar(&inputs, loopInputKey, nil, "Input override key=value")
	cmd.Flags().StringVar(&reason, "reason", "", "Fork reason")
	cmd.Flags().StringVar(&requestID, "request-id", "", "Idempotency key")
	mustMarkFlagRequired(cmd, "generation")
	return cmd
}

func newLoopRecoverNestedCommand(deps commandDeps) *cobra.Command {
	var workspaceRef, runID, requestID, runtimeFile string
	cmd := &cobra.Command{
		Use: loopRecoverNestedKey, Short: "Recover one failed direct child Loop run", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, workspaceID, err := loopClientAndWorkspace(cmd, deps, workspaceRef)
			if err != nil {
				return err
			}
			runtime, err := readLoopRuntimeFile(runtimeFile)
			if err != nil {
				return err
			}
			response, err := client.RecoverNestedLoopRun(
				cmd.Context(), workspaceID, strings.TrimSpace(runID),
				contract.RecoverNestedLoopRequest{RequestID: strings.TrimSpace(requestID), Runtime: runtime},
				agentCredentialsFromEnv(deps),
			)
			if err != nil {
				return err
			}
			return writeCommandOutput(cmd, loopOutputBundle(response,
				fmt.Sprintf("nested recovery · %s · child %s", response.OperationID, response.ChildRunID)))
		},
	}
	cmd.Flags().StringVar(&workspaceRef, loopWorkspaceKey, "", "Override workspace (ID, name, or path)")
	cmd.Flags().StringVar(&runID, "run", "", "Parent Loop run ID")
	cmd.Flags().StringVar(&requestID, "request-id", "", "Required idempotency key")
	cmd.Flags().StringVar(&runtimeFile, "runtime-file", "", "Internal JSON runtime selection file")
	mustMarkFlagRequired(cmd, "run")
	mustMarkFlagRequired(cmd, "request-id")
	mustMarkFlagRequired(cmd, "runtime-file")
	return cmd
}

func addLoopTimeTravelRunFlags(cmd *cobra.Command, workspaceRef, runID *string) {
	cmd.Flags().StringVar(workspaceRef, loopWorkspaceKey, "", "Override workspace (ID, name, or path)")
	cmd.Flags().StringVar(runID, loopRunIDKey, "", "Loop run ID")
	mustMarkFlagRequired(cmd, loopRunIDKey)
}
