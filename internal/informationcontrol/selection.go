package informationcontrol

import (
	"context"
	"github.com/HJSunDev/ownward/internal/contract"
)

func (c *Control) selected(ctx context.Context, selection contract.ControlSelection) contract.ControlState {
	if a, ok := c.authority.(contract.SelectedControlAuthority); ok {
		selection.Credential = contract.AuthenticationDigest(ctx)
		state, err := a.ReadSelectedControl(selection)
		state.ReadError = err
		return state
	}
	return c.authority.ReadControl()
}
