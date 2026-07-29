package contextinfo

import "testing"

func TestContextProvider_UseCaseIsCached(t *testing.T) {
	p := NewContextProvider(t.TempDir(), "v1")

	first := p.ContextUseCase()
	if first == nil {
		t.Fatal("ContextUseCase returned nil")
	}
	if second := p.ContextUseCase(); second != first {
		t.Error("ContextUseCase should return the cached instance")
	}
}
