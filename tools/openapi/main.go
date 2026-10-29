// Command openapi derives a reviewed, minimal provider schema from vendored HF
// inputs. It never downloads a schema except in the explicit update command.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Operation struct {
	Path   string `json:"path"`
	Method string `json:"method"`
	ID     string `json:"id"`
	Source string `json:"source"`
}
type Action struct {
	Target string         `yaml:"target"`
	Update map[string]any `yaml:"update"`
}
type Overlay struct {
	Version    int      `yaml:"version"`
	Provenance string   `yaml:"provenance"`
	Actions    []Action `yaml:"actions"`
}

var methods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true, "head": true, "options": true, "trace": true}

func main() {
	input := flag.String("input", "openapi", "directory containing vendored inputs")
	output := flag.String("output", "openapi/provider.json", "derived schema destination")
	update := flag.Bool("update", false, "fetch a candidate and diff for manual review; never overwrite upstream")
	flag.Parse()
	var err error
	if *update {
		err = updateCandidate(*input, "https://huggingface.co/.well-known/openapi.json", http.DefaultClient)
	} else {
		err = generate(*input, *output)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func jsonBytes(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
func clone(v map[string]any) map[string]any {
	b, _ := json.Marshal(v)
	var result map[string]any
	_ = json.Unmarshal(b, &result)
	return result
}
func object(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object, got %T", v)
	}
	return m, nil
}

// pointer supports escaped JSON Pointer tokens and array indexes. References are
// checked locally before the generator sees them; unresolved/remote refs fail.
func pointer(document any, path string) (any, error) {
	if path == "" {
		return document, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q", path)
	}
	node := document
	for _, token := range strings.Split(path[1:], "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch n := node.(type) {
		case map[string]any:
			var ok bool
			node, ok = n[token]
			if !ok {
				return nil, fmt.Errorf("unmatched target %q", path)
			}
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(n) {
				return nil, fmt.Errorf("unmatched target %q", path)
			}
			node = n[i]
		default:
			return nil, fmt.Errorf("unmatched target %q", path)
		}
	}
	return node, nil
}

func build(source map[string]any, operations []Operation, overlay Overlay) (map[string]any, error) {
	full := clone(source)
	for _, action := range overlay.Actions {
		target, err := pointer(full, action.Target)
		if err != nil {
			return nil, err
		}
		targetObject, err := object(target)
		if err != nil {
			return nil, fmt.Errorf("overlay target %s: %w", action.Target, err)
		}
		for key, value := range action.Update {
			if _, exists := targetObject[key]; exists {
				return nil, fmt.Errorf("overlay target %s already contains %s", action.Target, key)
			}
			targetObject[key] = value
		}
	}
	paths, err := object(full["paths"])
	if err != nil {
		return nil, err
	}
	originalPaths, _ := object(source["paths"])
	result := map[string]any{}
	// Preserve metadata/attribution/security. Drop unselected paths/components only.
	for key, value := range full {
		if key != "paths" && key != "components" {
			result[key] = value
		}
	}
	selected := map[string]any{}
	result["paths"] = selected
	components := map[string]any{}
	result["components"] = components
	ids, seen := map[string]bool{}, map[string]bool{}
	for _, operation := range operations {
		key := operation.Method + " " + operation.Path
		if !methods[operation.Method] || operation.ID == "" || ids[operation.ID] || seen[key] {
			return nil, fmt.Errorf("invalid or duplicate allowlist operation %s (%s)", key, operation.ID)
		}
		if operation.Source != "upstream" && operation.Source != "overlay" {
			return nil, fmt.Errorf("operation %s lacks explicit source", key)
		}
		ids[operation.ID], seen[key] = true, true
		pathItem, err := object(paths[operation.Path])
		if err != nil {
			return nil, fmt.Errorf("allowlist path %s missing", operation.Path)
		}
		upstreamItem, _ := object(originalPaths[operation.Path])
		_, inUpstream := upstreamItem[operation.Method]
		if (operation.Source == "upstream") != inUpstream {
			return nil, fmt.Errorf("allowlist source mismatch for %s", key)
		}
		op, err := object(pathItem[operation.Method])
		if err != nil {
			return nil, fmt.Errorf("allowlist operation %s missing", key)
		}
		selectedItem, _ := selected[operation.Path].(map[string]any)
		if selectedItem == nil {
			selectedItem = map[string]any{}
			selected[operation.Path] = selectedItem
			for field, value := range pathItem {
				if !methods[field] {
					selectedItem[field] = value
				}
			}
		}
		op["operationId"] = operation.ID
		selectedItem[operation.Method] = op
	}
	// Security Scheme references are names, not $ref strings.
	if err := walk(result, func(m map[string]any) error {
		if security, ok := m["security"].([]any); ok {
			for _, entry := range security {
				for name := range entry.(map[string]any) {
					value, err := pointer(full, "/components/securitySchemes/"+name)
					if err != nil {
						return err
					}
					addComponent(components, "securitySchemes", name, value)
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	pending := []string{}
	if err := collectRefs(result, &pending); err != nil {
		return nil, err
	}
	visited := map[string]bool{}
	for len(pending) > 0 {
		ref := pending[0]
		pending = pending[1:]
		if visited[ref] {
			continue
		}
		visited[ref] = true
		parts := strings.Split(ref, "/")
		if len(parts) < 4 || parts[0] != "#" || parts[1] != "components" {
			return nil, fmt.Errorf("unsupported non-component reference %q", ref)
		}
		if _, err := pointer(full, strings.TrimPrefix(ref, "#")); err != nil {
			return nil, fmt.Errorf("reference %s: %w", ref, err)
		}
		// Copy the whole component, including when a ref points to a child schema.
		rootRef := strings.Join(parts[:4], "/")
		value, err := pointer(full, strings.TrimPrefix(rootRef, "#"))
		if err != nil {
			return nil, err
		}
		name := strings.ReplaceAll(strings.ReplaceAll(parts[3], "~1", "/"), "~0", "~")
		addComponent(components, parts[2], name, value)
		if err := collectRefs(value, &pending); err != nil {
			return nil, err
		}
	}
	// Hoisting the complete inline create schema is reversible and leaves every
	// oneOf/const/type:null constraint intact. The named alias retains JSON methods.
	if create, ok := selected["/api/repos/create"].(map[string]any); ok && create["post"] != nil {
		mediaValue, err := pointer(create["post"], "/requestBody/content/application~1json")
		if err != nil {
			return nil, err
		}
		media, _ := object(mediaValue)
		schemas, _ := components["schemas"].(map[string]any)
		if schemas != nil && schemas["CreateRepoRequest"] != nil {
			return nil, errors.New("hoist component CreateRepoRequest already exists")
		}
		addComponent(components, "schemas", "CreateRepoRequest", media["schema"])
		media["schema"] = map[string]any{"$ref": "#/components/schemas/CreateRepoRequest"}
	}
	return result, nil
}

func addComponent(components map[string]any, category, name string, value any) {
	group, _ := components[category].(map[string]any)
	if group == nil {
		group = map[string]any{}
		components[category] = group
	}
	group[name] = value
}
func walk(value any, visit func(map[string]any) error) error {
	switch v := value.(type) {
	case map[string]any:
		if err := visit(v); err != nil {
			return err
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := walk(v[key], visit); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := walk(child, visit); err != nil {
				return err
			}
		}
	}
	return nil
}
func collectRefs(value any, refs *[]string) error {
	return walk(value, func(m map[string]any) error {
		if raw, ok := m["$ref"]; ok {
			ref, ok := raw.(string)
			if !ok || !strings.HasPrefix(ref, "#/") {
				return fmt.Errorf("remote or invalid reference %v is forbidden", raw)
			}
			*refs = append(*refs, ref)
		}
		return nil
	})
}

func generate(input, output string) error {
	raw, err := os.ReadFile(filepath.Join(input, "upstream.json"))
	if err != nil {
		return err
	}
	var provenance struct {
		SHA256 string `json:"downloaded_schema_sha256"`
	}
	meta, err := os.ReadFile(filepath.Join(input, "provenance.json"))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(meta, &provenance); err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if provenance.SHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("upstream checksum differs from provenance; review candidate manually")
	}
	var source map[string]any
	if err = json.Unmarshal(raw, &source); err != nil {
		return err
	}
	var operations []Operation
	raw, err = os.ReadFile(filepath.Join(input, "operations.json"))
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&operations); err != nil {
		return err
	}
	var overlay Overlay
	raw, err = os.ReadFile(filepath.Join(input, "overlay.yaml"))
	if err != nil {
		return err
	}
	y := yaml.NewDecoder(bytes.NewReader(raw))
	y.KnownFields(true)
	if err = y.Decode(&overlay); err != nil {
		return err
	}
	if overlay.Version != 1 || overlay.Provenance == "" {
		return errors.New("overlay requires version 1 and provenance")
	}
	if _, err = os.Stat(filepath.Join(input, overlay.Provenance)); err != nil {
		return fmt.Errorf("overlay provenance: %w", err)
	}
	derived, err := build(source, operations, overlay)
	if err != nil {
		return err
	}
	derived["x-provider-generation"] = map[string]any{
		"notice":          "Generated by tools/openapi from unmodified Hugging Face upstream plus reviewed local overlay; do not edit.",
		"source":          "https://huggingface.co/.well-known/openapi.json",
		"upstream-sha256": provenance.SHA256,
		"transforms":      []string{"explicit allowlist operation IDs", "transitive local component closure", "lossless CreateRepoRequest hoist"},
	}
	encoded, err := jsonBytes(derived)
	if err != nil {
		return err
	}
	return os.WriteFile(output, encoded, 0644)
}

// updateCandidate writes only review artifacts. Approval and replacement of the
// vendored source/provenance/overlay are separate manual changes.
func updateCandidate(input, url string, client *http.Client) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// Bound redirects/time/body independently of provider HTTP behavior.
	local := *client
	local.Timeout = 30 * time.Second
	response, err := local.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("candidate schema HTTP %d", response.StatusCode)
	}
	const maxBytes = 16 << 20
	candidate, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if len(candidate) > maxBytes {
		return errors.New("candidate exceeds 16 MiB")
	}
	var parsed map[string]any
	if err = json.Unmarshal(candidate, &parsed); err != nil {
		return err
	}
	if parsed["openapi"] != "3.1.0" {
		return fmt.Errorf("candidate OpenAPI version %v requires manual tooling review", parsed["openapi"])
	}
	original, err := os.ReadFile(filepath.Join(input, "upstream.json"))
	if err != nil {
		return err
	}
	filename := filepath.Join(input, "candidate.json")
	if err = os.WriteFile(filename, candidate, 0644); err != nil {
		return err
	}
	oldDigest, newDigest := sha256.Sum256(original), sha256.Sum256(candidate)
	metadata, err := jsonBytes(map[string]any{"upstream_url": url, "retrieved_at": time.Now().UTC().Format(time.RFC3339), "bytes": len(candidate), "candidate_sha256": hex.EncodeToString(newDigest[:]), "vendored_sha256": hex.EncodeToString(oldDigest[:]), "status": "candidate only; manual review required; upstream/provenance unchanged"})
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(input, "candidate-provenance.json"), metadata, 0644); err != nil {
		return err
	}
	command := exec.Command("git", "diff", "--no-index", "--", filepath.Join(input, "upstream.json"), filename)
	diff, err := command.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return fmt.Errorf("candidate diff: %w: %s", err, diff)
	}
	return os.WriteFile(filepath.Join(input, "candidate.diff"), diff, 0644)
}
