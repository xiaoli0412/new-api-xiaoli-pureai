package dto

const RelayPricingContractV1 = "v1"

type RelayPricingContract struct {
	ContractVersion  string                            `json:"contract_version"`
	SnapshotRevision int64                             `json:"snapshot_revision"`
	QuotaPerUnit     string                            `json:"quota_per_unit"`
	UpstreamGroup    string                            `json:"upstream_group,omitempty"`
	GroupRatio       string                            `json:"group_ratio,omitempty"`
	GroupRatios      map[string]string                 `json:"group_ratios,omitempty"`
	Pricing          RelayPricingInputs                `json:"pricing"`
	Models           map[string]RelayPricingModel      `json:"models"`
	Capabilities     map[string]RelayPricingCapability `json:"capabilities"`
}

type RelayPricingInputs struct {
	ModelRatio           map[string]string `json:"model_ratio"`
	CompletionRatio      map[string]string `json:"completion_ratio"`
	CacheRatio           map[string]string `json:"cache_ratio"`
	CreateCacheRatio     map[string]string `json:"create_cache_ratio"`
	CreateCache5mRatio   map[string]string `json:"create_cache_5m_ratio"`
	CreateCache1hRatio   map[string]string `json:"create_cache_1h_ratio"`
	ImageRatio           map[string]string `json:"image_ratio"`
	AudioRatio           map[string]string `json:"audio_ratio"`
	AudioCompletionRatio map[string]string `json:"audio_completion_ratio"`
	ModelPrice           map[string]string `json:"model_price"`
	BillingMode          map[string]string `json:"billing_mode"`
	BillingExpr          map[string]string `json:"billing_expr"`
}

type RelayPricingCapability struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type RelayPricingModel struct {
	BillingMode          string  `json:"billing_mode"`
	ChargeMode           string  `json:"charge_mode"`
	CostEstimationStatus string  `json:"cost_estimation_status"`
	ModelRatio           *string `json:"model_ratio,omitempty"`
	CompletionRatio      *string `json:"completion_ratio,omitempty"`
	CacheRatio           *string `json:"cache_ratio,omitempty"`
	CreateCache5mRatio   *string `json:"create_cache_5m_ratio,omitempty"`
	CreateCache1hRatio   *string `json:"create_cache_1h_ratio,omitempty"`
	ImageRatio           *string `json:"image_ratio,omitempty"`
	AudioRatio           *string `json:"audio_ratio,omitempty"`
	AudioCompletionRatio *string `json:"audio_completion_ratio,omitempty"`
	ModelPrice           *string `json:"model_price,omitempty"`
	BillingExpr          *string `json:"billing_expr,omitempty"`
	BillingExprVersion   *int    `json:"billing_expr_version,omitempty"`
}
