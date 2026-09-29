package grove

import (
	"context"
	"errors"

	groveeng "github.com/provasign/grove/pkg/grove"
)

// Keep compatibility with the released engine during the coordinated upgrade.
// An older engine cannot prove base-contract coverage; callers must report it.
var ErrPreviewUnavailable = errors.New("this Grove version lacks base-revision impact preview; upgrade Grove to verify old contracts")

func (c *Client) PreviewChangeImpacts(ctx context.Context, queries [][2]string, files map[string][]byte) ([]*ChangeImpactResult, []string, error) {
	e, err := c.requireEngine()
	if err != nil {
		return nil, nil, err
	}
	p, ok := any(e).(interface {
		PreviewChangeImpacts(context.Context, [][2]string, map[string][]byte) ([]groveeng.ChangeImpactResult, []string, error)
	})
	if !ok {
		return nil, nil, ErrPreviewUnavailable
	}
	results, failures, err := p.PreviewChangeImpacts(ctx, queries, files)
	if err != nil {
		return nil, nil, err
	}
	out := make([]*ChangeImpactResult, len(results))
	for i, r := range results {
		if failures[i] == "" {
			out[i] = convertChangeImpact(r)
		}
	}
	return out, failures, nil
}
