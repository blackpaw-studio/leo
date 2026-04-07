package daemon

import (
	"encoding/json"
	"testing"
)

func TestResponseMarshalUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		response Response
	}{
		{
			name: "success with data",
			response: Response{
				OK:   true,
				Data: json.RawMessage(`{"task":"heartbeat"}`),
			},
		},
		{
			name: "success without data",
			response: Response{
				OK: true,
			},
		},
		{
			name: "error response",
			response: Response{
				OK:    false,
				Error: "invalid config",
			},
		},
		{
			name: "error with null data",
			response: Response{
				OK:    false,
				Data:  json.RawMessage("null"),
				Error: "task not found",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal to JSON
			data, err := json.Marshal(tt.response)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}

			// Unmarshal back
			var got Response
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			// Verify fields match
			if got.OK != tt.response.OK {
				t.Errorf("OK = %v, want %v", got.OK, tt.response.OK)
			}
			if got.Error != tt.response.Error {
				t.Errorf("Error = %q, want %q", got.Error, tt.response.Error)
			}
			// Compare raw JSON representations
			if string(got.Data) != string(tt.response.Data) {
				t.Errorf("Data = %s, want %s", got.Data, tt.response.Data)
			}
		})
	}
}

func TestCronRequestMarshalUnmarshal(t *testing.T) {
	tests := []struct {
		name    string
		request CronRequest
	}{
		{
			name: "basic cron request",
			request: CronRequest{
				ConfigPath: "/home/user/.leo/leo.yaml",
			},
		},
		{
			name: "cron request with relative path",
			request: CronRequest{
				ConfigPath: "leo.yaml",
			},
		},
		{
			name: "cron request with empty path",
			request: CronRequest{
				ConfigPath: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.request)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}

			var got CronRequest
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			if got.ConfigPath != tt.request.ConfigPath {
				t.Errorf("ConfigPath = %q, want %q", got.ConfigPath, tt.request.ConfigPath)
			}
		})
	}
}

func TestTaskAddRequestMarshalUnmarshal(t *testing.T) {
	tests := []struct {
		name    string
		request TaskAddRequest
	}{
		{
			name: "minimal task add request",
			request: TaskAddRequest{
				Name:       "heartbeat",
				Schedule:   "0 * * * *",
				PromptFile: "HEARTBEAT.md",
				Enabled:    true,
			},
		},
		{
			name: "full task add request",
			request: TaskAddRequest{
				Name:       "daily-news",
				Schedule:   "0 7 * * *",
				PromptFile: "reports/news.md",
				Model:      "opus",
				MaxTurns:   20,
				TopicID:    1,
				Silent:     true,
				Enabled:    true,
			},
		},
		{
			name: "disabled task",
			request: TaskAddRequest{
				Name:       "disabled",
				Schedule:   "0 0 * * *",
				PromptFile: "noop.md",
				Enabled:    false,
			},
		},
		{
			name: "task with zero max turns",
			request: TaskAddRequest{
				Name:       "test",
				Schedule:   "* * * * *",
				PromptFile: "test.md",
				MaxTurns:   0,
				Enabled:    true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.request)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}

			var got TaskAddRequest
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			if got.Name != tt.request.Name {
				t.Errorf("Name = %q, want %q", got.Name, tt.request.Name)
			}
			if got.Schedule != tt.request.Schedule {
				t.Errorf("Schedule = %q, want %q", got.Schedule, tt.request.Schedule)
			}
			if got.PromptFile != tt.request.PromptFile {
				t.Errorf("PromptFile = %q, want %q", got.PromptFile, tt.request.PromptFile)
			}
			if got.Model != tt.request.Model {
				t.Errorf("Model = %q, want %q", got.Model, tt.request.Model)
			}
			if got.MaxTurns != tt.request.MaxTurns {
				t.Errorf("MaxTurns = %d, want %d", got.MaxTurns, tt.request.MaxTurns)
			}
			if got.TopicID != tt.request.TopicID {
				t.Errorf("TopicID = %d, want %d", got.TopicID, tt.request.TopicID)
			}
			if got.Silent != tt.request.Silent {
				t.Errorf("Silent = %v, want %v", got.Silent, tt.request.Silent)
			}
			if got.Enabled != tt.request.Enabled {
				t.Errorf("Enabled = %v, want %v", got.Enabled, tt.request.Enabled)
			}
		})
	}
}

func TestTaskNameRequestMarshalUnmarshal(t *testing.T) {
	tests := []struct {
		name    string
		request TaskNameRequest
	}{
		{
			name: "remove task",
			request: TaskNameRequest{
				Name: "heartbeat",
			},
		},
		{
			name: "enable task",
			request: TaskNameRequest{
				Name: "daily-news",
			},
		},
		{
			name: "disable task",
			request: TaskNameRequest{
				Name: "background-sync",
			},
		},
		{
			name: "empty name",
			request: TaskNameRequest{
				Name: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.request)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}

			var got TaskNameRequest
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			if got.Name != tt.request.Name {
				t.Errorf("Name = %q, want %q", got.Name, tt.request.Name)
			}
		})
	}
}

func TestResponseErrorUnmarshal(t *testing.T) {
	jsonStr := `{"ok":false,"error":"daemon not running"}`
	var resp Response
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if resp.OK {
		t.Error("OK should be false")
	}
	if resp.Error != "daemon not running" {
		t.Errorf("Error = %q, want %q", resp.Error, "daemon not running")
	}
}

func TestResponseDataUnmarshal(t *testing.T) {
	jsonStr := `{"ok":true,"data":{"status":"ok","task":"heartbeat"}}`
	var resp Response
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if !resp.OK {
		t.Error("OK should be true")
	}

	// Verify data can be parsed
	var data map[string]interface{}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal data failed: %v", err)
	}

	if status, ok := data["status"].(string); !ok || status != "ok" {
		t.Errorf("status = %v, want 'ok'", status)
	}
	if task, ok := data["task"].(string); !ok || task != "heartbeat" {
		t.Errorf("task = %v, want 'heartbeat'", task)
	}
}

func TestTaskAddRequestOmittedFields(t *testing.T) {
	// Verify that omitted optional fields don't appear in JSON
	req := TaskAddRequest{
		Name:       "test",
		Schedule:   "0 * * * *",
		PromptFile: "test.md",
		Enabled:    true,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal to map failed: %v", err)
	}

	// Check that omitted fields are not present or are null
	if val, ok := m["model"]; ok && val != nil {
		t.Errorf("model should not be present, got %v", val)
	}
	if val, ok := m["max_turns"]; ok && val != nil {
		t.Errorf("max_turns should not be present, got %v", val)
	}
	if val, ok := m["topic_id"]; ok && val != nil {
		t.Errorf("topic_id should not be present, got %v", val)
	}
}

func TestResponseOmittedFields(t *testing.T) {
	// Success response without data or error
	resp := Response{
		OK: true,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal to map failed: %v", err)
	}

	if val, ok := m["data"]; ok && val != nil {
		t.Errorf("data should not be present, got %v", val)
	}
	if val, ok := m["error"]; ok && val != nil {
		t.Errorf("error should not be present, got %v", val)
	}
}
