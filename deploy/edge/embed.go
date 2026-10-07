// Package edgefiles, uzak sunucunun kurulum dosyalarını programa gömer; kurulum betiği bu
// dosyaları yeni sunucuya yazar (bkz. internal/enroll).
package edgefiles

import _ "embed"

//go:embed docker-compose.yml
var Compose string

//go:embed nginx.conf.template
var Nginx string

//go:embed srs-edge.conf.tmpl
var SRS string
