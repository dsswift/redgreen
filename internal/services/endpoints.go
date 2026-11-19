package services

import (
	"net/url"
	"regexp"
	"strings"
)

// endpoint is where one setting reaches: a host and database, or an App
// Insights instrumentation key. Nothing else of the value is kept.
type endpoint struct {
	Kind, Host, Database, AppInsightsKey string
}

var (
	hostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	guid     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	storage  = regexp.MustCompile(`\.(blob|queue|table|file|dfs|web)\.core\.windows\.net$`)
	loopback = map[string]bool{"localhost": true, "127.0.0.1": true, "0.0.0.0": true, "host.docker.internal": true}
)

// hostKinds are the kinds Azure's host names give away, by suffix.
var hostKinds = []struct{ suffix, kind string }{
	{".database.windows.net", SQL},
	{".postgres.database.azure.com", Postgres},
	{".mysql.database.azure.com", MySQL},
	{".documents.azure.com", Cosmos},
	{".cosmos.azure.com", Cosmos},
	{".servicebus.windows.net", ServiceBus},
	{".redis.cache.windows.net", Redis},
	{".redis.azure.net", Redis},
	{".redisenterprise.cache.azure.net", Redis},
	{".vault.azure.net", KeyVault},
	{".azconfig.io", AppConfig},
	{".search.windows.net", Search},
}

// kindOfHost is the kind an Azure host name gives away; "" for any other host.
func kindOfHost(host string) string {
	if storage.MatchString(host) {
		return Storage
	}
	for _, k := range hostKinds {
		if strings.HasSuffix(host, k.suffix) {
			return k.kind
		}
	}
	return ""
}

// cleanHost is a host as it is kept: lowercase, without a port, a SQL
// protocol prefix, or an instance name. "" when it is not a host name, or
// is this machine.
func cleanHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(strings.TrimPrefix(h, "tcp:"), "np:")
	if i := strings.IndexAny(h, `\,`); i >= 0 {
		h = h[:i]
	}
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[:i], ":") {
		h = h[:i]
	}
	if !hostName.MatchString(h) || loopback[h] || strings.HasPrefix(h, "127.") {
		return ""
	}
	return h
}

// reach is where one setting's value points. It reads URLs, connection
// strings, Key Vault references, Redis configuration strings, Azure host
// names, and App Insights instrumentation keys.
func reach(name, value string) []endpoint {
	v := strings.TrimSpace(value)
	lower := strings.ToLower(v)
	switch {
	case v == "":
		return nil
	case strings.HasPrefix(lower, "@microsoft.keyvault("):
		return vaultReference(strings.TrimSuffix(v[len("@microsoft.keyvault("):], ")"))
	case guid.MatchString(v):
		if strings.Contains(strings.ToLower(name), "instrumentationkey") {
			return []endpoint{{Kind: AppInsights, AppInsightsKey: strings.ToLower(v)}}
		}
		return nil
	case strings.Contains(v, "://") && !strings.ContainsAny(v, " \t;"):
		return fromURL(name, v)
	case strings.Contains(v, "=") && strings.Contains(v, ";") || connectionKey(v):
		return fromPairs(name, pairs(v))
	case strings.Contains(v, ",") && strings.Contains(v, "="):
		return fromRedisConfig(name, v)
	}
	if h := cleanHost(v); h != "" && kindOfHost(h) != "" {
		return []endpoint{{Kind: settle(name, kindOfHost(h)), Host: h}}
	}
	return nil
}

// connectionKey says whether a value with no semicolon is still a one-pair
// connection string, such as Endpoint=https://...
func connectionKey(v string) bool {
	k, _, ok := strings.Cut(v, "=")
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "endpoint", "accountendpoint", "server", "data source", "instrumentationkey", "blobendpoint":
		return true
	}
	return false
}

// settle tells an Event Hubs namespace from a Service Bus one, which share
// a host suffix, by the setting's name.
func settle(name, kind string) string {
	if kind != ServiceBus {
		return kind
	}
	n := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(name))
	if strings.Contains(n, "eventhub") {
		return EventHub
	}
	return ServiceBus
}

var schemeKinds = map[string]string{
	"http": HTTP, "https": HTTP, "ws": HTTP, "wss": HTTP,
	"sb": ServiceBus, "amqp": ServiceBus, "amqps": ServiceBus,
	"redis": Redis, "rediss": Redis,
	"postgres": Postgres, "postgresql": Postgres, "mysql": MySQL, "sqlserver": SQL,
}

func fromURL(name, raw string) []endpoint {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	host := cleanHost(u.Hostname())
	if host == "" {
		return nil
	}
	scheme := strings.ToLower(u.Scheme)
	kind := kindOfHost(host)
	if kind == "" {
		kind = schemeKinds[scheme]
	}
	if kind == "" {
		return nil
	}
	e := endpoint{Kind: settle(name, kind), Host: host}
	if (kind == SQL || kind == Postgres || kind == MySQL) && scheme != "https" && scheme != "http" {
		e.Database = strings.Split(strings.Trim(u.Path, "/"), "/")[0]
	}
	return []endpoint{e}
}

// pairs reads a key=value; connection string, keys lowercase. A value may be
// quoted to hold a semicolon, as an Entity Framework provider connection string is.
func pairs(v string) map[string]string {
	out := map[string]string{}
	for len(v) > 0 {
		k, rest, ok := strings.Cut(v, "=")
		if !ok {
			break
		}
		rest = strings.TrimLeft(rest, " ")
		var val string
		if len(rest) > 0 && (rest[0] == '"' || rest[0] == '\'') {
			if end := strings.IndexByte(rest[1:], rest[0]); end >= 0 {
				val, rest = rest[1:end+1], rest[end+2:]
				_, rest, _ = strings.Cut(rest, ";")
			} else {
				val, rest = rest[1:], ""
			}
		} else {
			val, rest, _ = strings.Cut(rest, ";")
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(val)
		v = rest
	}
	return out
}

func fromPairs(name string, p map[string]string) []endpoint {
	if inner, ok := p["provider connection string"]; ok {
		return fromPairs(name, pairs(inner))
	}
	if k := p["instrumentationkey"]; guid.MatchString(k) {
		return []endpoint{{Kind: AppInsights, AppInsightsKey: strings.ToLower(k)}}
	}
	if strings.EqualFold(p["usedevelopmentstorage"], "true") {
		return nil
	}
	if a := p["accountendpoint"]; a != "" {
		if out := fromURL(name, a); len(out) > 0 {
			out[0].Kind = Cosmos
			out[0].Database = p["database"]
			return out
		}
		return nil
	}
	if b := p["blobendpoint"]; b != "" {
		return fromURL(name, b)
	}
	if acct := p["accountname"]; acct != "" {
		suffix := p["endpointsuffix"]
		if suffix == "" {
			suffix = "core.windows.net"
		}
		if h := cleanHost(acct + ".blob." + suffix); h != "" {
			return []endpoint{{Kind: Storage, Host: h}}
		}
		return nil
	}
	if ep := p["endpoint"]; ep != "" {
		return fromURL(name, ep)
	}
	for _, k := range []string{"server", "data source", "address", "addr", "network address", "host"} {
		h := cleanHost(p[k])
		if h == "" {
			continue
		}
		kind := kindOfHost(h)
		switch {
		case kind != "":
		case p["port"] == "3306":
			kind = MySQL
		case k == "host":
			// Npgsql names the server Host; SQL Server never does.
			kind = Postgres
		default:
			kind = SQL
		}
		db := p["database"]
		if db == "" {
			db = p["initial catalog"]
		}
		return []endpoint{{Kind: settle(name, kind), Host: h, Database: db}}
	}
	return nil
}

// fromRedisConfig reads a StackExchange.Redis configuration string:
// host:port,password=...,ssl=True. A host Azure does not name a cache counts
// only under a setting named for Redis.
func fromRedisConfig(name, v string) []endpoint {
	first, _, _ := strings.Cut(v, ",")
	h := cleanHost(first)
	if h == "" {
		return nil
	}
	kind := kindOfHost(h)
	if kind == "" && strings.Contains(strings.ToLower(name), "redis") {
		kind = Redis
	}
	if kind == "" {
		return nil
	}
	return []endpoint{{Kind: kind, Host: h}}
}

// vaultReference reads an App Service Key Vault reference:
// SecretUri=https://... or VaultName=...;SecretName=...
func vaultReference(inner string) []endpoint {
	p := pairs(inner)
	if u := p["secreturi"]; u != "" {
		if out := fromURL("", u); len(out) > 0 && out[0].Kind == KeyVault {
			return out
		}
		return nil
	}
	if h := cleanHost(p["vaultname"] + ".vault.azure.net"); p["vaultname"] != "" && h != "" {
		return []endpoint{{Kind: KeyVault, Host: h}}
	}
	return nil
}
