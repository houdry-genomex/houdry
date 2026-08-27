package openaicompat

import "testing"

func TestMessagesToPromptAndAuto(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: raw(`"You are helpful."`)},
		{Role: "user", Content: raw(`"Say hello"`)},
	}
	p := MessagesToPrompt(msgs)
	if p == "" || LastUserText(msgs) != "Say hello" {
		t.Fatalf("prompt=%q last=%q", p, LastUserText(msgs))
	}
	if !IsAutoModel("auto") || !IsAutoModel("") || IsAutoModel("tinyllama") {
		t.Fatal("auto detection")
	}
	if err := ValidateChatRequest(ChatCompletionRequest{Messages: nil}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestBuildCompletion(t *testing.T) {
	r := BuildCompletion("chatcmpl-1", "tinyllama:latest", "hi", 3, 2)
	if r.Object != ObjectChatCompletion || len(r.Choices) != 1 {
		t.Fatalf("%+v", r)
	}
	if r.Usage.TotalTokens != 5 {
		t.Fatalf("usage=%+v", r.Usage)
	}
	if r.Choices[0].Message.ContentString() != "hi" {
		t.Fatalf("content=%s", r.Choices[0].Message.ContentString())
	}
}

func raw(s string) []byte { return []byte(s) }
