package herdrcli

import (
	"context"
	"testing"
)

func TestAgentSessionIsOptional(t *testing.T) {
	for _, field := range []string{"", `,"agent_session":{"agent":"codex","kind":"id","source":"herdr:codex","value":"session-123"}`} {
		f := newFake(t, `{"result":{"agent":{"name":"test"`+field+`}}}`, "", 0)
		ag, err := f.client().AgentGet(context.Background(), "test")
		if err != nil {
			t.Fatal(err)
		}
		if field == "" {
			if ag.AgentSession != nil {
				t.Fatal("older Herdr invented a session")
			}
		} else if ag.AgentSession == nil || *ag.AgentSession != (AgentSession{Agent: "codex", Kind: "id", Source: "herdr:codex", Value: "session-123"}) {
			t.Fatalf("session was dropped: %+v", ag)
		}
	}
}
