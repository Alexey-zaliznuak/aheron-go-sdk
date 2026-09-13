package integrationoauth

import (
	"errors"
	"net/url"
	"path"
	"strings"
)

// The endpoint is an explicit versioned capability, never derived from another
// URL. Omission is compatible with legacy installations, not delivery support.
func ValidateMigrationEndpoint(raw string, other ...string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "#\\{}") || strings.TrimSpace(raw) != raw || len(raw) > 2048 {
		return errors.New("oauthMigrationUrl must be a dedicated HTTPS endpoint without credentials, query or fragment")
	}
	canonical := func(v *url.URL) string {
		host := strings.ToLower(v.Host)
		if v.Port() == "443" && v.Scheme == "https" {
			host = strings.TrimSuffix(host, ":443")
		}
		return strings.ToLower(v.Scheme) + "://" + host + path.Clean("/"+v.Path)
	}
	for _, old := range other {
		v, err := url.Parse(old)
		if err == nil && canonical(u) == canonical(v) {
			return errors.New("oauthMigrationUrl must differ from install, uninstall and lifecycle endpoints")
		}
	}
	return nil
}
