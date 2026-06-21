package agent

import (
	"context"
	"strings"
	"testing"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

type scriptedAgent struct {
	prompts []string
	outputs []string
}

func (a *scriptedAgent) Run(ctx context.Context, invocation *agentcore.Invocation) (<-chan *event.Event, error) {
	a.prompts = append(a.prompts, invocation.Message.Content)
	idx := len(a.prompts) - 1
	output := ""
	if idx < len(a.outputs) {
		output = a.outputs[idx]
	}
	ch := make(chan *event.Event, 1)
	ch <- &event.Event{
		Response: &model.Response{
			Choices: []model.Choice{{
				Message: model.Message{Content: output},
			}},
		},
	}
	close(ch)
	return ch, nil
}

func (a *scriptedAgent) Tools() []tool.Tool {
	return nil
}

func (a *scriptedAgent) Info() agentcore.Info {
	return agentcore.Info{Name: "scripted-agent"}
}

func (a *scriptedAgent) SubAgents() []agentcore.Agent {
	return nil
}

func (a *scriptedAgent) FindSubAgent(name string) agentcore.Agent {
	return nil
}

func TestRequirementIntakeUsesLLMExtractedStartCityAndDoesNotAskAgain(t *testing.T) {
	ag := &scriptedAgent{outputs: []string{`{
		"skill_name":"travel-requirement-intake",
		"stage":"requirement_intake",
		"status":"ready",
		"requirement_ready":true,
		"missing_fields":[],
		"follow_up_questions":[],
		"result":{
			"requirement":{
				"destination_scope":"云南",
				"total_days":7,
				"start_city":"北京",
				"start_date":"2026-07-01",
				"budget_total":"2万",
				"transport_mode":"自驾",
				"travel_style":["自然风光"],
				"pace":"均衡",
				"daily_driving_preference":"4-6小时",
				"accommodation_style":"经济舒适",
				"food_preference":["当地特色"]
			},
			"default_intent":"none"
		},
		"next_stage":"macro_planning",
		"stop_workflow":false,
		"output":"需求已确认"
	}`}}
	orchestrator := NewTravelSkillOrchestrator()

	result, err := orchestrator.Handle(context.Background(), "user-intake", "session-intake", "出发城市北京，7天去云南自驾", ag)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if result == nil || !result.RequirementReady || result.NextStage != StageMacroPlanning {
		t.Fatalf("expected requirement ready, got %#v", result)
	}
	rt := orchestrator.LoadOrInitRuntime("user-intake", "session-intake")
	if rt.Requirement.StartCity != "北京" {
		t.Fatalf("start_city = %q, want 北京", rt.Requirement.StartCity)
	}
	if stringSliceContains(rt.Requirement.MissingFields, "start_city") || strings.Contains(result.Output, "出发") {
		t.Fatalf("should not ask start city again: result=%#v runtime=%#v", result, rt.Requirement)
	}
}

func TestRequirementIntakeBlocksPlanningWhenP0MissingAndUsesLLMQuestion(t *testing.T) {
	ag := &scriptedAgent{outputs: []string{`{
		"skill_name":"travel-requirement-intake",
		"stage":"requirement_intake",
		"status":"need_user_input",
		"requirement_ready":false,
		"missing_fields":["start_city"],
		"follow_up_questions":["LLM生成：请告诉我出发城市。"],
		"result":{
			"requirement":{
				"destination_scope":"上海",
				"total_days":3
			},
			"default_intent":"none"
		},
		"next_stage":"awaiting_user_info",
		"stop_workflow":true,
		"output":"LLM生成：请告诉我出发城市。"
	}`}}
	orchestrator := NewTravelSkillOrchestrator()

	result, err := orchestrator.Handle(context.Background(), "user-p0", "session-p0", "我想去上海玩3天", ag)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if result == nil || result.RequirementReady || result.NextStage != StageAwaitingUserInfo {
		t.Fatalf("expected user input request, got %#v", result)
	}
	if !stringSliceContains(result.MissingFields, "start_city") {
		t.Fatalf("missing fields should contain start_city: %#v", result.MissingFields)
	}
	if result.Output != "LLM生成：请告诉我出发城市。" {
		t.Fatalf("should use LLM follow-up output, got %q", result.Output)
	}
	if len(ag.prompts) != 1 {
		t.Fatalf("valid LLM follow-up should avoid extra question prompt, calls=%d", len(ag.prompts))
	}
}

func TestRequirementMergeKeepsExistingStartCityWhenLLMOmitsIt(t *testing.T) {
	ag := &scriptedAgent{outputs: []string{`{
		"skill_name":"travel-requirement-merge",
		"stage":"requirement_merge",
		"status":"need_user_input",
		"requirement_ready":false,
		"missing_fields":["start_date","transport_mode","travel_style","pace","accommodation_style","food_preference"],
		"follow_up_questions":["LLM生成：还需要确认时间、交通和偏好。"],
		"result":{
			"requirement":{"budget_total":"2万"},
			"default_intent":"none"
		},
		"next_stage":"awaiting_user_info",
		"stop_workflow":true,
		"output":"LLM生成：还需要确认时间、交通和偏好。"
	}`}}
	orchestrator := NewTravelSkillOrchestrator()
	orchestrator.LoadOrInitRuntime("user-merge", "session-merge")
	orchestrator.updateRuntime("user-merge", "session-merge", func(rt *TravelSkillRuntime) {
		rt.CurrentStage = StageAwaitingUserInfo
		rt.Requirement = TravelRequirementSnapshot{
			DestinationScope: "云南",
			TotalDays:        5,
			StartCity:        "北京",
			MissingFields:    []string{"budget", "transport_mode", "travel_style", "pace"},
		}
		rt.LastFollowUpQuestions = []string{"上一轮 LLM 追问"}
	})

	result, err := orchestrator.Handle(context.Background(), "user-merge", "session-merge", "预算2万", ag)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	rt := orchestrator.LoadOrInitRuntime("user-merge", "session-merge")
	if rt.Requirement.StartCity != "北京" {
		t.Fatalf("start_city should be preserved, got %q", rt.Requirement.StartCity)
	}
	if stringSliceContains(result.MissingFields, "start_city") || strings.Contains(result.Output, "出发城市") {
		t.Fatalf("should not ask existing start_city again: %#v", result)
	}
	if !strings.Contains(ag.prompts[0], "上一轮 LLM 追问") {
		t.Fatalf("merge prompt should include previous LLM question: %s", ag.prompts[0])
	}
}

func TestRequirementMergeDefaultIntentRunsPromptDefaultCompletion(t *testing.T) {
	ag := &scriptedAgent{outputs: []string{
		`{
			"skill_name":"travel-requirement-merge",
			"stage":"requirement_merge",
			"status":"need_user_input",
			"requirement_ready":false,
			"missing_fields":["start_date","budget","transport_mode","travel_style","pace","accommodation_style","food_preference"],
			"follow_up_questions":[],
			"result":{"requirement":{},"default_intent":"explicit_default"},
			"next_stage":"awaiting_user_info",
			"stop_workflow":true,
			"output":""
		}`,
		`{
			"skill_name":"travel-requirement-default-completion",
			"stage":"requirement_merge",
			"status":"ready",
			"requirement_ready":true,
			"missing_fields":[],
			"follow_up_questions":[],
			"result":{
				"requirement":{
					"start_date":"2026-07-01",
					"budget_total":"中等",
					"transport_mode":"高铁",
					"travel_style":["自然风光"],
					"pace":"均衡",
					"accommodation_style":"经济舒适",
					"food_preference":["当地特色"]
				},
				"default_intent":"explicit_default"
			},
			"next_stage":"macro_planning",
			"stop_workflow":false,
			"output":"已按默认补齐。"
		}`,
	}}
	orchestrator := NewTravelSkillOrchestrator()
	orchestrator.LoadOrInitRuntime("user-default", "session-default")
	orchestrator.updateRuntime("user-default", "session-default", func(rt *TravelSkillRuntime) {
		rt.CurrentStage = StageAwaitingUserInfo
		rt.Requirement = TravelRequirementSnapshot{
			DestinationScope: "上海",
			TotalDays:        3,
			StartCity:        "北京",
			MissingFields:    []string{"start_date", "budget", "transport_mode", "travel_style", "pace"},
		}
	})

	result, err := orchestrator.Handle(context.Background(), "user-default", "session-default", "按默认来，别问了", ag)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if result == nil || !result.RequirementReady || result.NextStage != StageMacroPlanning {
		t.Fatalf("expected default completion to enter planning, got %#v", result)
	}
	if len(ag.prompts) != 2 || !strings.Contains(ag.prompts[1], "默认补齐") {
		t.Fatalf("expected merge + default prompts, prompts=%#v", ag.prompts)
	}
	rt := orchestrator.LoadOrInitRuntime("user-default", "session-default")
	if rt.Requirement.BudgetTotal == "" || rt.Requirement.TransportMode == "" {
		t.Fatalf("default prompt fields were not merged: %#v", rt.Requirement)
	}
}

func TestNewPlanningIntentClassifierControlsRuntimeReset(t *testing.T) {
	t.Run("true resets and re-runs intake", func(t *testing.T) {
		ag := &scriptedAgent{outputs: []string{
			`{"new_planning_intent":true}`,
			`{
				"skill_name":"travel-requirement-intake",
				"stage":"requirement_intake",
				"status":"ready",
				"requirement_ready":true,
				"missing_fields":[],
				"follow_up_questions":[],
				"result":{
					"requirement":{
						"destination_scope":"杭州",
						"total_days":3,
						"start_city":"上海",
						"start_date":"2026-07-01",
						"budget_total":"5000元",
						"transport_mode":"高铁",
						"travel_style":["美食"],
						"pace":"均衡",
						"accommodation_style":"经济舒适",
						"food_preference":["当地特色"]
					},
					"default_intent":"none"
				},
				"next_stage":"macro_planning",
				"stop_workflow":false,
				"output":"新需求已确认"
			}`,
		}}
		orchestrator := NewTravelSkillOrchestrator()
		orchestrator.LoadOrInitRuntime("user-new", "session-new")
		orchestrator.updateRuntime("user-new", "session-new", func(rt *TravelSkillRuntime) {
			rt.CurrentStage = StageMacroPlanning
			rt.Requirement = TravelRequirementSnapshot{
				DestinationScope: "云南",
				TotalDays:        7,
				StartCity:        "北京",
				RequirementReady: true,
			}
		})

		result, err := orchestrator.Handle(context.Background(), "user-new", "session-new", "重新规划：上海出发，杭州3天", ag)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if result == nil || !result.RequirementReady || len(ag.prompts) != 2 {
			t.Fatalf("expected classifier and intake, result=%#v prompts=%d", result, len(ag.prompts))
		}
		rt := orchestrator.LoadOrInitRuntime("user-new", "session-new")
		if rt.Requirement.DestinationScope != "杭州" || rt.Requirement.StartCity != "上海" {
			t.Fatalf("runtime was not reset into new requirement: %#v", rt.Requirement)
		}
	})

	t.Run("false keeps current runtime", func(t *testing.T) {
		ag := &scriptedAgent{outputs: []string{`{"new_planning_intent":false}`}}
		orchestrator := NewTravelSkillOrchestrator()
		orchestrator.LoadOrInitRuntime("user-keep", "session-keep")
		orchestrator.updateRuntime("user-keep", "session-keep", func(rt *TravelSkillRuntime) {
			rt.CurrentStage = StageMacroPlanning
			rt.Requirement = TravelRequirementSnapshot{
				DestinationScope: "云南",
				TotalDays:        7,
				StartCity:        "北京",
				RequirementReady: true,
			}
		})

		result, err := orchestrator.Handle(context.Background(), "user-keep", "session-keep", "能不能把节奏放慢一点", ag)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if result == nil || result.NextStage != StageMacroPlanning || len(ag.prompts) != 1 {
			t.Fatalf("expected classifier only and existing runtime, result=%#v prompts=%d", result, len(ag.prompts))
		}
		rt := orchestrator.LoadOrInitRuntime("user-keep", "session-keep")
		if rt.Requirement.DestinationScope != "云南" || rt.Requirement.StartCity != "北京" {
			t.Fatalf("runtime should be preserved: %#v", rt.Requirement)
		}
	})
}
