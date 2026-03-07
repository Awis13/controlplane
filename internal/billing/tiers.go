package billing

// TierLimits defines resource limits for each billing tier.
type TierLimits struct {
	MaxStations    int    `json:"max_stations"`
	MaxPlatforms   int    `json:"max_platforms"`   // restream destinations per station
	MaxQuality     string `json:"max_quality"`     // "720p", "1080p", "4k"
	Watermark      bool   `json:"watermark"`
	DSP            bool   `json:"dsp"`
	CustomOverlays bool   `json:"custom_overlays"`
	Analytics      string `json:"analytics"` // "basic", "full"
	Storage        string `json:"storage"`   // "unlimited" for all tiers
}

// Valid tier names.
const (
	TierFree    = "free"
	TierStarter = "starter"
	TierPro     = "pro"
	TierStudio  = "studio"
)

// ValidTiers is the set of all valid tier names.
var ValidTiers = map[string]bool{
	TierFree:    true,
	TierStarter: true,
	TierPro:     true,
	TierStudio:  true,
}

// tierLimits maps tier name to its resource limits.
var tierLimits = map[string]TierLimits{
	TierFree: {
		MaxStations:    1,
		MaxPlatforms:   1,
		MaxQuality:     "720p",
		Watermark:      true,
		DSP:            false,
		CustomOverlays: false,
		Analytics:      "basic",
		Storage:        "unlimited",
	},
	TierStarter: {
		MaxStations:    1,
		MaxPlatforms:   1,
		MaxQuality:     "720p",
		Watermark:      false,
		DSP:            false,
		CustomOverlays: false,
		Analytics:      "basic",
		Storage:        "unlimited",
	},
	TierPro: {
		MaxStations:    1,
		MaxPlatforms:   3,
		MaxQuality:     "1080p",
		Watermark:      false,
		DSP:            true,
		CustomOverlays: true,
		Analytics:      "full",
		Storage:        "unlimited",
	},
	TierStudio: {
		MaxStations:    3,
		MaxPlatforms:   3,
		MaxQuality:     "4k",
		Watermark:      false,
		DSP:            true,
		CustomOverlays: true,
		Analytics:      "full",
		Storage:        "unlimited",
	},
}

// GetLimits returns the resource limits for a given tier.
// Falls back to free tier limits if tier is unknown.
func GetLimits(tier string) TierLimits {
	if limits, ok := tierLimits[tier]; ok {
		return limits
	}
	return tierLimits[TierFree]
}

// IsValidTier returns true if the tier name is recognized.
func IsValidTier(tier string) bool {
	return ValidTiers[tier]
}

// IsPaidTier returns true if the tier requires a Stripe subscription.
func IsPaidTier(tier string) bool {
	return tier != TierFree && ValidTiers[tier]
}
