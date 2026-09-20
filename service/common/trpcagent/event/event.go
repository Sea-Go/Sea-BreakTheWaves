package event

import (
	trpcevent "trpc.group/trpc-go/trpc-agent-go/event"
)

// Snapshot is a transport-neutral projection of a Runner event. It contains no
// framework pointers, so HTTP/SSE and queue transports can consume it safely.
type Snapshot struct {
	InvocationID string
	Author       string
	ID           string
	Object       string
	Delta        string
	Text         string
	Partial      bool
	Done         bool
	ToolCall     bool
	ToolResult   bool
	Final        bool
	RunnerDone   bool
	Err          string
}

// Snapshot converts and null-guards a framework event.
func NewSnapshot(src *trpcevent.Event) Snapshot {
	if src == nil {
		return Snapshot{}
	}
	out := Snapshot{
		InvocationID: src.InvocationID,
		Author:       src.Author,
		ID:           src.ID,
		RunnerDone:   src.IsRunnerCompletion(),
	}
	if src.Response != nil {
		out.Object = src.Response.Object
		out.Partial = src.Response.IsPartial
		out.Done = src.Response.Done
		out.Final = src.Response.IsFinalResponse()
		out.ToolCall = src.Response.IsToolCallResponse()
		out.ToolResult = src.Response.IsToolResultResponse()
		if len(src.Response.Choices) > 0 {
			out.Delta = src.Response.Choices[0].Delta.Content
			out.Text = src.Response.Choices[0].Message.Content
		}
		if src.Response.Error != nil {
			out.Err = src.Response.Error.Message
		}
	}
	if src.IsError() && out.Err == "" {
		out.Err = "agent run error"
	}
	return out
}
