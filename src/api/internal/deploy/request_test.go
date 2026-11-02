package deploy

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
)

func requestWithParts(t *testing.T, parts map[string]string) (*httptest.ResponseRecorder, error, stackFiles) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for name, value := range parts {
		part, err := w.CreateFormField(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte(value))
	}
	_ = w.Close()
	r := httptest.NewRequest("POST", "/deploy", &b)
	r.Header.Set("Content-Type", w.FormDataContentType())
	out := httptest.NewRecorder()
	_, files, err := parseRequest(out, r)
	return out, err, files
}
func TestMultipartValidationAndSecretsOverride(t *testing.T) {
	parts := map[string]string{"forearm": `{"name":"app","node":"worker"}`, "compose": "services:\n  hello:\n    image: nginx\n", "env": `{"VALUE":"original","SLASH":"a\\b"}`, "secrets": `{"VALUE":"secret'word"}`}
	_, err, files := requestWithParts(t, parts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files.dotenv), `VALUE='secret\'word'`) || !strings.Contains(string(files.dotenv), `SLASH='a\b'`) {
		t.Fatalf("incorrect environment rendering: %q", files.dotenv)
	}
	for _, invalid := range []string{`{"BAD":null}`, `{"BAD":{}}`, `{"BAD":[]}`, `{"BAD-NAME":"x"}`, `{"BAD":"nul\u0000byte"}`} {
		parts["env"] = invalid
		_, err, _ = requestWithParts(t, parts)
		if err == nil {
			t.Fatalf("accepted invalid env %s", invalid)
		}
	}
}

func TestScalarEnvironmentValues(t *testing.T) {
	parts := map[string]string{
		"forearm": `{"name":"app","node":"worker"}`,
		"compose": "services:\n  hello:\n    image: nginx\n",
		"env":     `{"INTEGER":30,"DECIMAL":1.25,"LARGE":12345678901234567890,"TRUE":true,"FALSE":false}`,
		"secrets": `{"INTEGER":42}`,
	}
	_, err, files := requestWithParts(t, parts)
	if err != nil {
		t.Fatal(err)
	}
	want := "DECIMAL='1.25'\nFALSE='false'\nINTEGER='42'\nLARGE='12345678901234567890'\nTRUE='true'\n"
	if got := string(files.dotenv); got != want {
		t.Fatalf("incorrect scalar environment: got %q, want %q", got, want)
	}
}

func TestMultilineEnvironment(t *testing.T) {
	parts := map[string]string{
		"forearm": `{"name":"app","node":"worker"}`,
		"compose": "services:\n  hello:\n    image: nginx\n",
		"secrets": `{"KEY":"first\r\nsecond\nquote'and\\slash"}`,
	}
	_, err, files := requestWithParts(t, parts)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(files.dotenv), "KEY='first\r\nsecond\nquote\\'and\\slash'\n"; got != want {
		t.Fatalf("incorrect multiline environment: got %q, want %q", got, want)
	}
}

func TestStackWithoutEnvironment(t *testing.T) {
	parts := map[string]string{"forearm": `{"name":"app","node":"worker"}`, "compose": "services:\n  hello:\n    image: nginx\n"}
	_, err, files := requestWithParts(t, parts)
	if err != nil || len(files.dotenv) != 0 {
		t.Fatalf("empty environment: %q, %v", files.dotenv, err)
	}
	parts["secrets"] = `{"TOKEN":"value"}`
	_, err, files = requestWithParts(t, parts)
	if err != nil || string(files.dotenv) != "TOKEN='value'\n" {
		t.Fatalf("secrets-only environment: %q, %v", files.dotenv, err)
	}
	parts["env"] = ""
	_, err, _ = requestWithParts(t, parts)
	if err == nil {
		t.Fatal("accepted an empty env part")
	}
}
