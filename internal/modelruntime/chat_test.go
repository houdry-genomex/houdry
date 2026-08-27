package modelruntime

import "testing"

func TestNormalizeToolCallsFromText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"raw json", `{"name":"get_weather","arguments":{"city":"Paris"}}`, "get_weather"},
		{"fenced", "```json\n{\"name\":\"run\",\"arguments\":{\"cmd\":\"ls\"}}\n```", "run"},
		{"xml", `<tool_call>{"name":"write_file","arguments":{"path":"a.py"}}</tool_call>`, "write_file"},
		{"openai shape", `{"type":"function","function":{"name":"exec","arguments":"{\"x\":1}"}}`, "exec"},
		{"plain prose", "I will create fibonacci.py next.", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeToolCallsFromText(tc.in)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("expected none, got %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Function.Name != tc.want {
				t.Fatalf("got %+v want %s", got, tc.want)
			}
			if got[0].Function.Arguments == "" {
				t.Fatal("empty arguments")
			}
		})
	}
}

func TestOllamaToolCallsToOpenAI(t *testing.T) {
	raw := []byte(`[{"function":{"name":"get_weather","arguments":{"city":"Paris"}}}]`)
	got := OllamaToolCallsToOpenAI(raw)
	if len(got) != 1 || got[0].Function.Name != "get_weather" {
		t.Fatalf("%+v", got)
	}
	if got[0].Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("args=%q", got[0].Function.Arguments)
	}
}
