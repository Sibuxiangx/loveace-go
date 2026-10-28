package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Sibuxiangx/loveace-go/server/internal/dto"
	"github.com/Sibuxiangx/loveace-go/server/internal/gate"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// registerMe wires /v1/me and device management.
func (s *Server) registerMe(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-me",
		Summary:     "Current account and credential state",
		Method:      http.MethodGet,
		Path:        "/v1/me",
		Tags:        []string{"me"},
	}, func(ctx context.Context, in *AuthHeaderInput) (*Output[dto.UserView], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		u, err := s.deps.Store.GetUser(ctx, claims.UserID)
		if err != nil {
			return nil, mapErr(err)
		}
		masked := "***"
		if cred, cerr := s.deps.Store.GetCredential(ctx, u.ID); cerr == nil {
			if plaintext, derr := s.deps.Vault.Decrypt(u.ID, cred.Blob); derr == nil {
				var payload vault.CredPayload
				if json.Unmarshal(plaintext, &payload) == nil && payload.Account != "" {
					masked = gate.MaskedAccount(payload.Account)
				}
			}
		}
		return &Output[dto.UserView]{Status: http.StatusOK, Body: dto.UserView{
			ID:              u.ID,
			AccountMasked:   masked,
			CredentialState: u.CredentialState,
		}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-devices",
		Summary:     "List active devices",
		Method:      http.MethodGet,
		Path:        "/v1/me/devices",
		Tags:        []string{"me"},
	}, func(ctx context.Context, in *AuthHeaderInput) (*Output[[]dto.DeviceView], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		devices, err := s.deps.Store.ListDevices(ctx, claims.UserID)
		if err != nil {
			return nil, mapErr(err)
		}
		out := make([]dto.DeviceView, 0, len(devices))
		for _, d := range devices {
			out = append(out, dto.DeviceView{
				ID: d.ID, Name: d.Name, Platform: d.Platform,
				CreatedAt: d.CreatedAt, RevokedAt: d.RevokedAt,
			})
		}
		return &Output[[]dto.DeviceView]{Status: http.StatusOK, Body: out}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revoke-device",
		Summary:     "Revoke a device and release its school session quota",
		Method:      http.MethodDelete,
		Path:        "/v1/me/devices/{id}",
		Tags:        []string{"me"},
	}, func(ctx context.Context, in *RevokeDeviceInput) (*Output[struct{}], error) {
		claims, err := s.authenticate(ctx, in.RawAuthorization)
		if err != nil {
			return nil, err
		}
		if err := s.revokeDevice(ctx, claims.UserID, in.ID); err != nil {
			return nil, mapErr(err)
		}
		return &Output[struct{}]{Status: http.StatusOK}, nil
	})
}

type RevokeDeviceInput struct {
	RawAuthorization string `header:"Authorization" required:"false"`
	ID               string `path:"id" minLength:"1" required:"false"`
}
