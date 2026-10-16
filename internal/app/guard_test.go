package app

import (
	"testing"
)

func TestSandboxGuardModes(t *testing.T) {
	repo := t.TempDir()
	if v := sandboxGuard("steer", repo, "")("execute_code", "print(2)"); !v.Steer {
		t.Error("steer steers every server exec call")
	}
	if v := sandboxGuard("show", repo, "")("terminal", "make test"); v.Steer {
		t.Error("show never steers")
	}
	smart := sandboxGuard("smart", repo, "")
	if v := smart("terminal", "make test"); !v.Steer {
		t.Error("smart steers make")
	}
	if v := smart("execute_code", "print(2)"); v.Steer {
		t.Error("smart leaves a scratch computation alone")
	}
	if v := smart("tool_call", "Local Shell"); !v.Steer || v.Kind != "hands-tool" {
		t.Errorf("smart steers a hands tool called as Hermes' own: %+v", v)
	}
}
