package bdpwire

import (
	"encoding/json"
	"testing"
)

func TestErasedPointerPresenceAcrossCodecsAndValidation(t *testing.T) {
	for _, value := range []string{`null`, `false`, `0`, `""`, `"beads/secret"`, `{}`, `[]`} {
		t.Run(value, func(t *testing.T) {
			p := NewReadProblem(CodeResourceErased)
			p.Extensions = map[string]json.RawMessage{"pointer": json.RawMessage(value)}
			if p.Validate() == nil {
				t.Fatal("Validate allowed pointer")
			}
			if _, err := json.Marshal(p); err == nil {
				t.Fatal("marshal allowed pointer")
			}
			raw := `{"type":"` + p.Type + `","code":"resource-erased","retry":"never","pointer":` + value + `}`
			var a, b ReadProblem
			if err := Unmarshal([]byte(raw), &a); err == nil {
				t.Fatal("strict decode allowed pointer")
			}
			if err := json.Unmarshal([]byte(raw), &b); err == nil {
				t.Fatal("JSON decode allowed pointer")
			}
		})
	}
	for _, code := range []ReadProblemCode{CodeResourceErased, CodeResourceNotFound} {
		p := NewReadProblem(code)
		p.Extensions = map[string]json.RawMessage{"supportTicket": json.RawMessage(`{"id":42}`)}
		if code != CodeResourceErased {
			p.Extensions["pointer"] = json.RawMessage(`null`)
		}
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var got ReadProblem
		if err := Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if string(got.Extensions["supportTicket"]) != `{"id":42}` {
			t.Fatal("ordinary extension lost")
		}
		if code != CodeResourceErased {
			if _, exists := got.Extensions["pointer"]; !exists {
				t.Fatal("other problem's extension lost")
			}
		}
	}
}

func TestErasedPointerGuardIsBoundToPinnedSchema(t *testing.T) {
	problem := asMap(t, loadBundleDefs(t)["readProblem"], "readProblem")
	if value, present := problem["additionalProperties"]; present && value != true {
		t.Fatal("ordinary RFC 9457 extensions must remain open")
	}
	guards := 0
	for _, value := range asSlice(t, problem["allOf"], "allOf") {
		branch := asMap(t, value, "branch")
		then := asMapOrEmpty(t, branch["then"])
		props := asMapOrEmpty(t, then["properties"])
		if props["pointer"] != false {
			continue
		}
		condition := asMap(t, branch["if"], "if")
		code := asMap(t, asMap(t, condition["properties"], "properties")["code"], "code")
		if code["const"] != string(CodeResourceErased) {
			t.Fatal("pointer guard applied to a different condition")
		}
		required := stringSet(t, asSlice(t, condition["required"], "required"))
		if !required["code"] {
			t.Fatal("pointer condition must require its code")
		}
		guards++
	}
	if guards != 1 {
		t.Fatalf("got %d explicit erased pointer guards, want one", guards)
	}
}
