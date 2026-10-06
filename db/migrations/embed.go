// Package migrations embebe las migraciones SQL de goose en el binario: la
// imagen de producción es distroless (sin shell ni binario `goose` aparte).
package migrations

import "embed"

// FS contiene los .sql de esta carpeta.
//
//go:embed *.sql
var FS embed.FS
