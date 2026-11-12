// Package geo turns Nadeo zone names into country flag emoji.
package geo

import "strings"

// CountryFlag turns a Nadeo zone string such as
// "World|Europe|France|Ile-de-France" into the French flag emoji.
func CountryFlag(zone string) string {
	country := countryFromZone(zone)
	if country == "" {
		return ""
	}
	if code, ok := countryCodes[normalizeCountry(country)]; ok {
		return flagFromISO(code)
	}
	// Some responses may already carry a two-letter country code.
	if len(country) == 2 {
		return flagFromISO(country)
	}
	return ""
}

func countryFromZone(zone string) string {
	zone = strings.TrimSpace(zone)
	if zone == "" {
		return ""
	}
	parts := strings.Split(zone, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	// Nadeo zones are hierarchical: World|Continent|Country|Region...
	if len(parts) >= 3 && parts[2] != "" {
		return parts[2]
	}
	if len(parts) == 2 && parts[1] != "" {
		return parts[1]
	}
	return parts[0]
}

func normalizeCountry(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.Trim(name, ".")
	return strings.Join(strings.Fields(name), " ")
}

// flagFromISO builds a regional-indicator flag from an ISO 3166-1 alpha-2
// country code.
func flagFromISO(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return ""
	}
	return string([]rune{
		rune(code[0]-'A') + 0x1F1E6,
		rune(code[1]-'A') + 0x1F1E6,
	})
}
