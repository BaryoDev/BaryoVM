// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package site

import (
	"bytes"
	"text/template"
)

// The gzip block lives inside the site's static locations and nowhere else. A global `gzip on`
// also compresses proxied responses, and a dynamic response that carries a token next to text an
// attacker controls is open to BREACH. nginx compresses text/html whenever gzip is on, which is
// fine for a static page and is why HTML is not in the types list. No brotli: stock nginx needs a
// third party module for it.
//
// add_header does not inherit into a location that sets its own, so the HSTS header is repeated
// in each location through the "headers" template instead of relying on the server level one.
const serverTemplate = `# {{.Marker}} (baryovm site add). Hand edits are overwritten on the next run.
{{define "port80"}}server {
    listen 80;
    listen [::]:80;
    server_name {{.Domain}};

    location /.well-known/acme-challenge/ {
        root {{.ACMEDir}};
    }

    location / {
        return 301 https://{{.Domain}}$request_uri;
    }
}
{{end}}{{define "gzip"}}        gzip on;
        gzip_comp_level 5;
        gzip_min_length 1024;
        gzip_vary on;
        gzip_types application/javascript text/javascript text/css image/svg+xml application/wasm application/json;
{{end}}{{define "headers"}}        add_header Strict-Transport-Security "max-age=31536000" always;
{{end}}{{if .HTTPOnly}}# Stage 1: port 80 only, until the certificate for {{.Domain}} exists.

{{template "port80" .}}{{else}}
{{template "port80" .}}
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name {{.Domain}};

    ssl_certificate {{.CertDir}}/fullchain.pem;
    ssl_certificate_key {{.CertDir}}/privkey.pem;
{{if .SSLOptions}}    include {{.SSLOptionsPath}};
{{end}}{{if .DHParam}}    ssl_dhparam {{.DHParamPath}};
{{end}}
    root {{.Root}};
    index index.html;

    location ^~ /.well-known/ {
        try_files $uri =404;
    }

    location ~ /\. {
        deny all;
        return 404;
    }
{{range .HashedPrefixes}}
    location ^~ {{.}} {
        try_files $uri =404;
        add_header Cache-Control "public, max-age=31536000, immutable" always;
{{template "headers" $}}{{template "gzip" $}}    }
{{end}}
    location / {
        try_files $uri $uri.html $uri/ =404;
        add_header Cache-Control "no-cache" always;
{{template "headers" .}}{{template "gzip" .}}    }
}
{{end}}`

var serverTmpl = template.Must(template.New("server").Parse(serverTemplate))

// SSLFiles says which of certbot's helper files exist on the VM. `certbot certonly` does not create
// them (only the nginx plugin does), so including them blindly fails nginx -t on a host that never
// ran the plugin.
type SSLFiles struct {
	Options bool
	DHParam bool
}

type serverData struct {
	Marker         string
	Domain         string
	Root           string
	ACMEDir        string
	CertDir        string
	SSLOptions     bool
	SSLOptionsPath string
	DHParam        bool
	DHParamPath    string
	HTTPOnly       bool
	// HashedPrefixes are the paths a static export puts content-hashed files under. They get a
	// year-long immutable cache; everything else is no-cache so a new deploy shows at once.
	HashedPrefixes []string
}

var hashedPrefixes = []string{"/_next/static/", "/assets/"}

func render(o Options, f SSLFiles, httpOnly bool) string {
	var b bytes.Buffer
	err := serverTmpl.Execute(&b, serverData{
		Marker:         Marker,
		Domain:         o.Domain,
		Root:           o.Root,
		ACMEDir:        ACMEDir,
		CertDir:        leDir + "/live/" + o.Domain,
		SSLOptions:     f.Options,
		SSLOptionsPath: sslOptions,
		DHParam:        f.DHParam,
		DHParamPath:    sslDHParam,
		HTTPOnly:       httpOnly,
		HashedPrefixes: hashedPrefixes,
	})
	if err != nil {
		// The template is a constant and the data is a struct of strings; a failure is a bug in
		// this file, caught by the template test, never a runtime condition.
		panic("site: render server block: " + err.Error())
	}
	return b.String()
}

// HTTPBlock is the stage-one config: port 80 only, so nginx can start and serve the challenge
// before any certificate exists. A 443 block pointing at a missing certificate fails nginx -t.
func HTTPBlock(o Options) string { return render(o, SSLFiles{}, true) }

// FullBlock is the final config: the redirect, TLS, and the static site.
func FullBlock(o Options, f SSLFiles) string { return render(o, f, false) }
