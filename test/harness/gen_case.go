//go:build ignore
// +build ignore

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// RequestSpec mirrors the harness CaseSpec.Request
type RequestSpec struct {
	Method  string                 `json:"method" yaml:"method"`
	Path    string                 `json:"path" yaml:"path"`
	Headers map[string]string      `json:"headers,omitempty" yaml:"headers,omitempty"`
	Body    map[string]interface{} `json:"body,omitempty" yaml:"body,omitempty"`
}

// ExpectSpec mirrors the harness CaseSpec.Expect
type ExpectSpec struct {
	Status int                    `json:"status" yaml:"status"`
	Body   map[string]interface{} `json:"body,omitempty" yaml:"body,omitempty"`
}

// CaseYAML is the structure we'll write to case.yml
type CaseYAML struct {
	Name    string      `yaml:"name"`
	Request RequestSpec `yaml:"request"`
	Expect  ExpectSpec  `yaml:"expect"`
}

func readMaybeFileOrJSON(raw string) ([]byte, error) {
	if raw == "" {
		return nil, nil
	}
	// If starts with @ treat as file path
	if strings.HasPrefix(raw, "@") {
		p := strings.TrimPrefix(raw, "@")
		b, err := ioutil.ReadFile(p)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	// Otherwise treat as JSON string
	return []byte(raw), nil
}

func parseRequest(raw string) (RequestSpec, error) {
	if raw == "" {
		// default stub: POST
		return RequestSpec{Method: "POST"}, nil
	}
	b, err := readMaybeFileOrJSON(raw)
	if err != nil {
		return RequestSpec{}, err
	}
	var r RequestSpec
	if err := json.Unmarshal(b, &r); err != nil {
		// try YAML as fallback
		if err2 := yaml.Unmarshal(b, &r); err2 == nil {
			return r, nil
		}
		return RequestSpec{}, fmt.Errorf("failed to parse request spec: %v (json err) / %v (yaml err)", err, err2)
	}
	return r, nil
}

func parseExpect(raw string) (ExpectSpec, error) {
	if raw == "" {
		return ExpectSpec{Status: 200}, nil
	}
	b, err := readMaybeFileOrJSON(raw)
	if err != nil {
		return ExpectSpec{}, err
	}
	var e ExpectSpec
	if err := json.Unmarshal(b, &e); err != nil {
		if err2 := yaml.Unmarshal(b, &e); err2 == nil {
			return e, nil
		}
		return ExpectSpec{}, fmt.Errorf("failed to parse expect spec: %v (json err) / %v (yaml err)", err, err2)
	}
	return e, nil
}

// findHandlerFile searches .go files for a function matching handlerName (methodName)
// supports methods with receiver: func (s *Server) handleLogin(c *gin.Context)
// returns the file path and the function body as string
func findHandlerFile(handlerName string) (string, string, error) {
	var files []string
	_ = filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})

	fnRegexes := []*regexp.Regexp{
		regexp.MustCompile(`func\s+\(.*\)\s*` + regexp.QuoteMeta(handlerName) + `\s*\(`),
		regexp.MustCompile(`func\s+` + regexp.QuoteMeta(handlerName) + `\s*\(`),
	}

	for _, f := range files {
		b, _ := ioutil.ReadFile(f)
		s := string(b)
		for _, rex := range fnRegexes {
			loc := rex.FindStringIndex(s)
			if loc != nil {
				// find opening brace after this location
				open := strings.Index(s[loc[1]:], "{")
				if open == -1 {
					continue
				}
				start := loc[1] + open
				// now find matching closing brace
				count := 0
				end := -1
				for i := start; i < len(s); i++ {
					if s[i] == '{' {
						count++
					} else if s[i] == '}' {
						count--
						if count == 0 {
							end = i
							break
						}
					}
				}
				if end != -1 {
					body := s[start+1 : end]
					return f, body, nil
				}
			}
		}
	}
	return "", "", fmt.Errorf("handler %s not found", handlerName)
}

// extractRequestFields tries to find a ShouldBindJSON(&var) and resolve the struct fields
func extractRequestFields(funcBody, fileDir string) map[string]interface{} {
	out := map[string]interface{}{}
	// find ShouldBindJSON(&varName) or BindJSON(&varName)
	rex := regexp.MustCompile(`ShouldBindJSON\s*\(\s*&\s*([A-Za-z0-9_]+)\s*\)|BindJSON\s*\(\s*&\s*([A-Za-z0-9_]+)\s*\)|ShouldBind\s*\(\s*&\s*([A-Za-z0-9_]+)\s*\)`)
	m := rex.FindStringSubmatch(funcBody)
	var varName string
	for i := 1; i < len(m); i++ {
		if m[i] != "" {
			varName = m[i]
			break
		}
	}
	if varName == "" {
		return out
	}
	// search for 'var varName <Type>' or 'varName := <Type>{' or 'varName := <Type>' in function body
	// pattern: var <varName> <Type>
	varTypeR := regexp.MustCompile(`var\s+` + regexp.QuoteMeta(varName) + `\s+([A-Za-z0-9_]+)`)
	if mm := varTypeR.FindStringSubmatch(funcBody); len(mm) >= 2 {
		typeName := mm[1]
		// search for type definition in repository
		if fields := findStructFields(typeName); len(fields) > 0 {
			for k := range fields {
				out[k] = ""
			}
		}
		return out
	}
	// look for '<varName> := <Type>{' inline literal
	litR := regexp.MustCompile(regexp.QuoteMeta(varName) + `\s*:=\s*struct\s*\{([^}]*)\}`)
	if mm := litR.FindStringSubmatch(funcBody); len(mm) >= 2 {
		body := mm[1]
		fields := parseStructFieldsFromBody(body)
		for k := range fields {
			out[k] = ""
		}
		return out
	}
	// case: varName := Type{} (no inline fields) -> try to find type
	shortR := regexp.MustCompile(regexp.QuoteMeta(varName) + `\s*:?=\s*([A-Za-z0-9_]+)`)
	if mm := shortR.FindStringSubmatch(funcBody); len(mm) >= 2 {
		typeName := mm[1]
		if fields := findStructFields(typeName); len(fields) > 0 {
			for k := range fields {
				out[k] = ""
			}
		}
	}
	return out
}

// findStructFields looks for 'type TypeName struct { ... }' across repository and returns map of json field names
func findStructFields(typeName string) map[string]string {
	res := map[string]string{}
	_ = filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, _ := ioutil.ReadFile(path)
		s := string(b)
		rex := regexp.MustCompile(`type\s+` + regexp.QuoteMeta(typeName) + `\s+struct\s*\{([\s\S]*?)\}`)
		if mm := rex.FindStringSubmatch(s); len(mm) >= 2 {
			body := mm[1]
			fields := parseStructFieldsFromBody(body)
			for k, v := range fields {
				res[k] = v
			}
		}
		return nil
	})
	return res
}

// parseStructFieldsFromBody parses field lines like 'Email string `json:"email"`' and returns map json->type
func parseStructFieldsFromBody(body string) map[string]string {
	out := map[string]string{}
	s := bufio.NewScanner(strings.NewReader(body))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		// split by spaces
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		// last part may include tag
		tag := ""
		if strings.Contains(line, "`json:") {
			// extract json tag
			re := regexp.MustCompile(`json:"([^\"]+)"`)
			if mm := re.FindStringSubmatch(line); len(mm) >= 2 {
				tag = mm[1]
			}
		}
		varName := parts[0]
		if tag == "" {
			// fallback to lowercased field name
			tag = strings.ToLower(varName)
		}
		out[tag] = parts[1]
	}
	return out
}

// extractResponseFields tries to heuristically find c.JSON(..., gin.H{ ... }) and extract keys
func extractResponseFields(funcBody string) map[string]interface{} {
	out := map[string]interface{}{}
	// find gin.H{ ... }
	re := regexp.MustCompile(`gin\.H\s*\{([\s\S]*?)}`)
	if mm := re.FindStringSubmatch(funcBody); len(mm) >= 2 {
		inside := mm[1]
		// find keys like "key": or key:
		reKey := regexp.MustCompile(`(?m)(?:"([^"]+)"|([a-zA-Z0-9_]+))\s*:\s*`)
		ks := reKey.FindAllStringSubmatch(inside, -1)
		for _, k := range ks {
			if k[1] != "" {
				out[k[1]] = ""
			} else if k[2] != "" {
				out[k[2]] = ""
			}
		}
		return out
	}
	// fallback: look for c.JSON with struct variable -> not implemented
	return out
}

// findRouteForHandler attempts to find the registered route path for a handler
// by scanning source files for router registration calls like api.POST("/login", s.handleLogin)
func findRouteForHandler(handlerName string) string {
	verbs := []string{"POST", "GET", "PUT", "DELETE", "PATCH", "Any"}
	// scan all .go files line-by-line
	_ = filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := ioutil.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			for _, v := range verbs {
				// look for verb usage, e.g. POST("/api/xxx",
				if strings.Contains(line, v+"(") || strings.Contains(line, strings.ToLower(v)+"(") {
					// try extract quoted path in the line
					rePath := regexp.MustCompile(`"([^"]+)"`)
					if mm := rePath.FindStringSubmatch(line); len(mm) >= 2 {
						pathStr := mm[1]
						// if handlerName appears in same line or within next 3 lines, consider it a match
						if strings.Contains(line, handlerName) {
							returnVal := pathStr
							// write returnVal to a temp file marker and later read it outside. Simpler: set an env var — but we are in same process; instead, panic with a sentinel containing path, and recover outside. But that's messy. Simpler: create a package-level global variable routeFound and set it here.
							routeFound = returnVal
							return fmt.Errorf("stopwalk")
						}
						// look ahead up to 3 lines for handlerName
						for j := i; j <= i+3 && j < len(lines); j++ {
							if strings.Contains(lines[j], handlerName) {
								routeFound = pathStr
								return fmt.Errorf("stopwalk")
							}
						}
					}
				}
			}
		}
		return nil
	})
	// if walk was stopped with our sentinel, routeFound will be set
	return routeFound
}

// package-level variable to capture found route inside filepath.Walk
var routeFound string

func main() {
	var reqRaw string
	var expectRaw string
	flag.StringVar(&reqRaw, "req", "", "request spec JSON or @file")
	flag.StringVar(&expectRaw, "expect", "", "expect spec JSON or @file")
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		fmt.Println("usage: go run test/harness/gen_case.go [flags] <methodName> [caseName]")
		flag.PrintDefaults()
		os.Exit(1)
	}
	method := args[0]
	caseName := "case01"
	if len(args) >= 2 {
		caseName = args[1]
	}

	baseDir := filepath.Join("test", method)
	caseDir := filepath.Join(baseDir, caseName)
	prepDir := filepath.Join(caseDir, "PrepareData")
	checkDir := filepath.Join(caseDir, "CheckData")

	// create directories (PrepareData/CheckData will be empty unless user provides CSVs)
	if err := os.MkdirAll(prepDir, 0o755); err != nil {
		fmt.Printf("failed to create PrepareData dir: %v\n", err)
		os.Exit(2)
	}
	if err := os.MkdirAll(checkDir, 0o755); err != nil {
		fmt.Printf("failed to create CheckData dir: %v\n", err)
		os.Exit(3)
	}

	// try to locate handler and extract fields heuristically
	hFile, hBody, _ := findHandlerFile(method)
	var reqFields map[string]interface{}
	var respFields map[string]interface{}
	if hFile != "" {
		reqFields = extractRequestFields(hBody, filepath.Dir(hFile))
		respFields = extractResponseFields(hBody)
	}

	// parse flags and fallbacks
	rSpec, err := parseRequest(reqRaw)
	if err != nil {
		fmt.Printf("parse request spec failed: %v\n", err)
		os.Exit(4)
	}
	eSpec, err := parseExpect(expectRaw)
	if err != nil {
		fmt.Printf("parse expect spec failed: %v\n", err)
		os.Exit(5)
	}

	// merge detected fields into request/expect bodies if they are empty
	if (rSpec.Body == nil || len(rSpec.Body) == 0) && len(reqFields) > 0 {
		rSpec.Body = reqFields
	}
	if (eSpec.Body == nil || len(eSpec.Body) == 0) && len(respFields) > 0 {
		eSpec.Body = respFields
	}

	// attempt to detect registered route for handler and use it (overrides default path)
	if rSpec.Path == "" {
		if route := findRouteForHandler(method); route != "" {
			rSpec.Path = route
		}
	}

	// fill defaults if missing
	if rSpec.Path == "" {
		rSpec.Path = "/api/" + method
	}
	if rSpec.Method == "" {
		rSpec.Method = "POST"
	}
	if eSpec.Status == 0 {
		eSpec.Status = 200
	}

	// generate case.yml dynamically
	cy := CaseYAML{
		Name:    method + " " + caseName,
		Request: rSpec,
		Expect:  eSpec,
	}
	b, err := yaml.Marshal(&cy)
	if err != nil {
		fmt.Printf("yaml marshal failed: %v\n", err)
		os.Exit(6)
	}
	caseYmlPath := filepath.Join(caseDir, "case.yml")
	if err := ioutil.WriteFile(caseYmlPath, b, 0o644); err != nil {
		fmt.Printf("write case.yml failed: %v\n", err)
		os.Exit(7)
	}

	// generate test file (non-hardcoded, but simple harness wrapper)
	testFile := filepath.Join(baseDir, fmt.Sprintf("%s_test.go", method))
	testContent := fmt.Sprintf(`// @Target(%s)
package %s

import (
    "testing"

    "nofx/test/harness"
)

// %sTest 嵌入 BaseTest，可按需重写 Before/After 钩子
type %sTest struct {
    harness.BaseTest
}

func (rt *%sTest) Before(t *testing.T) {
    rt.BaseTest.Before(t)
    if rt.Env != nil {
        t.Logf("TestEnv API URL: %s", rt.Env.URL())
    } else {
        t.Log("Warning: Env is nil in Before")
    }
}

func (rt *%sTest) After(t *testing.T) {
    // no-op
}

// @RunWith(%s)
func Test%s(t *testing.T) {
    rt := &%sTest{}
    harness.RunCase(t, rt)
}
`, method, method, strings.Title(method), strings.Title(method), strings.Title(method), strings.Title(method), caseName, strings.Title(method), strings.Title(method))
	if err := ioutil.WriteFile(testFile, []byte(testContent), 0o644); err != nil {
		fmt.Printf("write test file failed: %v\n", err)
		os.Exit(8)
	}

	fmt.Printf("generated test scaffolding for method '%s' at %s\n", method, baseDir)
	fmt.Printf("case.yml path: %s\n", caseYmlPath)
}
