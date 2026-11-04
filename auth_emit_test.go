package main

import (
	"strings"
	"testing"
)

func TestBuildAuthInitBearerOnly(t *testing.T) {
	spec := `{
  "openapi": "3.0.0",
  "info": {"title": "Bearer", "version": "1"},
  "security": [{"ApiKey": []}],
  "paths": {},
  "components": {
    "securitySchemes": {
      "ApiKey": {"type": "http", "scheme": "bearer"}
    }
  }
}`

	init, needsAPIKey, needsOAuth, schemes := buildAuthInit(loadTestSpec(t, spec))

	want := "apikey.InitBearerWithType(\"ApiKey\", \"Authorization\")\n" +
		"bartolocli.SetDefaultAuthType(\"ApiKey\")"
	if init != want {
		t.Fatalf("bearer-only AuthInit mismatch:\n got: %q\nwant: %q", init, want)
	}
	if !needsAPIKey || needsOAuth {
		t.Fatalf("expected needsAPIKey=true needsOAuth=false, got %v/%v", needsAPIKey, needsOAuth)
	}
	if len(schemes) != 1 || !schemes[0].Default || !schemes[0].Supported {
		t.Fatalf("expected one default supported scheme, got %+v", schemes)
	}
}

func TestBuildAuthInitClientCredentials(t *testing.T) {
	spec := `{
  "openapi": "3.0.0",
  "info": {"title": "CC", "version": "1"},
  "security": [{"cc": []}],
  "paths": {},
  "components": {
    "securitySchemes": {
      "cc": {
        "type": "oauth2",
        "flows": {
          "clientCredentials": {
            "tokenUrl": "https://example.com/token",
            "scopes": {"write": "w", "read": "r"}
          }
        }
      }
    }
  }
}`

	init, needsAPIKey, needsOAuth, _ := buildAuthInit(loadTestSpec(t, spec))

	want := "oauth.InitClientCredentialsWithType(\"cc\", \"https://example.com/token\", oauth.Scopes(\"read\", \"write\"))\n" +
		"bartolocli.SetDefaultAuthType(\"cc\")"
	if init != want {
		t.Fatalf("client-credentials AuthInit mismatch:\n got: %q\nwant: %q", init, want)
	}
	if needsAPIKey || !needsOAuth {
		t.Fatalf("expected needsAPIKey=false needsOAuth=true, got %v/%v", needsAPIKey, needsOAuth)
	}
}

func TestBuildAuthInitClientCredentialsNoScopes(t *testing.T) {
	spec := `{
  "openapi": "3.0.0",
  "info": {"title": "CC", "version": "1"},
  "security": [{"cc": []}],
  "paths": {},
  "components": {
    "securitySchemes": {
      "cc": {
        "type": "oauth2",
        "flows": {"clientCredentials": {"tokenUrl": "https://example.com/token", "scopes": {}}}
      }
    }
  }
}`

	init, _, _, _ := buildAuthInit(loadTestSpec(t, spec))
	if strings.Contains(init, "oauth.Scopes") {
		t.Fatalf("expected no Scopes option when empty, got %q", init)
	}
	if !strings.Contains(init, "oauth.InitClientCredentialsWithType(\"cc\", \"https://example.com/token\")") {
		t.Fatalf("unexpected AuthInit without scopes: %q", init)
	}
}

func TestBuildAuthInitAuthorizationCode(t *testing.T) {
	spec := `{
  "openapi": "3.0.0",
  "info": {"title": "AC", "version": "1"},
  "security": [{"ac": []}],
  "paths": {},
  "components": {
    "securitySchemes": {
      "ac": {
        "type": "oauth2",
        "flows": {
          "authorizationCode": {
            "authorizationUrl": "https://example.com/authorize",
            "tokenUrl": "https://example.com/token",
            "scopes": {"profile": "p", "openid": "o"}
          }
        }
      }
    }
  }
}`

	init, needsAPIKey, needsOAuth, _ := buildAuthInit(loadTestSpec(t, spec))

	want := "oauth.InitAuthCodeWithType(\"ac\", \"\", \"https://example.com/authorize\", \"https://example.com/token\", oauth.Scopes(\"openid\", \"profile\"))\n" +
		"bartolocli.SetDefaultAuthType(\"ac\")"
	if init != want {
		t.Fatalf("auth-code AuthInit mismatch:\n got: %q\nwant: %q", init, want)
	}
	if needsAPIKey || !needsOAuth {
		t.Fatalf("expected needsAPIKey=false needsOAuth=true, got %v/%v", needsAPIKey, needsOAuth)
	}
}

func TestBuildAuthInitTwoSchemes(t *testing.T) {
	spec := `{
  "openapi": "3.0.0",
  "info": {"title": "Two", "version": "1"},
  "security": [{"Bearer": []}, {"ApiKeyHeader": []}],
  "paths": {},
  "components": {
    "securitySchemes": {
      "Bearer": {"type": "http", "scheme": "bearer"},
      "ApiKeyHeader": {"type": "apiKey", "in": "header", "name": "X-API-Key"}
    }
  }
}`

	init, needsAPIKey, needsOAuth, schemes := buildAuthInit(loadTestSpec(t, spec))

	// Emission order follows the sorted scheme-name set; the default is the
	// first scheme in the top-level security order (Bearer).
	want := "apikey.InitWithType(\"ApiKeyHeader\", \"X-API-Key\", apikey.LocationHeader)\n" +
		"apikey.InitBearerWithType(\"Bearer\", \"Authorization\")\n" +
		"bartolocli.SetDefaultAuthType(\"Bearer\")"
	if init != want {
		t.Fatalf("two-scheme AuthInit mismatch:\n got: %q\nwant: %q", init, want)
	}
	if !needsAPIKey || needsOAuth {
		t.Fatalf("expected needsAPIKey=true needsOAuth=false, got %v/%v", needsAPIKey, needsOAuth)
	}

	var defaults []string
	for _, s := range schemes {
		if s.Default {
			defaults = append(defaults, s.Name)
		}
	}
	if len(defaults) != 1 || defaults[0] != "Bearer" {
		t.Fatalf("expected Bearer to be the single default, got %v", defaults)
	}
}

func TestBuildAuthInitUnsupportedSchemeDocumentedManual(t *testing.T) {
	spec := `{
  "openapi": "3.0.0",
  "info": {"title": "Manual", "version": "1"},
  "security": [{"basic": []}],
  "paths": {},
  "components": {
    "securitySchemes": {
      "basic": {"type": "http", "scheme": "basic"}
    }
  }
}`

	init, needsAPIKey, needsOAuth, schemes := buildAuthInit(loadTestSpec(t, spec))
	if init != "" {
		t.Fatalf("expected empty AuthInit for unsupported-only spec, got %q", init)
	}
	if needsAPIKey || needsOAuth {
		t.Fatalf("expected no imports needed, got %v/%v", needsAPIKey, needsOAuth)
	}
	if len(schemes) != 1 || schemes[0].Supported {
		t.Fatalf("expected one unsupported scheme, got %+v", schemes)
	}
}
