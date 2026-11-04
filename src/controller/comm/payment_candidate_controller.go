package comm

import (
	"strconv"
	"strings"

	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/util/constant"
	"github.com/labstack/echo/v4"
)

// PaymentCandidates lists unacknowledged observations for the authenticated
// merchant only. No response field represents verified payment or finality.
func (c *BaseCommController) PaymentCandidates(ctx echo.Context) error {
	var req struct {
		Action string `json:"action"`
	}
	if ctx.Bind(&req) != nil || req.Action != "transfer_candidates.list.v1" {
		return c.FailJson(ctx, constant.ParamsMarshalErr)
	}
	key := apiKeyFromContext(ctx)
	if key == nil || key.ID == 0 {
		return c.FailJson(ctx, constant.SignatureErr)
	}
	rows, err := data.ListPaymentCandidates(key.ID)
	if err != nil {
		return c.FailJson(ctx, err)
	}
	return c.SucJson(ctx, rows)
}

// AckPaymentCandidates confirms receipt, not chain verification or settlement.
func (c *BaseCommController) AckPaymentCandidates(ctx echo.Context) error {
	var req struct {
		Action string `json:"action"`
		IDs    string `json:"ids"`
	}
	if ctx.Bind(&req) != nil || req.Action != "transfer_candidates.ack.v1" || len(req.IDs) == 0 {
		return c.FailJson(ctx, constant.ParamsMarshalErr)
	}
	parts := strings.Split(req.IDs, ",")
	if len(parts) > 100 {
		return c.FailJson(ctx, constant.ParamsMarshalErr)
	}
	key := apiKeyFromContext(ctx)
	if key == nil || key.ID == 0 {
		return c.FailJson(ctx, constant.SignatureErr)
	}
	ids := make([]uint64, 0, len(parts))
	for _, s := range parts {
		id, err := strconv.ParseUint(s, 10, 64)
		if err != nil || id == 0 || strconv.FormatUint(id, 10) != s {
			return c.FailJson(ctx, constant.ParamsMarshalErr)
		}
		ids = append(ids, id)
	}
	if err := data.AcknowledgePaymentCandidates(key.ID, ids); err != nil {
		return c.FailJson(ctx, constant.ParamsMarshalErr)
	}
	return c.SucJson(ctx, map[string]interface{}{"received": true})
}
