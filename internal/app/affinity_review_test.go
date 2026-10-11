package app

import (
	"net/http"
	"testing"
)

func TestSessionKeyWithoutConversationIsEmpty(t *testing.T) {
	for _, body := range []string{`{}`, `{"input":[]}`, `{"instructions":"system only"}`, `{"messages":[{"role":"system","content":"system only"}]}`} {
		if key := sessionKeyOf([]byte(body), nil); key != "" {
			t.Fatalf("body=%s key=%s", body, key)
		}
	}
}

func TestResponsesConversationAffinity(t *testing.T) {
	one := `{"instructions":"help","input":"first conversation"}`
	two := `{"instructions":"help","input":"second conversation"}`
	first := sessionKeyOf([]byte(one), nil)
	if first == "" || first == sessionKeyOf([]byte(two), nil) {
		t.Fatal("Responses conversations collapsed to one binding")
	}
	roundOne := `{"instructions":"help","input":[{"role":"user","content":"first conversation"}]}`
	roundTwo := `{"instructions":"help","input":[{"role":"user","content":"first conversation"},{"role":"assistant","content":"reply"},{"role":"user","content":"follow up"}]}`
	if sessionKeyOf([]byte(roundOne), nil) != first || sessionKeyOf([]byte(roundTwo), nil) != first {
		t.Fatal("Responses follow-ups changed binding")
	}
}

func TestSessionKeyIncludesChatSystemAndHonorsExplicitSession(t *testing.T) {
	a := []byte(`{"messages":[{"role":"system","content":"a"},{"role":"user","content":"same"}]}`)
	b := []byte(`{"messages":[{"role":"system","content":"b"},{"role":"user","content":"same"}]}`)
	if sessionKeyOf(a, nil) == sessionKeyOf(b, nil) {
		t.Fatal("different system contexts collapsed")
	}
	header := http.Header{"X-Session-Id": []string{"explicit"}}
	if sessionKeyOf(a, header) != "s:explicit" || sessionKeyOf(b, header) != "s:explicit" {
		t.Fatal("explicit session must win")
	}
}
