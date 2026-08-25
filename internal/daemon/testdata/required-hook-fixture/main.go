package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	compozysdk "github.com/compozy/compozy/sdk/go"
	"github.com/compozy/compozy/sdk/go/contracts"
)

const deliberateFailureExitCode = 23

func definition() compozysdk.ExtensionDefinition {
	return compozysdk.ExtensionDefinition{
		Name:        "required-hook-fixture",
		Version:     "0.1.0",
		Description: "Deliberate raw hook failure fixture",
		Subprocess: compozysdk.DescribeSubprocess{
			Command: "./bin",
			Env: map[string]string{
				"COMPOZY_REQUIRED_HOOK_MARKER": os.Getenv("COMPOZY_REQUIRED_HOOK_MARKER"),
			},
		},
		SupportedHookEvents: []compozysdk.DescribeHookEvent{{
			Name:     "publisher-guard",
			Event:    contracts.HookEventToolPreCall,
			Mode:     contracts.HookMode("sync"),
			Matcher:  contracts.HookMatcher{AgentName: "batuta-publisher"},
			Required: true,
		}},
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__describe" {
		runExtension(os.Stdin)
		return
	}

	reader := bufio.NewReader(os.Stdin)
	firstFrame, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		fail("read first input frame: %v", err)
	}
	if len(firstFrame) == 0 {
		fail("first input frame is empty")
	}

	var probe struct {
		JSONRPC string              `json:"jsonrpc"`
		Method  string              `json:"method"`
		Event   contracts.HookEvent `json:"event"`
	}
	if err := json.Unmarshal(firstFrame, &probe); err != nil {
		fail("decode first input frame: %v", err)
	}
	switch {
	case probe.Event == contracts.HookEventToolPreCall:
		failRawToolPreCall(firstFrame)
	case probe.JSONRPC == compozysdk.JSONRPCVersion && probe.Method == "initialize":
		runExtension(io.MultiReader(bytes.NewReader(firstFrame), reader))
	default:
		fail("unsupported first input frame")
	}
}

func failRawToolPreCall(frame []byte) {
	var payload contracts.ToolPreCallPayload
	if err := json.Unmarshal(frame, &payload); err != nil {
		fail("decode raw tool.pre_call payload: %v", err)
	}
	if payload.AgentName == "" || payload.ToolID == "" {
		fail("raw tool.pre_call payload lacks matched agent or tool identity")
	}
	evidence, err := json.Marshal(map[string]string{
		"agent_name": payload.AgentName,
		"tool_id":    payload.ToolID,
	})
	if err != nil {
		fail("encode deliberate failure evidence: %v", err)
	}
	if err := os.WriteFile(os.Getenv("COMPOZY_REQUIRED_HOOK_MARKER"), evidence, 0o600); err != nil {
		fail("write deliberate failure evidence: %v", err)
	}
	if _, err := fmt.Fprintln(os.Stderr, "required-hook-fixture: deliberate raw tool.pre_call rejection after payload validation"); err != nil {
		os.Exit(deliberateFailureExitCode)
	}
	os.Exit(deliberateFailureExitCode)
}

func runExtension(input io.Reader) {
	extension := compozysdk.NewExtension(
		definition(),
		compozysdk.WithStdio(input, os.Stdout),
		compozysdk.WithStderr(os.Stderr),
	)
	if err := extension.Run(context.Background()); err != nil {
		fail("run extension RPC server: %v", err)
	}
}

func fail(format string, args ...any) {
	if _, err := fmt.Fprintf(os.Stderr, "required-hook-fixture: "+format+"\n", args...); err != nil {
		os.Exit(1)
	}
	os.Exit(1)
}
