package core

import (
	"context"
	"errors"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

var _ contract.OrganizationJobCapability = (*StreamingAssets)(nil)

func (s *StreamingAssets) organizationJobsTool(ctx context.Context, request contract.StreamRequest) (*contract.StreamResult, error) {
	ctx, err := s.Store.BeginAccess(ctx, contract.AuthenticationDigest(ctx), contract.MaintainPermission)
	if err != nil {
		return nil, err
	}
	r, err := request.Arguments.Open(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := streamjson.Parse(ctx, s.Scratch, r, s.Budget, s.DiskBytes)
	r.Close()
	if err != nil {
		return nil, err
	}
	var input contract.OrganizationJobRequest
	err = doc.Root().DecodeSmall(&input, 4096)
	doc.Close()
	if err != nil {
		return nil, err
	}
	if len(input.AssetID) > 256 || len(input.AfterAssetID) > 256 || len(input.Lease) > 256 {
		return nil, errors.New("组织执行身份过长")
	}
	var out contract.OrganizationJobResult
	switch input.Action {
	case "claim":
		out.Claim, err = s.Store.ClaimOrganizationAfter(ctx, input.AssetID, input.RequestID, input.LeaseSeconds, input.AfterAssetID, input.Background)
		out.Available = out.Claim != nil
	case "renew", "release":
		out.Claim, err = s.Store.ChangeOrganizationLease(ctx, input.AssetID, input.Lease, input.LeaseSeconds, input.Action == "release")
	case "wait":
		out.Available, err = s.Store.WaitOrganization(ctx, input.AssetID, input.WaitSeconds)
	default:
		return nil, errors.New("不支持的组织待办操作")
	}
	if err != nil {
		return nil, err
	}
	s.deliveryMu.RLock()
	defer s.deliveryMu.RUnlock()
	if err = s.Store.CheckAccess(ctx); err != nil {
		return nil, err
	}
	result, err := s.smallResult(ctx, out)
	if err == nil {
		s.trackResult(result)
	}
	return result, err
}

func (s *StreamingAssets) OrganizationJobs(ctx context.Context, input contract.OrganizationJobRequest) (contract.OrganizationJobResult, error) {
	return streamValue[contract.OrganizationJobResult](ctx, s, "ownward_semantic_jobs", input)
}
