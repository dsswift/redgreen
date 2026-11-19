package services

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Connection is one host a service's config files say it reaches, in one
// environment. It holds names only: the host, the database, and the settings
// that named them. A value, a password, or a key never leaves the file it
// was read from; an App Insights instrumentation key is the one exception,
// since it is an identifier, not a secret.
type Connection struct {
	// Service is the ID of the service whose folder holds the file.
	Service     string `json:"service"`
	Environment string `json:"environment"`
	Kind        string `json:"kind"`
	Host        string `json:"host,omitempty"`
	Database    string `json:"database,omitempty"`
	// AppInsightsKey stands where a host would for kind appinsights.
	AppInsightsKey string `json:"appInsightsKey,omitempty"`
	// Settings are the names of the settings that named it, never their values.
	Settings []string `json:"settings"`
}

// Connection kinds.
const (
	SQL         = "sql"
	Cosmos      = "cosmos"
	Postgres    = "postgres"
	MySQL       = "mysql"
	Storage     = "storage"
	ServiceBus  = "servicebus"
	EventHub    = "eventhub"
	Redis       = "redis"
	HTTP        = "http"
	KeyVault    = "keyvault"
	AppConfig   = "appconfig"
	Search      = "search"
	AppInsights = "appinsights"
)

// ConnectionKinds are every kind a connection may be.
var ConnectionKinds = []string{SQL, Cosmos, Postgres, MySQL, Storage, ServiceBus, EventHub, Redis, HTTP, KeyVault, AppConfig, Search, AppInsights}

// UnknownEnvironment is the environment of a file whose name names none.
const UnknownEnvironment = "unknown"

// environments maps a word in a file name to the environment it names.
var environments = map[string]string{
	"production": "Prod", "prod": "Prod", "prd": "Prod",
	"staging": "Staging", "stage": "Staging", "stg": "Staging",
	"test": "Test", "testing": "Test", "tst": "Test",
	"development": "Dev", "develop": "Dev", "dev": "Dev",
	"preprod": "Preprod", "preproduction": "Preprod",
	"uat": "Uat",
	"qa":  "Qa",
}

// configFormat is how a config file is read.
type configFormat int

const (
	notConfig configFormat = iota
	jsonConfig
	xmlConfig
	envConfig
)

// formatOf says whether a file is a config file connections are read from, and how.
func formatOf(file string) configFormat {
	name := strings.ToLower(path.Base(file))
	switch {
	case strings.HasPrefix(name, "appsettings") && strings.HasSuffix(name, ".json"), name == "local.settings.json":
		return jsonConfig
	case strings.HasPrefix(name, "web") && strings.HasSuffix(name, ".config"), name == "applicationinsights.config":
		return xmlConfig
	case strings.HasSuffix(name, ".env.example"):
		return envConfig
	}
	return notConfig
}

// EnvironmentOf is the environment a config file's name names: a word between
// its dots, such as appsettings.Production.json or Web.Prod.config.
func EnvironmentOf(file string) string {
	segments := strings.Split(strings.ToLower(path.Base(file)), ".")
	for _, s := range segments {
		if env, ok := environments[strings.NewReplacer("-", "", "_", "").Replace(s)]; ok {
			return env
		}
	}
	for _, s := range segments {
		for _, w := range strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' }) {
			if env, ok := environments[w]; ok {
				return env
			}
		}
	}
	return UnknownEnvironment
}

// connect reads the config files in each service's own folder and keeps the
// hosts they name. A file that cannot be parsed is a problem, not an error.
func (f *Found) connect(paths []string, read Reader) {
	type at struct{ service, env, host, database string }
	merged := map[at]*Connection{}
	var order []at
	for _, s := range f.Services {
		if s.Type == Infrastructure || s.Type == Library {
			continue
		}
		dir := s.Path
		if dir == "" {
			dir = "."
		}
		for _, file := range paths {
			format := formatOf(file)
			if format == notConfig || path.Dir(file) != dir {
				continue
			}
			text, ok := read(file)
			if !ok {
				continue
			}
			settings, err := settingsOf(format, []byte(text))
			if err != nil {
				f.Problems = append(f.Problems, file+": "+err.Error())
			}
			env := EnvironmentOf(file)
			for _, st := range settings {
				for _, r := range reach(st.name, st.value) {
					k := at{s.ID, env, r.Host + r.AppInsightsKey, strings.ToLower(r.Database)}
					c := merged[k]
					if c == nil {
						c = &Connection{Service: s.ID, Environment: env, Kind: r.Kind, Host: r.Host, Database: r.Database, AppInsightsKey: r.AppInsightsKey}
						merged[k] = c
						order = append(order, k)
					}
					if !slices.Contains(c.Settings, st.name) {
						c.Settings = append(c.Settings, st.name)
					}
				}
			}
		}
	}
	for _, k := range order {
		c := merged[k]
		slices.Sort(c.Settings)
		f.Connections = append(f.Connections, *c)
	}
	slices.SortFunc(f.Connections, func(a, b Connection) int {
		return strings.Compare(a.Service+"\x00"+a.Environment+"\x00"+a.Host+a.AppInsightsKey+"\x00"+a.Database,
			b.Service+"\x00"+b.Environment+"\x00"+b.Host+b.AppInsightsKey+"\x00"+b.Database)
	})
}

// setting is one name and value from a config file. The value lives only
// while the file is read.
type setting struct{ name, value string }

func settingsOf(format configFormat, text []byte) ([]setting, error) {
	switch format {
	case jsonConfig:
		return jsonSettings(text)
	case xmlConfig:
		return xmlSettings(text)
	case envConfig:
		return envSettings(text), nil
	}
	return nil, nil
}

// jsonSettings walks a .NET settings file, comments and trailing commas
// allowed, and names each string by its path, as .NET does: ConnectionStrings:Main.
func jsonSettings(text []byte) ([]setting, error) {
	var doc any
	if err := json.Unmarshal(relaxJSON(text), &doc); err != nil {
		return nil, errors.New("not JSON: " + err.Error())
	}
	var out []setting
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				name := k
				if prefix != "" {
					name = prefix + ":" + k
				}
				walk(name, x[k])
			}
		case []any:
			for i, e := range x {
				walk(prefix+":"+strconv.Itoa(i), e)
			}
		case string:
			out = append(out, setting{prefix, x})
		}
	}
	walk("", doc)
	return out, nil
}

// relaxJSON drops the comments and trailing commas .NET accepts in a settings file.
func relaxJSON(in []byte) []byte {
	out := make([]byte, 0, len(in))
	inString, escaped := false, false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(in) && in[i+1] == '/':
			for i < len(in) && in[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(in) && in[i+1] == '*':
			i += 2
			for i+1 < len(in) && (in[i] != '*' || in[i+1] != '/') {
				i++
			}
			i++
		case c == '}' || c == ']':
			trimmed := bytes.TrimRight(out, " \t\r\n")
			if len(trimmed) > 0 && trimmed[len(trimmed)-1] == ',' {
				out = trimmed[:len(trimmed)-1]
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return bytes.TrimPrefix(out, []byte("\xef\xbb\xbf"))
}

// xmlSettings reads a web.config, a transform of one, or ApplicationInsights.config:
// every <add> with a key or name and a value or connection string, every
// client <endpoint> address, every <setting><value>, and the
// InstrumentationKey and ConnectionString elements.
func xmlSettings(text []byte) ([]setting, error) {
	dec := xml.NewDecoder(bytes.NewReader(text))
	dec.Strict = false
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	var out []setting
	var stack []string
	settingName := ""
	attr := func(e xml.StartElement, names ...string) string {
		for _, n := range names {
			for _, a := range e.Attr {
				if strings.EqualFold(a.Name.Local, n) && a.Name.Space != "xmlns" {
					return a.Value
				}
			}
		}
		return ""
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, errors.New("not XML: " + err.Error())
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			stack = append(stack, name)
			switch name {
			case "add":
				if key := attr(t, "key", "name"); key != "" {
					if v := attr(t, "value", "connectionString"); v != "" {
						out = append(out, setting{key, v})
					}
				}
			case "endpoint":
				if v := attr(t, "address"); v != "" {
					out = append(out, setting{orElse(attr(t, "name", "contract"), "endpoint"), v})
				}
			case "setting":
				settingName = attr(t, "name")
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) == 0 {
				continue
			}
			v := strings.TrimSpace(string(t))
			if v == "" {
				continue
			}
			switch here := stack[len(stack)-1]; {
			case here == "instrumentationkey":
				out = append(out, setting{"InstrumentationKey", v})
			case here == "connectionstring":
				out = append(out, setting{"ConnectionString", v})
			case here == "value" && len(stack) > 1 && stack[len(stack)-2] == "setting" && settingName != "":
				out = append(out, setting{settingName, v})
			}
		}
	}
}

func orElse(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// envSettings reads KEY=value lines.
func envSettings(text []byte) []setting {
	var out []setting
	for _, line := range strings.Split(string(text), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		out = append(out, setting{strings.TrimSpace(k), v})
	}
	return out
}
