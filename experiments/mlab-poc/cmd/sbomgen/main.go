// Command sbomgen generates a CycloneDX 1.5 JSON SBOM for the GoSpeed
// M-Lab experiment module by reading the real Go module graph (`go list -m
// -json all`) and the license files shipped in the module cache.
//
// The generator never invents dependency entries: every component comes from
// the actual build graph, and licenses that cannot be confirmed from the
// module files are reported as NOASSERTION instead of being guessed.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	sdkModulePath    = "github.com/m-lab/ndt7-client-go"
	sdkModuleVersion = "v0.10.1"
)

type goModule struct {
	Path     string `json:"Path"`
	Version  string `json:"Version"`
	Dir      string `json:"Dir"`
	Main     bool   `json:"Main"`
	Indirect bool   `json:"Indirect"`
	GoMod    string `json:"GoMod"`
}

type bom struct {
	BOMFormat    string      `json:"bomFormat"`
	SpecVersion  string      `json:"specVersion"`
	SerialNumber string      `json:"serialNumber"`
	Version      int         `json:"version"`
	Metadata     metadata    `json:"metadata"`
	Components   []component `json:"components"`
}

type metadata struct {
	Timestamp  string     `json:"timestamp"`
	Tools      []tool     `json:"tools"`
	Component  *component `json:"component,omitempty"`
	Properties []property `json:"properties,omitempty"`
}

type tool struct {
	Vendor  string `json:"vendor"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type component struct {
	Type        string          `json:"type"`
	BOMRef      string          `json:"bom-ref"`
	Name        string          `json:"name"`
	Version     string          `json:"version,omitempty"`
	Scope       string          `json:"scope,omitempty"`
	PURL        string          `json:"purl,omitempty"`
	Licenses    []licenseChoice `json:"licenses,omitempty"`
	Properties  []property      `json:"properties,omitempty"`
	Description string          `json:"description,omitempty"`
}

type licenseChoice struct {
	License *license `json:"license,omitempty"`
}

type license struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func main() {
	root := flag.String("root", ".", "Go module root to scan")
	output := flag.String("o", "", "output file (default stdout)")
	flag.Parse()

	modules, err := listModules(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	participating, err := listBuildParticipatingModules(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	document := buildBOM(modules, participating)
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: encode SBOM: %v\n", err)
		os.Exit(1)
	}
	data = append(data, '\n')
	if *output == "" {
		_, _ = os.Stdout.Write(data)
		return
	}
	if err := os.WriteFile(*output, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: write %s: %v\n", *output, err)
		os.Exit(1)
	}
}

func listModules(root string) ([]goModule, error) {
	cmd := exec.Command("go", "list", "-m", "-json", "all")
	cmd.Dir = mustAbs(root)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	decoder := json.NewDecoder(&stdout)
	var modules []goModule
	for {
		var module goModule
		if err := decoder.Decode(&module); err != nil {
			break
		}
		modules = append(modules, module)
	}
	if len(modules) == 0 {
		return nil, fmt.Errorf("go list returned no modules")
	}
	return modules, nil
}

// listBuildParticipatingModules returns the module paths of every package
// that actually participates in building ./... (go list -deps). The full
// `go list -m all` graph is larger: it also contains modules that only
// satisfy historical go.mod requirements but whose code is never compiled
// into this experiment.
func listBuildParticipatingModules(root string) (map[string]bool, error) {
	cmd := exec.Command("go", "list", "-deps", "-json", "./...")
	cmd.Dir = mustAbs(root)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list -deps: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	participating := map[string]bool{}
	decoder := json.NewDecoder(&stdout)
	for {
		var pkg struct {
			Module *goModule `json:"Module"`
		}
		if err := decoder.Decode(&pkg); err != nil {
			break
		}
		if pkg.Module != nil && pkg.Module.Path != "" {
			participating[pkg.Module.Path] = true
		}
	}
	return participating, nil
}

func buildBOM(modules []goModule, participating map[string]bool) bom {
	components := make([]component, 0, len(modules))
	var mainModule *goModule
	for i := range modules {
		if modules[i].Main {
			copied := modules[i]
			mainModule = &copied
			continue
		}
		components = append(components, moduleComponent(&modules[i], participating))
	}

	properties := []property{
		{Name: "gospeed:go-version", Value: runtime.Version()},
		{Name: "gospeed:sdk-pin", Value: sdkModulePath + "@" + sdkModuleVersion},
		{Name: "gospeed:license-method", Value: "license files shipped in each module directory; unconfirmed licenses are reported as NOASSERTION"},
		{Name: "gospeed:scope-method", Value: "scope=required marks modules whose code is compiled into this experiment (go list -deps ./...); scope=optional marks modules that only appear in the go list -m all requirement graph"},
	}

	document := bom{
		BOMFormat:    "CycloneDX",
		SpecVersion:  "1.5",
		SerialNumber: "urn:uuid:" + newUUIDv4(),
		Version:      1,
		Metadata: metadata{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Tools: []tool{{
				Vendor:  "GoSpeed",
				Name:    "experiments/mlab-poc/cmd/sbomgen",
				Version: "0.1.0-p0j",
			}},
			Properties: properties,
		},
		Components: components,
	}
	if mainModule != nil {
		ref := mainModule.Path
		version := mainModule.Version
		if version == "" {
			version = "(devel)"
		}
		document.Metadata.Component = &component{
			Type:        "application",
			BOMRef:      ref,
			Name:        ref,
			Version:     version,
			Description: "Part of the GoSpeed repository; licenses are inherited from the repository root.",
			Licenses:    licensesFromDirOrParents(mainModule.Dir, 3),
		}
	}
	return document
}

func moduleComponent(module *goModule, participating map[string]bool) component {
	class := "direct"
	if module.Indirect {
		class = "indirect"
	}
	scope := "optional"
	if participating[module.Path] {
		scope = "required"
	}
	return component{
		Type:     "library",
		BOMRef:   module.Path + "@" + module.Version,
		Name:     module.Path,
		Version:  module.Version,
		Scope:    scope,
		PURL:     "pkg:golang/" + module.Path + "@" + module.Version,
		Licenses: licensesFromDir(module.Dir),
		Properties: []property{
			{Name: "gospeed:dependency-class", Value: class},
		},
	}
}

// licensesFromDirOrParents walks up the directory tree looking for license
// files, so that a module without its own LICENSE can inherit the repository
// root license.
func licensesFromDirOrParents(dir string, maxDepth int) []licenseChoice {
	current := dir
	for depth := 0; depth <= maxDepth && current != ""; depth++ {
		if paths := licenseFilePaths(current); len(paths) > 0 {
			return licensesFromDir(current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return []licenseChoice{{License: &license{Name: "NOASSERTION"}}}
}

func licensesFromDir(dir string) []licenseChoice {
	paths := licenseFilePaths(dir)
	if len(paths) == 0 {
		return []licenseChoice{{License: &license{Name: "NOASSERTION"}}}
	}
	seen := map[string]bool{}
	var choices []licenseChoice
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		id := classifyLicense(string(data))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		choices = append(choices, licenseChoice{License: &license{ID: id}})
	}
	if len(choices) == 0 {
		return []licenseChoice{{License: &license{Name: "NOASSERTION"}}}
	}
	return choices
}

func licenseFilePaths(dir string) []string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToUpper(entry.Name())
		if strings.HasPrefix(name, "LICENSE") || strings.HasPrefix(name, "COPYING") ||
			strings.HasPrefix(name, "COPYRIGHT") || strings.HasPrefix(name, "NOTICE") {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	return paths
}

// classifyLicense identifies a license from its text. Only well-known, exact
// SPDX identifiers used by this dependency tree are recognized; anything else
// is reported as unknown so that NOASSERTION is emitted instead of a guess.
func classifyLicense(text string) string {
	const limit = 32 * 1024
	if len(text) > limit {
		text = text[:limit]
	}
	switch {
	case strings.Contains(text, "Apache License") && strings.Contains(text, "Version 2.0, January 2004"):
		return "Apache-2.0"
	case strings.Contains(text, "Permission is hereby granted, free of charge"):
		return "MIT"
	case strings.Contains(text, "Redistribution and use in source and binary forms") &&
		strings.Contains(text, "Neither the name"):
		return "BSD-3-Clause"
	case strings.Contains(text, "Redistribution and use in source and binary forms"):
		return "BSD-2-Clause"
	default:
		return ""
	}
}

func newUUIDv4() string {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		// A random serial number is cosmetic, not a security control; a
		// timestamp-based fallback keeps the SBOM valid if the CSPRNG fails.
		binary := time.Now().UnixNano()
		for i := range uuid {
			uuid[i] = byte(binary >> (uint(i%8) * 8))
		}
	}
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

func mustAbs(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absolute
}
