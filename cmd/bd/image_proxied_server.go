package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
)

// proxiedImager hands back the raw bead-image surface for the proxied route,
// through a provider's OWN capability accessor.
//
// It peels decorators until one answers, because the role is not one every
// decorator forwards: the external-dependency wrapper does not declare it, and
// a read needs none of that wrapper's policy — it is about edges a write adds
// across repositories, and an image reads raw rows of one database.
func proxiedImager() (issueops.Imager, error) {
	if uowProvider == nil {
		return nil, fmt.Errorf("proxied-server UOW provider not initialized")
	}
	for p := uowProvider; p != nil; {
		if source, ok := p.(uow.ImagerSource); ok {
			return source.Imager()
		}
		unwrapper, ok := p.(uow.ProviderUnwrapper)
		if !ok {
			break
		}
		p = unwrapper.Unwrap()
	}
	return nil, fmt.Errorf("proxied-server provider %T does not offer the Imager accessor", uowProvider)
}

// runImageProxiedServer is `bd image` against a team server. Ids reach the
// role exact, as they reach the Deleter on this route, and every id is read on
// one read-only unit of work. The output is printImages', byte for byte the
// direct route's.
func runImageProxiedServer(ctx context.Context, ids []string) error {
	imager, err := proxiedImager()
	if err != nil {
		return HandleError("%v", err)
	}
	return printImages(ctx, imager, ids)
}
