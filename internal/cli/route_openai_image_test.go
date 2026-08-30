package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A path typed into the message is the common way users refer to a drawing.
// If this stops working the turn silently degrades to a chat model, which
// fabricates a result rather than reporting that it cannot see the file.
func TestImagesFromPromptReadsLocalFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "part.jpg")
	want := []byte("\xff\xd8\xff\xe0 not really a jpeg")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	images := imagesFromPrompt("make a 3d model of " + path + " please")
	if len(images) != 1 {
		t.Fatalf("got %d images, want 1", len(images))
	}
	got, err := base64.StdEncoding.DecodeString(images[0])
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("payload = %q, want %q", got, want)
	}
}

func TestImagesFromPromptDeduplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "part.png")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := len(imagesFromPrompt(path + " and again " + path)); n != 1 {
		t.Errorf("got %d images, want 1 after dedup", n)
	}
}

// Only real, readable image files count. Anything else must be skipped rather
// than aborting the turn, so a prompt that merely mentions a filename still
// gets answered.
func TestImagesFromPromptSkipsNonImages(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(secret, []byte("API_KEY=hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "shots.png")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, prompt := range []string{
		"read " + secret,
		"convert to 3d " + filepath.Join(dir, "missing.jpg"),
		"look at " + subdir,
		"what is a .jpg file",
		"my model.png is nice", // relative, not an absolute path
	} {
		if got := imagesFromPrompt(prompt); len(got) != 0 {
			t.Errorf("imagesFromPrompt(%q) returned %d images, want 0", prompt, len(got))
		}
	}
}

// The pipeline is for dimensioned drawings; the refusal has to say so, or a
// user feeds it a photo and reads the meaningless part as a real answer.
func TestCADNeedsImageMessageSetsExpectations(t *testing.T) {
	for _, want := range []string{"2D engineering drawing", "Attach the image", "path"} {
		if !strings.Contains(cadNeedsImageMessage, want) {
			t.Errorf("message does not mention %q:\n%s", want, cadNeedsImageMessage)
		}
	}
}
