// Package plugin lets blocks be added without recompiling Sentinel. A plugin block is a folder
// with a block.yaml manifest and an executable (any language). Sentinel runs it as a child
// process: one JSON request on stdin, one JSON reply on stdout. A block is loaded only after a
// person approved its exact contents (see trust.go), so an agent can propose a block but a
// scheduled run never executes code nobody reviewed.
package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"sentinel/internal/engine"
)

const (
	ManifestFile   = "block.yaml"
	ProtocolNumber = 1
	defaultTimeout = 30
	maxTimeout     = 3600
)

var (
	nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)*$`)
	typeRe = regexp.MustCompile(`^(string|number|bool|list|map|any)\??$`)
	envRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Manifest is block.yaml.
type Manifest struct {
	Name        string            `yaml:"name"`
	Doc         string            `yaml:"doc"`
	Effect      engine.Effect     `yaml:"effect"`  // read | write-internal | write-external
	Inputs      map[string]string `yaml:"inputs"`  // name -> type; "?" suffix = optional
	Outputs     map[string]string `yaml:"outputs"` // name -> type
	Run         []string          `yaml:"run"`     // command and arguments, run inside the block folder
	Timeout     int               `yaml:"timeout"` // seconds, default 30
	Env         []string          `yaml:"env"`     // environment variables the block may read (secrets); nothing else is passed
	SupportsDry bool              `yaml:"supports_dry"`
	Tests       []Test            `yaml:"tests"`
}

// Test is one fixture run by `sentinel block check`. A block that changes things is only ever
// tested with dry: true.
type Test struct {
	Name  string         `yaml:"name"`
	In    map[string]any `yaml:"in"`
	Dry   bool           `yaml:"dry"`
	Out   map[string]any `yaml:"out"`   // every listed output must equal this (others are ignored)
	Error string         `yaml:"error"` // the block must fail with a message containing this
}

// ParseManifest reads and validates dir/block.yaml.
func ParseManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", ManifestFile, err)
	}
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if errs := m.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("%s: %s", ManifestFile, strings.Join(errs, "; "))
	}
	if filepath.Base(dir) != m.Name {
		return nil, fmt.Errorf("folder %q must be named after the block (%q)", filepath.Base(dir), m.Name)
	}
	return &m, nil
}

// Validate lists every problem with the manifest.
func (m *Manifest) Validate() []string {
	var errs []string
	if !nameRe.MatchString(m.Name) {
		errs = append(errs, "name must look like Http.Get (letters, digits, _ and dots)")
	}
	switch m.Effect {
	case engine.Read, engine.WriteInternal, engine.WriteExternal:
	default:
		errs = append(errs, "effect must be read, write-internal or write-external")
	}
	if strings.TrimSpace(m.Doc) == "" {
		errs = append(errs, "doc is required: say what the block does in a sentence or two")
	}
	if len(m.Run) == 0 || m.Run[0] == "" {
		errs = append(errs, "run is required, for example [python3, block.py]")
	}
	if m.Timeout < 0 || m.Timeout > maxTimeout {
		errs = append(errs, fmt.Sprintf("timeout must be between 1 and %d seconds", maxTimeout))
	}
	for k, t := range m.Inputs {
		if !typeRe.MatchString(t) || k == "" || strings.HasPrefix(k, "_") {
			errs = append(errs, fmt.Sprintf("input %q: type %q is not string, number, bool, list, map or any (add ? if optional); names cannot start with _", k, t))
		}
	}
	for k, t := range m.Outputs {
		if !typeRe.MatchString(t) || strings.HasSuffix(t, "?") || k == "" || strings.HasPrefix(k, "_") {
			errs = append(errs, fmt.Sprintf("output %q: type %q is not string, number, bool, list, map or any; names cannot start with _", k, t))
		}
	}
	for _, e := range m.Env {
		if !envRe.MatchString(e) {
			errs = append(errs, fmt.Sprintf("env %q is not a variable name", e))
		}
	}
	for i, t := range m.Tests {
		if t.Out != nil && t.Error != "" {
			errs = append(errs, fmt.Sprintf("test %d: use out or error, not both", i+1))
		}
		if m.Effect != engine.Read && !t.Dry {
			errs = append(errs, fmt.Sprintf("test %d: a block that changes things may only be tested with dry: true", i+1))
		}
	}
	return errs
}

func (m *Manifest) timeout() int {
	if m.Timeout <= 0 {
		return defaultTimeout
	}
	return m.Timeout
}

// Spec is the engine's view of the block.
func (m *Manifest) Spec() engine.Spec {
	in, out := map[string]string{}, map[string]string{}
	for k, v := range m.Inputs {
		in[k] = v
	}
	for k, v := range m.Outputs {
		out[k] = v
	}
	return engine.Spec{Name: m.Name, Effect: m.Effect, Inputs: in, Outputs: out, Doc: strings.TrimSpace(m.Doc), Source: "external"}
}
