package rpc

import (
	"encoding/json"
	"testing"
)

func TestRPCReqUnmarshal(t *testing.T) {
	var r request
	if err := json.Unmarshal([]byte(`{"id":7,"type":"turn","text":"hi"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Type != "turn" || r.Text != "hi" || string(r.ID) != "7" {
		t.Errorf("request = %+v", r)
	}
}
