package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// accountUsageCallTimeout bounds one _kiro/account/getUsage round-trip so a wedged RPC
// cannot pin its caller and the utility session lease.
const accountUsageCallTimeout = 45 * time.Second

// AccountUsage fetches account usage via the utility bridge, constructing it lazily so
// it works with no chat open. Uncached: the HTTP layer caches.
func (rt *Runtime) AccountUsage(ctx context.Context) (*marotte.AccountUsage, error) {
	cctx, cancel := context.WithTimeout(ctx, accountUsageCallTimeout)
	defer cancel()
	raw, err := rt.utility.get().session.accountUsageRaw(cctx)
	if err != nil {
		return nil, err
	}
	return parseAccountUsage(raw)
}

// kasUsageResult mirrors the KAS _kiro/account/getUsage reply ({success, message, data}).
type kasUsageResult struct {
	Data    *kasUsageData `json:"data"`
	Message string        `json:"message"`
	Success bool          `json:"success"`
}

type kasUsageData struct {
	PlanName          string              `json:"planName"`
	BillingCycleReset string              `json:"billingCycleReset"`
	UsageBreakdowns   []kasUsageBreakdown `json:"usageBreakdowns"`
	IsEnterprise      bool                `json:"isEnterprise"`
	OveragesEnabled   bool                `json:"overagesEnabled"`
}

type kasUsageBreakdown struct {
	ResourceType    string  `json:"resourceType"`
	DisplayName     string  `json:"displayName"`
	Currency        string  `json:"currency"`
	Used            float64 `json:"used"`
	Limit           float64 `json:"limit"`
	CurrentOverages float64 `json:"currentOverages"`
	OverageCharges  float64 `json:"overageCharges"`
	Percentage      int     `json:"percentage"`
	HasLimit        bool    `json:"hasLimit"`
}

// parseAccountUsage converts the KAS getUsage result. success=false is an error;
// success=true with nil data (admin-managed plan) yields only the note.
func parseAccountUsage(raw json.RawMessage) (*marotte.AccountUsage, error) {
	if len(raw) == 0 {
		return nil, errors.New("account usage: empty result")
	}
	var r kasUsageResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if !r.Success {
		msg := strings.TrimSpace(r.Message)
		if msg == "" {
			msg = "account usage unavailable"
		}
		return nil, errors.New(msg)
	}
	out := &marotte.AccountUsage{FetchedAt: time.Now().UTC().Format(time.RFC3339)}
	if r.Data == nil {
		out.Note = strings.TrimSpace(r.Message)
		out.Breakdowns = []marotte.AccountUsageBreakdown{}
		return out, nil
	}
	out.PlanName = r.Data.PlanName
	out.BillingCycleReset = r.Data.BillingCycleReset
	out.IsEnterprise = r.Data.IsEnterprise
	out.OveragesEnabled = r.Data.OveragesEnabled
	out.Breakdowns = make([]marotte.AccountUsageBreakdown, 0, len(r.Data.UsageBreakdowns))
	for _, b := range r.Data.UsageBreakdowns {
		out.Breakdowns = append(out.Breakdowns, marotte.AccountUsageBreakdown{
			ResourceType:    b.ResourceType,
			DisplayName:     b.DisplayName,
			Currency:        b.Currency,
			Used:            b.Used,
			Limit:           b.Limit,
			CurrentOverages: b.CurrentOverages,
			OverageCharges:  b.OverageCharges,
			Percentage:      b.Percentage,
			HasLimit:        b.HasLimit,
		})
	}
	return out, nil
}
