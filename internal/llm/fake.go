package llm

import (
	"context"
	"errors"
	"sync"
)

// FakeClient 是供 Agent 和上层集成测试使用的可编程模型客户端。
type FakeClient struct {
	mu        sync.Mutex
	Responses []Response
	Err       error
	Requests  []Request
}

// Complete 按顺序返回预置响应，并记录请求。
func (f *FakeClient) Complete(_ context.Context, request Request) (Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, request)
	if f.Err != nil {
		return Response{}, f.Err
	}
	if len(f.Responses) == 0 {
		return Response{}, errors.New("fake llm: no response configured")
	}
	response := f.Responses[0]
	f.Responses = f.Responses[1:]
	return response, nil
}

// Stream 将预置响应转换成确定性的增量事件。
func (f *FakeClient) Stream(ctx context.Context, request Request, emit EmitFunc) (Response, error) {
	response, err := f.Complete(ctx, request)
	if err != nil {
		return Response{}, err
	}
	if response.Text != "" && emit != nil {
		if err := emit(StreamEvent{Type: EventTextDelta, Text: response.Text}); err != nil {
			return Response{}, err
		}
	}
	for i := range response.ToolCalls {
		call := response.ToolCalls[i]
		if emit != nil {
			if err := emit(StreamEvent{Type: EventToolCall, ToolCall: &call}); err != nil {
				return Response{}, err
			}
		}
	}
	return response, nil
}
