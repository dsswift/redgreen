package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// readConnections reads a fixture repository and fails the test if any
// secret in it survives into what is kept.
func readConnections(t *testing.T, files map[string]string, secrets ...string) *Found {
	t.Helper()
	m := memFiles{commit: "c", files: map[string][]byte{}}
	for p, s := range files {
		m.files[p] = []byte(s)
	}
	found, _, err := Read(context.Background(), m, "github/acme/shop", "shop", nil, Bots{})
	if err != nil {
		t.Fatal(err)
	}
	kept, _ := json.Marshal(found)
	for _, s := range secrets {
		if strings.Contains(string(kept), s) {
			t.Errorf("a secret was kept: %s", s)
		}
	}
	return found
}

func line(c Connection) string {
	to := c.Host + c.AppInsightsKey
	if c.Database != "" {
		to += "/" + c.Database
	}
	return c.Service + "@" + c.Environment + " " + c.Kind + " " + to + " " + strings.Join(c.Settings, ",")
}

func lines(f *Found) []string {
	out := []string{}
	for _, c := range f.Connections {
		out = append(out, line(c))
	}
	return out
}

func TestAModernAppsSettingsNameItsHostsByEnvironment(t *testing.T) {
	found := readConnections(t, map[string]string{
		"src/Api/Api.csproj": csproj("", "Microsoft.NET.Sdk.Web"),
		"src/Api/appsettings.json": `{
  // comments and trailing commas are allowed, as .NET allows them
  "ConnectionStrings": {
    "Main": "Server=tcp:shop-dev.database.windows.net,1433;Initial Catalog=Shop;User ID=app;Password=hunter2;",
    "Local": "Server=(localdb)\\mssqllocaldb;Database=Shop;Trusted_Connection=True",
  },
  "Storage": "DefaultEndpointsProtocol=https;AccountName=shopfiles;AccountKey=c2VjcmV0a2V5;EndpointSuffix=core.windows.net",
  "Urls": "http://*:5000",
  "AllowedHosts": "*",
}`,
		"src/Api/appsettings.Production.json": `{
  "ConnectionStrings": { "Main": "Server=tcp:shop-prod.database.windows.net,1433;Initial Catalog=Shop;Password=prodpass" },
  "ServiceBus": { "Orders": "Endpoint=sb://shop-bus.servicebus.windows.net/;SharedAccessKeyName=send;SharedAccessKey=YnVza2V5" },
  "EventHubConnection": "Endpoint=sb://shop-hub.servicebus.windows.net/;SharedAccessKeyName=x;SharedAccessKey=aHVia2V5;EntityPath=clicks",
  "Redis": "shop-cache.redis.cache.windows.net:6380,password=cmVkaXNrZXk=,ssl=True,abortConnect=False",
  "Cosmos": "AccountEndpoint=https://shop-docs.documents.azure.com:443/;AccountKey=Y29zbW9za2V5;",
  "Secret": "@Microsoft.KeyVault(SecretUri=https://shop-kv.vault.azure.net/secrets/main/)",
  "AppConfig": "Endpoint=https://shop-cfg.azconfig.io;Id=abc;Secret=Y2Znc2VjcmV0",
  "Search": { "Url": "https://shop-search.search.windows.net" },
  "Payments": { "BaseUrl": "https://user:pw@api.payments.example.com/v2?code=c2FzdG9rZW4" },
  "APPLICATIONINSIGHTS_CONNECTION_STRING": "InstrumentationKey=0A1B2C3D-0000-4000-8000-000000000001;IngestionEndpoint=https://eastus-8.in.applicationinsights.azure.com/",
  "Reports": "Host=shop-pg.postgres.database.azure.com;Database=reports;Username=app;Password=pgpass"
}`,
		"src/Api/appsettings.Staging.json":    `{"ConnectionStrings":{"Main":"Data Source=shop-stg.database.windows.net;Initial Catalog=shop;Password=stgpass"}}`,
		"src/Api/Nested/appsettings.Dev.json": `{"X":"Server=nested.database.windows.net;Database=n"}`,
		"src/Api/local.settings.json":         `{"IsEncrypted":false,"Values":{"AzureWebJobsStorage":"UseDevelopmentStorage=true","Queue":"AccountName=shopqueue;AccountKey=cXVldWVrZXk="}}`,
		"src/Api/prod.env.example":            "# example\nexport DATABASE_URL=postgres://app:pw@shop-pg.postgres.database.azure.com:5432/orders\nAPI=\"https://api.partner.example.com/v1\"\n",
		"infra/main.tf":                       "terraform {\n  backend azurerm {}\n}\n",
		"infra/appsettings.Production.json":   `{"X":"Server=infra.database.windows.net"}`,
	}, "hunter2", "prodpass", "stgpass", "pgpass", "c2VjcmV0a2V5", "YnVza2V5", "aHVia2V5", "cmVkaXNrZXk", "Y29zbW9za2V5", "Y2Znc2VjcmV0", "c2FzdG9rZW4", "cXVldWVrZXk", "user:pw", "app:pw")
	same(t, lines(found), []string{
		"src/Api@Prod appinsights 0a1b2c3d-0000-4000-8000-000000000001 APPLICATIONINSIGHTS_CONNECTION_STRING",
		"src/Api@Prod http api.partner.example.com API",
		"src/Api@Prod http api.payments.example.com Payments:BaseUrl",
		"src/Api@Prod servicebus shop-bus.servicebus.windows.net ServiceBus:Orders",
		"src/Api@Prod redis shop-cache.redis.cache.windows.net Redis",
		"src/Api@Prod appconfig shop-cfg.azconfig.io AppConfig",
		"src/Api@Prod cosmos shop-docs.documents.azure.com Cosmos",
		"src/Api@Prod eventhub shop-hub.servicebus.windows.net EventHubConnection",
		"src/Api@Prod keyvault shop-kv.vault.azure.net Secret",
		"src/Api@Prod postgres shop-pg.postgres.database.azure.com/orders DATABASE_URL",
		"src/Api@Prod postgres shop-pg.postgres.database.azure.com/reports Reports",
		"src/Api@Prod sql shop-prod.database.windows.net/Shop ConnectionStrings:Main",
		"src/Api@Prod search shop-search.search.windows.net Search:Url",
		"src/Api@Staging sql shop-stg.database.windows.net/shop ConnectionStrings:Main",
		"src/Api@unknown sql shop-dev.database.windows.net/Shop ConnectionStrings:Main",
		"src/Api@unknown storage shopfiles.blob.core.windows.net Storage",
		"src/Api@unknown storage shopqueue.blob.core.windows.net Values:Queue",
	})
}

func TestALegacyWebConfigAndItsTransforms(t *testing.T) {
	found := readConnections(t, map[string]string{
		"Legacy/Legacy.csproj": `<Project ToolsVersion="15.0"><PropertyGroup><ProjectTypeGuids>{349c5851-65df-11da-9384-00065b846f21};{fae04ec0-301f-11d3-bf4b-00c04f79efbc}</ProjectTypeGuids><OutputType>Library</OutputType></PropertyGroup></Project>`,
		"Legacy/Web.config": `<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <connectionStrings>
    <add name="ShopEntities" connectionString="metadata=res://*/Shop.csdl;provider=System.Data.SqlClient;provider connection string=&quot;data source=legacy-dev.database.windows.net;initial catalog=LegacyShop;user id=app;password=webpass;MultipleActiveResultSets=True&quot;" providerName="System.Data.EntityClient" />
  </connectionStrings>
  <appSettings>
    <add key="StorageConnection" value="DefaultEndpointsProtocol=https;AccountName=legacyfiles;AccountKey=bGVnYWN5a2V5" />
    <add key="webpages:Version" value="3.0.0.0" />
    <add key="Help" value="https://docs.example.com/help" />
  </appSettings>
  <system.serviceModel>
    <client>
      <endpoint address="https://orders.example.com/Orders.svc" binding="basicHttpBinding" contract="IOrders" name="Orders" />
    </client>
  </system.serviceModel>
  <applicationSettings>
    <Legacy.Properties.Settings>
      <setting name="ReportServer" serializeAs="String"><value>https://reports.example.com/ReportServer</value></setting>
    </Legacy.Properties.Settings>
  </applicationSettings>
  <system.webServer><httpProtocol><customHeaders><add name="Content-Security-Policy" value="default-src 'self' https://cdn.example.com" /></customHeaders></httpProtocol></system.webServer>
</configuration>`,
		"Legacy/Web.Prod.config": `<?xml version="1.0"?>
<configuration xmlns:xdt="http://schemas.microsoft.com/XML-Document-Transform">
  <connectionStrings>
    <add name="ShopEntities" connectionString="metadata=res://*/Shop.csdl;provider=System.Data.SqlClient;provider connection string=&quot;data source=tcp:legacy-prod.database.windows.net,1433;initial catalog=LegacyShop;password=prodweb&quot;" xdt:Transform="SetAttributes" xdt:Locator="Match(name)" />
  </connectionStrings>
  <appSettings>
    <add key="StorageConnection" value="AccountName=legacyprod;AccountKey=cHJvZGtleQ==" xdt:Transform="SetAttributes" xdt:Locator="Match(key)" />
    <add key="APPINSIGHTS_INSTRUMENTATIONKEY" value="11111111-2222-4333-8444-555555555555" xdt:Transform="Insert" />
  </appSettings>
</configuration>`,
		"Legacy/Web.Pre-Prod.config": `<configuration><connectionStrings><add name="ShopEntities" connectionString="Server=legacy-pre.database.windows.net;Database=LegacyShop;Password=prepass" /></connectionStrings></configuration>`,
		"Legacy/Web.Debug.config":    `<configuration><appSettings><add key="Help" value="http://localhost:8080/help" /></appSettings></configuration>`,
		"Legacy/ApplicationInsights.config": `<?xml version="1.0" encoding="utf-8"?>
<ApplicationInsights xmlns="http://schemas.microsoft.com/ApplicationInsights/2013/Settings">
  <InstrumentationKey>99999999-8888-4777-8666-555555555555</InstrumentationKey>
</ApplicationInsights>`,
	}, "webpass", "bGVnYWN5a2V5", "prodweb", "cHJvZGtleQ", "prepass", "app;")
	same(t, lines(found), []string{
		"Legacy@Preprod sql legacy-pre.database.windows.net/LegacyShop ShopEntities",
		"Legacy@Prod appinsights 11111111-2222-4333-8444-555555555555 APPINSIGHTS_INSTRUMENTATIONKEY",
		"Legacy@Prod sql legacy-prod.database.windows.net/LegacyShop ShopEntities",
		"Legacy@Prod storage legacyprod.blob.core.windows.net StorageConnection",
		"Legacy@unknown appinsights 99999999-8888-4777-8666-555555555555 InstrumentationKey",
		"Legacy@unknown http docs.example.com Help",
		"Legacy@unknown sql legacy-dev.database.windows.net/LegacyShop ShopEntities",
		"Legacy@unknown storage legacyfiles.blob.core.windows.net StorageConnection",
		"Legacy@unknown http orders.example.com Orders",
		"Legacy@unknown http reports.example.com ReportServer",
	})
}

func TestAConfigFileThatCannotBeParsedIsAProblem(t *testing.T) {
	found := readConnections(t, map[string]string{
		"main.go":                      "package main\nfunc main() {}\n",
		"appsettings.json":             `{"A": `,
		"appsettings.Development.json": `{"Db":"Server=dev.database.windows.net;Database=d"}`,
	})
	same(t, lines(found), []string{"@Dev sql dev.database.windows.net/d Db"})
	if len(found.Problems) != 1 || !strings.HasPrefix(found.Problems[0], "appsettings.json: not JSON") {
		t.Errorf("problems = %v", found.Problems)
	}
}

func TestEnvironmentOf(t *testing.T) {
	for file, want := range map[string]string{
		"appsettings.json": UnknownEnvironment, "appsettings.Production.json": "Prod", "a/Web.Prod.config": "Prod", "Web.PRD.config": "Prod",
		"appsettings.Staging.json": "Staging", "Web.Test.config": "Test", "appsettings.Development.json": "Dev", "Web.Dev.config": "Dev",
		"Web.PreProd.config": "Preprod", "Web.Pre-Prod.config": "Preprod", "Web.UAT.config": "Uat", "appsettings.qa.json": "Qa",
		"Web.Prod-EU.config": "Prod", "Web.Release.config": UnknownEnvironment, "local.settings.json": UnknownEnvironment,
		"ApplicationInsights.config": UnknownEnvironment, ".env.example": UnknownEnvironment, "staging.env.example": "Staging",
	} {
		if got := EnvironmentOf(file); got != want {
			t.Errorf("EnvironmentOf(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestOnlyConfigFilesAreRead(t *testing.T) {
	for file, want := range map[string]configFormat{
		"appsettings.json": jsonConfig, "AppSettings.Prod.json": jsonConfig, "local.settings.json": jsonConfig, "Web.config": xmlConfig,
		"web.Prod.config": xmlConfig, "ApplicationInsights.config": xmlConfig, ".env.example": envConfig, "prod.env.example": envConfig,
		"packages.config": notConfig, ".env": notConfig, "settings.json": notConfig, "NLog.config": notConfig,
	} {
		if got := formatOf(file); got != want {
			t.Errorf("formatOf(%q) = %d, want %d", file, got, want)
		}
	}
}
