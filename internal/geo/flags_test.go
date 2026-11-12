package geo

import "testing"

func TestCountryFlag(t *testing.T) {
	tests := map[string]string{
		"World|Europe|France|Ile-de-France":            "🇫🇷",
		"World|Europe|United Kingdom|England":          "🇬🇧",
		"World|North America|United States|California": "🇺🇸",
		"World|Asia|South Korea|Seoul":                 "🇰🇷",
		"World|Europe|Czech Republic|Prague":           "🇨🇿",
		"World|Europe|Türkiye|Istanbul":                "🇹🇷",
		"World|Europe|Atlantis|Nowhere":                "",
		"World|Europe|France":                          "🇫🇷",
		"":                                             "",
	}
	for zone, want := range tests {
		if got := CountryFlag(zone); got != want {
			t.Errorf("CountryFlag(%q) = %q, want %q", zone, got, want)
		}
	}
}
