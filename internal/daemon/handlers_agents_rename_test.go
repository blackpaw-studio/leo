package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

func TestHandleAgentRename(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "leo-old"}}}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(map[string]string{"new_name": "leo-new"})
	resp, err := client.Post("http://localhost/agents/leo-old/rename", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if mgr.lastRename.query != "leo-old" {
		t.Errorf("lastRename.query = %q, want leo-old", mgr.lastRename.query)
	}
	if mgr.lastRename.newName != "leo-new" {
		t.Errorf("lastRename.newName = %q, want leo-new", mgr.lastRename.newName)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK {
		t.Errorf("env.OK = false, want true")
	}
	var rec agent.Record
	if err := json.Unmarshal(env.Data, &rec); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if rec.Name != "leo-new" {
		t.Errorf("rec.Name = %q, want leo-new", rec.Name)
	}
}

func TestHandleAgentRename_MissingNewName(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "leo-old"}}}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(map[string]string{})
	resp, err := client.Post("http://localhost/agents/leo-old/rename", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleAgentRename_NotFound(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{}}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(map[string]string{"new_name": "leo-new"})
	resp, err := client.Post("http://localhost/agents/missing/rename", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandleAgentRename_NoManager(t *testing.T) {
	_, client := startTestServerWithAgent(t, nil)

	body, _ := json.Marshal(map[string]string{"new_name": "leo-new"})
	resp, err := client.Post("http://localhost/agents/leo-old/rename", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestHandleAgentRename_NameTaken(t *testing.T) {
	mgr := &fakeAgentManager{
		records:   []agent.Record{{Name: "leo-old"}},
		renameErr: agent.ErrAgentNameTaken,
	}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(map[string]string{"new_name": "leo-existing"})
	resp, err := client.Post("http://localhost/agents/leo-old/rename", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestHandleAgentRename_NameUnchanged(t *testing.T) {
	mgr := &fakeAgentManager{
		records:   []agent.Record{{Name: "leo-old"}},
		renameErr: agent.ErrAgentNameUnchanged,
	}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(map[string]string{"new_name": "leo-old"})
	resp, err := client.Post("http://localhost/agents/leo-old/rename", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleAgentRename_UndecodableBody(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "leo-old"}}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Post("http://localhost/agents/leo-old/rename", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
