package modelruntime

import "context"

// VLLM is a placeholder adapter. Available until a real client is wired.
type VLLM struct{}

func (VLLM) Name() string                   { return "vllm" }
func (VLLM) Available(context.Context) bool { return false }
func (VLLM) ListModels(context.Context) ([]Model, error) {
	return nil, nil
}
func (VLLM) EnsureModel(context.Context, string, string) (Model, error) {
	return Model{}, ErrRuntimeUnavailable{"vllm"}
}
func (VLLM) Infer(context.Context, InferRequest) (InferResult, error) {
	return InferResult{}, ErrRuntimeUnavailable{"vllm"}
}

// LlamaCPP is a placeholder adapter for future llama.cpp integration.
type LlamaCPP struct{}

func (LlamaCPP) Name() string                   { return "llama.cpp" }
func (LlamaCPP) Available(context.Context) bool { return false }
func (LlamaCPP) ListModels(context.Context) ([]Model, error) {
	return nil, nil
}
func (LlamaCPP) EnsureModel(context.Context, string, string) (Model, error) {
	return Model{}, ErrRuntimeUnavailable{"llama.cpp"}
}
func (LlamaCPP) Infer(context.Context, InferRequest) (InferResult, error) {
	return InferResult{}, ErrRuntimeUnavailable{"llama.cpp"}
}

// ErrRuntimeUnavailable means the adapter is not installed/reachable.
type ErrRuntimeUnavailable struct {
	Runtime string
}

func (e ErrRuntimeUnavailable) Error() string {
	return e.Runtime + " runtime is not available on this node"
}
