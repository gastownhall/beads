//go:build !integration

package beads

func setupIntegrationTestMain(_ string) (func(), error) {
	return func() {}, nil
}
