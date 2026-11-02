package deploy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type stackFiles struct{ compose, dotenv []byte }

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func decodeForearm(body []byte) (Forearm, error) {
	var f Forearm
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return f, fmt.Errorf("invalid forearm JSON: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return f, errors.New("expected one JSON object")
	}
	return f, nil
}
func parseRequest(w http.ResponseWriter, r *http.Request) (Forearm, stackFiles, error) {
	var files stackFiles
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil && r.Header.Get("Content-Type") != "" {
		return Forearm{}, files, err
	}
	if media != "multipart/form-data" {
		if media != "" && media != "application/json" {
			return Forearm{}, files, errors.New("expected application/json or multipart/form-data")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return Forearm{}, files, err
		}
		f, err := decodeForearm(body)
		if err == nil && f.Name != "" {
			err = errors.New("stack deployment requires multipart forearm, compose and env parts")
		}
		return f, files, err
	}
	reader, err := r.MultipartReader()
	if err != nil {
		return Forearm{}, files, err
	}
	parts := map[string][]byte{}
	for {
		p, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return Forearm{}, files, e
		}
		name := p.FormName()
		if name != "forearm" && name != "compose" && name != "env" && name != "secrets" {
			p.Close()
			return Forearm{}, files, fmt.Errorf("unknown part %q", name)
		}
		if _, ok := parts[name]; ok {
			p.Close()
			return Forearm{}, files, fmt.Errorf("duplicate part %q", name)
		}
		data, e := io.ReadAll(p)
		p.Close()
		if e != nil {
			return Forearm{}, files, e
		}
		parts[name] = data
	}
	f, err := decodeForearm(parts["forearm"])
	if err != nil {
		return f, files, err
	}
	if f.Name == "" {
		if _, ok := parts["compose"]; ok {
			return f, files, errors.New("compose requires a stack")
		}
		if _, ok := parts["env"]; ok {
			return f, files, errors.New("env requires a stack")
		}
		if _, ok := parts["secrets"]; ok {
			return f, files, errors.New("secrets require a stack")
		}
		return f, files, nil
	}
	if len(parts["compose"]) == 0 {
		return f, files, errors.New("stack deployment requires a compose part")
	}
	var doc struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err = yaml.Unmarshal(parts["compose"], &doc); err != nil || len(doc.Services) == 0 {
		return f, files, errors.New("compose must be valid YAML with at least one service")
	}
	parseEnv := func(b []byte) (map[string]string, error) {
		var raw map[string]json.RawMessage
		d := json.NewDecoder(bytes.NewReader(b))
		if err := d.Decode(&raw); err != nil {
			return nil, err
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			return nil, errors.New("expected one JSON object")
		}
		if raw == nil {
			return nil, errors.New("expected JSON object")
		}
		values := make(map[string]string, len(raw))
		for k, data := range raw {
			var v string
			if err := json.Unmarshal(data, &v); err != nil || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
				var scalar any
				decoder := json.NewDecoder(bytes.NewReader(data))
				decoder.UseNumber()
				if err := decoder.Decode(&scalar); err != nil {
					return nil, fmt.Errorf("environment variable %q must be a string, number or boolean", k)
				}
				switch value := scalar.(type) {
				case json.Number:
					v = value.String()
				case bool:
					v = fmt.Sprint(value)
				default:
					return nil, fmt.Errorf("environment variable %q must be a string, number or boolean", k)
				}
			}
			if !envKey.MatchString(k) || strings.ContainsRune(v, '\x00') {
				return nil, fmt.Errorf("invalid environment variable %q", k)
			}
			values[k] = v
		}
		return values, nil
	}
	env := map[string]string{}
	if b, ok := parts["env"]; ok {
		env, err = parseEnv(b)
		if err != nil {
			return f, files, fmt.Errorf("invalid env JSON: %w", err)
		}
	}
	if b, ok := parts["secrets"]; ok {
		secrets, e := parseEnv(b)
		if e != nil {
			return f, files, fmt.Errorf("invalid secrets JSON: %w", e)
		}
		for k, v := range secrets {
			env[k] = v
		}
	}
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	var out strings.Builder
	for _, k := range names {
		v := strings.ReplaceAll(env[k], "'", `\'`)
		fmt.Fprintf(&out, "%s='%s'\n", k, v)
	}
	files = stackFiles{compose: parts["compose"], dotenv: []byte(out.String())}
	return f, files, nil
}
