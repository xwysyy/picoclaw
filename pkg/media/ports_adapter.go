package media

import "github.com/xwysyy/X-Claw/internal/core/ports"

// AsMediaResolver adapts a MediaStore to the core MediaResolver port.
// Returns nil when store is nil.
func AsMediaResolver(store MediaStore) ports.MediaResolver {
	if store == nil {
		return nil
	}
	return mediaResolverAdapter{store: store}
}

type mediaResolverAdapter struct {
	store MediaStore
}

func (a mediaResolverAdapter) ResolveWithMeta(ref string) (string, ports.MediaMeta, error) {
	localPath, meta, err := a.store.ResolveWithMeta(ref)
	if err != nil {
		return "", ports.MediaMeta{}, err
	}
	return localPath, ports.MediaMeta{
		Filename:    meta.Filename,
		ContentType: meta.ContentType,
		Source:      meta.Source,
	}, nil
}
