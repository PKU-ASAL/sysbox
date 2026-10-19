package artifact

import "context"

type Publication func(context.Context, Spec) (func(Result) error, error)
type publicationKey struct{}

// WithPublication installs a trusted begin-before-use publisher for the run.
// The callback must persist its intent before permitting any cache mutation.
func WithPublication(ctx context.Context, publish Publication) context.Context {
	return context.WithValue(ctx, publicationKey{}, publish)
}
func NewWithContext(ctx context.Context) *Resolver {
	r := New()
	r.ctx = ctx
	r.publish, _ = ctx.Value(publicationKey{}).(Publication)
	return r
}
func (r *Resolver) beginPublication(spec Spec) (func(Result) error, error) {
	if r.publish == nil {
		return func(Result) error { return nil }, nil
	}
	return r.publish(r.ctx, spec)
}

// BeginPublication also protects OCI providers which bypass the file resolver.
func BeginPublication(ctx context.Context, spec Spec) (func(Result) error, error) {
	return NewWithContext(ctx).beginPublication(spec)
}
