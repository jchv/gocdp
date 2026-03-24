package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CDPSchema is the top-level structure of a CDP schema document.
type CDPSchema struct {
	Version struct {
		Major string `json:"major"`
		Minor string `json:"minor"`
	} `json:"version"`
	Domains []Domain `json:"domains"`
}

// Domain contains the specification for a single CDP domain.
type Domain struct {
	Domain       string    `json:"domain"`
	Description  string    `json:"description"`
	Experimental bool      `json:"experimental"`
	Dependencies []string  `json:"dependencies"`
	Types        []Type    `json:"types"`
	Commands     []Command `json:"commands"`
	Events       []Event   `json:"events"`
}

// Type contains type definitions within a schema.
type Type struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	Type        string     `json:"type"`
	Items       *Items     `json:"items"`
	Enum        []string   `json:"enum"`
	Properties  []Property `json:"properties"`
}

// Items is the type of items within an array.
type Items struct {
	Type string `json:"type"`
	Ref  string `json:"$ref"`
}

// Property is the type of a property within an object.
type Property struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Type         string   `json:"type"`
	Ref          string   `json:"$ref"`
	Enum         []string `json:"enum"`
	Items        *Items   `json:"items"`
	Optional     bool     `json:"optional"`
	Experimental bool     `json:"experimental"`
	Deprecated   bool     `json:"deprecated"`
}

// Command contains the schema for an individual CDP command.
type Command struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Experimental bool       `json:"experimental"`
	Deprecated   bool       `json:"deprecated"`
	Parameters   []Property `json:"parameters"`
	Returns      []Property `json:"returns"`
}

// Event contains the schema for an individual CDP event.
type Event struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Deprecated   bool       `json:"deprecated"`
	Experimental bool       `json:"experimental"`
	Parameters   []Property `json:"parameters"`
}

func getFile(filename string, update bool) (data []byte, err error) {
	path := filepath.Join("cmd", "cdpgen", filename)
	if !update {
		if data, err := os.ReadFile(path); err == nil {
			return data, nil
		}
	}
	url := fmt.Sprintf("https://raw.githubusercontent.com/ChromeDevTools/devtools-protocol/master/json/%s", filename)
	slog.Info("Downloading file", slog.String("url", url))
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err2 := resp.Body.Close(); err2 != nil && err == nil {
			data = nil
			err = err2
		}
	}()
	data, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	err = os.WriteFile(path, data, 0644)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func title(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func snakeToTitle(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		parts[i] = title(p)
	}
	return strings.Join(parts, "")
}

func goName(s string) string {
	if s == "type" {
		return "Type"
	}
	return title(s)
}

func safeName(s string) string {
	switch s {
	case "type", "range", "func", "interface", "select", "case", "defer", "go", "map", "struct", "chan", "else", "goto", "package", "switch", "const", "fallthrough", "if", "continue", "for", "import", "return", "var", "default", "break":
		return s + "_"
	}
	return s
}

func fixSpec(domains []Domain) {
	for x := range domains {
		domain := &domains[x]
		switch domain.Domain {
		case "Network":
			for i := range domain.Types {
				t := &domain.Types[i]
				if t.ID == "Cookie" {
					for j := range t.Properties {
						// This adaptation is from nodriver. `expires` can be nil if it is not representable in JSON (e.g. Infinity)
						if t.Properties[j].Name == "expires" {
							t.Properties[j].Optional = true
						}
					}
				}
			}
		}
	}
}

func goType(domain Domain, typeName, ref string, items *Items, optional bool) string {
	var t string
	if ref != "" {
		parts := strings.Split(ref, ".")
		if len(parts) == 2 {
			t = goName(parts[0]) + goName(parts[1])
		} else {
			t = goName(domain.Domain) + goName(parts[0])
		}
	} else if items != nil {
		t = "[]" + goType(domain, items.Type, items.Ref, nil, false)
	} else {
		switch typeName {
		case "string":
			t = "string"
		case "integer":
			t = "int64"
		case "number":
			t = "float64"
		case "boolean":
			t = "bool"
		case "object":
			t = "map[string]any"
		case "any":
			t = "any"
		case "array":
			t = "[]any"
		default:
			t = "any"
		}
	}

	if optional && !strings.HasPrefix(t, "[]") && !strings.HasPrefix(t, "map[") && t != "any" {
		return "*" + t
	}
	return t
}

func comment(desc string) string {
	if desc == "" {
		return ""
	}
	lines := strings.Split(desc, "\n")
	var res strings.Builder
	for _, l := range lines {
		res.WriteString("// " + l + "\n")
	}
	return res.String()
}

func generateDomain(domain Domain, outDir string) error {
	buf := &bytes.Buffer{}
	buf.WriteString("// Code generated by cdpgen; DO NOT EDIT.\n\n")
	buf.WriteString("package cdp\n\n")

	// Types
	for _, t := range domain.Types {
		generateType(domain, t, buf)
	}

	// Commands
	for _, c := range domain.Commands {
		generateCommand(domain, c, buf)
	}

	// Events
	for _, e := range domain.Events {
		generateEvent(domain, e, buf)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		slog.Error("ERROR formatting domain", slog.String("domain", domain.Domain), slog.Any("err", err))
		return os.WriteFile(filepath.Join(outDir, strings.ToLower(domain.Domain)+".go"), buf.Bytes(), 0644)
	}

	slog.Info("Writing domain", slog.String("domain", domain.Domain), slog.String("path", filepath.Join(outDir, strings.ToLower(domain.Domain)+".go")))
	return os.WriteFile(filepath.Join(outDir, strings.ToLower(domain.Domain)+".go"), formatted, 0644)
}

func generateType(domain Domain, t Type, buf *bytes.Buffer) {
	buf.WriteString(comment(t.Description))
	name := goName(domain.Domain) + goName(t.ID)

	if len(t.Enum) > 0 {
		fmt.Fprintf(buf, "type %s string\n\n", name)
		buf.WriteString("const (\n")
		for _, e := range t.Enum {
			// make valid go identifier
			safeVal := strings.ReplaceAll(e, "-", "_")
			fmt.Fprintf(buf, "\t%s%s %s = %q\n", name, snakeToTitle(safeVal), name, e)
		}
		buf.WriteString(")\n\n")
	} else if len(t.Properties) > 0 {
		fmt.Fprintf(buf, "type %s struct {\n", name)
		for _, p := range t.Properties {
			pName := goName(p.Name)
			if pName == name {
				pName = pName + "_" // avoid field matching struct name
			}
			buf.WriteString(comment(p.Description))
			omit := ""
			if p.Optional {
				omit = ",omitempty"
			}
			typ := goType(domain, p.Type, p.Ref, p.Items, p.Optional)
			fmt.Fprintf(buf, "\t%s %s `json:\"%s%s\"`\n", pName, typ, p.Name, omit)
		}
		buf.WriteString("}\n\n")
	} else {
		typ := goType(domain, t.Type, "", t.Items, false)
		fmt.Fprintf(buf, "type %s %s\n\n", name, typ)
	}
}

func generateCommand(domain Domain, c Command, buf *bytes.Buffer) {
	cmdName := goName(domain.Domain) + goName(c.Name)
	buf.WriteString(comment(c.Description))
	fmt.Fprintf(buf, "type %sParams struct {\n", cmdName)
	for _, p := range c.Parameters {
		pName := goName(p.Name)
		omit := ""
		if p.Optional {
			omit = ",omitempty"
		}
		typ := goType(domain, p.Type, p.Ref, p.Items, p.Optional)
		buf.WriteString(comment(p.Description))
		fmt.Fprintf(buf, "\t%s %s `json:\"%s%s\"`\n", pName, typ, p.Name, omit)
	}
	buf.WriteString("}\n\n")

	if len(c.Returns) > 0 {
		fmt.Fprintf(buf, "type %sReturns struct {\n", cmdName)
		for _, p := range c.Returns {
			pName := goName(p.Name)
			omit := ""
			if p.Optional {
				omit = ",omitempty"
			}
			typ := goType(domain, p.Type, p.Ref, p.Items, p.Optional)
			buf.WriteString(comment(p.Description))
			fmt.Fprintf(buf, "\t%s %s `json:\"%s%s\"`\n", pName, typ, p.Name, omit)
		}
		buf.WriteString("}\n\n")
	}

	// Constructor
	var reqArgs []string
	var reqAssign []string
	for _, p := range c.Parameters {
		if !p.Optional {
			typ := goType(domain, p.Type, p.Ref, p.Items, false)
			reqArgs = append(reqArgs, fmt.Sprintf("%s %s", safeName(p.Name), typ))
			reqAssign = append(reqAssign, fmt.Sprintf("%s: %s,", goName(p.Name), safeName(p.Name)))
		}
	}

	fmt.Fprintf(buf, "func %s(%s) *%sParams {\n", cmdName, strings.Join(reqArgs, ", "), cmdName)
	fmt.Fprintf(buf, "\treturn &%sParams{\n", cmdName)
	for _, s := range reqAssign {
		buf.WriteString("\t\t" + s + "\n")
	}
	buf.WriteString("\t}\n}\n\n")

	// Chain builder for optional parameters
	for _, p := range c.Parameters {
		if p.Optional {
			typ := goType(domain, p.Type, p.Ref, p.Items, p.Optional)
			valTyp := goType(domain, p.Type, p.Ref, p.Items, false)

			ptrAssign := fmt.Sprintf("p.%s = v", goName(p.Name))
			if strings.HasPrefix(typ, "*") {
				ptrAssign = fmt.Sprintf("p.%s = &v", goName(p.Name))
			}

			fmt.Fprintf(buf, "func (p *%sParams) With%s(v %s) *%sParams {\n", cmdName, goName(p.Name), valTyp, cmdName)
			fmt.Fprintf(buf, "\t%s\n", ptrAssign)
			buf.WriteString("\treturn p\n}\n\n")
		}
	}

	// Do method
	fmt.Fprintf(buf, "func (p *%sParams) CDPMethodName() string {\n", cmdName)
	fmt.Fprintf(buf, "\treturn \"%s.%s\"\n}\n\n", domain.Domain, c.Name)
}

func generateEvent(domain Domain, e Event, buf *bytes.Buffer) {
	eventName := goName(domain.Domain) + goName(e.Name) + "Event"
	eventConstant := "Event" + goName(domain.Domain) + goName(e.Name)

	buf.WriteString(comment(e.Description))
	fmt.Fprintf(buf, "const %s = \"%s.%s\"\n\n", eventConstant, domain.Domain, e.Name)
	fmt.Fprintf(buf, "type %s struct {\n", eventName)
	for _, p := range e.Parameters {
		pName := goName(p.Name)
		omit := ""
		if p.Optional {
			omit = ",omitempty"
		}
		typ := goType(domain, p.Type, p.Ref, p.Items, p.Optional)
		buf.WriteString(comment(p.Description))
		fmt.Fprintf(buf, "\t%s %s `json:\"%s%s\"`\n", pName, typ, p.Name, omit)
	}
	buf.WriteString("}\n\n")
	fmt.Fprintf(buf, "func (e *%s) CDPEventName() string {\n", eventName)
	fmt.Fprintf(buf, "\treturn %s\n}\n\n", eventConstant)
}

func main() {
	update := flag.Bool("update", false, "Force download of the latest CDP schema files")
	flag.Parse()

	browserData, err := getFile("browser_protocol.json", *update)
	if err != nil {
		slog.Error("Error getting browser_protocol.json", slog.Any("err", err))
		os.Exit(1)
	}
	jsData, err := getFile("js_protocol.json", *update)
	if err != nil {
		slog.Error("Error getting js_protocol.json", slog.Any("err", err))
		os.Exit(1)
	}

	var browserSchema CDPSchema
	if err := json.Unmarshal(browserData, &browserSchema); err != nil {
		slog.Error("Error parsing browser_protocol.json", slog.Any("err", err))
		os.Exit(1)
	}

	var jsSchema CDPSchema
	if err := json.Unmarshal(jsData, &jsSchema); err != nil {
		slog.Error("Error parsing js_protocol.json", slog.Any("err", err))
		os.Exit(1)
	}

	var allDomains []Domain
	allDomains = append(allDomains, browserSchema.Domains...)
	allDomains = append(allDomains, jsSchema.Domains...)

	// Sort domains to insure deterministic behavior
	sort.Slice(allDomains, func(i, j int) bool {
		return allDomains[i].Domain < allDomains[j].Domain
	})

	fixSpec(allDomains)

	outDir := filepath.Join("cdp")
	_ = os.RemoveAll(outDir)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		slog.Error("Error making output directory", slog.Any("err", err))
		os.Exit(1)
	}

	for _, domain := range allDomains {
		if err := generateDomain(domain, outDir); err != nil {
			slog.Error("Error generating domain", slog.String("domain", domain.Domain), slog.Any("err", err))
			os.Exit(1)
		}
	}
}
