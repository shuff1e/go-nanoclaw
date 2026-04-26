package gateway

import (
	"context"
	"fmt"

	mclog "go-nanoclaw/internal/log"
	mcRuntime "go-nanoclaw/internal/runtime"
)

type agentRuntime interface {
	Process(ctx context.Context, execCtx *mcRuntime.ExecutionContext, text, agentID string) (runtimeResult, error)
}

type runtimeResult struct {
	TargetAgentID string
	Response      string
}

type nativeAgentRuntime struct {
	gateway *Gateway
}

func (r *nativeAgentRuntime) Process(ctx context.Context, execCtx *mcRuntime.ExecutionContext, text, agentID string) (runtimeResult, error) {
	a, err := r.gateway.Orchestrator.GetOrCreateAgent(agentID)
	if err != nil {
		return runtimeResult{}, mcRuntime.Wrap(mcRuntime.CodeInternal, err, "get agent '%s'", agentID)
	}
	r.gateway.configureAgentStorage(agentID, a)

	route := a.Router.Route(text, agentID)
	result := runtimeResult{TargetAgentID: agentID}
	err = r.gateway.dispatcher.Run(ctx, execCtx, func(runCtx context.Context) error {
		runCtx = mcRuntime.WithExecutionContext(runCtx, execCtx)
		if route.TargetAgent != agentID {
			mclog.Tree(0, true, fmt.Sprintf("路由到其他Agent: %s (技能匹配: %d)", route.TargetAgent, len(route.MatchedSkills)))
			result.TargetAgentID = route.TargetAgent
		}

		if result.TargetAgentID != agentID {
			targetAgent, getErr := r.gateway.Orchestrator.GetOrCreateAgent(result.TargetAgentID)
			if getErr != nil {
				return mcRuntime.Wrap(mcRuntime.CodeInternal, getErr, "get target agent '%s'", result.TargetAgentID)
			}
			result.Response, getErr = targetAgent.ProcessExecution(runCtx, execCtx, text)
			return getErr
		}

		var processErr error
		result.Response, processErr = a.ProcessExecution(runCtx, execCtx, text)
		return processErr
	})
	if err != nil {
		return runtimeResult{}, err
	}
	return result, nil
}
