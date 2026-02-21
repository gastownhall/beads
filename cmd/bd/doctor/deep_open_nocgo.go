//go:build !cgo

package doctor

import (
	"database/sql"
	"fmt"
)

func openDeepValidationDB(_ string, _ string) (*sql.DB, func(), error) {
	return nil, func() {}, fmt.Errorf("deep validation requires CGO-enabled build")
}
