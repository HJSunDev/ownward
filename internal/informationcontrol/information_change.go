package informationcontrol

import (
	"context"
	"github.com/HJSunDev/ownward/internal/contract"
)

func (p *Product) ReadInformation(ctx context.Context, id string) (contract.InformationRead, error) {
	return authorized(ctx, p.control, contract.ReadPermission, func(bound context.Context) (contract.InformationRead, error) {
		return p.kernel.ReadInformation(bound, id)
	})
}
func (p *Product) ReadEvidenceWithBasis(ctx context.Context, id string) (contract.EvidenceRead, error) {
	return authorized(ctx, p.control, contract.ReadPermission, func(bound context.Context) (contract.EvidenceRead, error) {
		return p.kernel.ReadEvidenceWithBasis(bound, id)
	})
}
func (p *Product) CheckInformation(ctx context.Context, refs []string) ([]contract.InformationCheck, error) {
	return authorized(ctx, p.control, contract.ReadPermission, func(bound context.Context) ([]contract.InformationCheck, error) {
		return p.kernel.CheckInformation(bound, refs)
	})
}
