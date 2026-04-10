package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Hard upper bound for any TTL the CLI or refresh endpoint will issue.
// The root token itself lives 365d (see cliMintRoot / init broker), so a
// child cannot outlive its parent in UCAN semantics. We keep the ceiling
// at exactly one year so the CLI refuses anything it knows the validator
// will reject later — fail at mint time, not at first use.
const MaxChildTokenTTL = 365 * 24 * time.Hour

// Floor at one minute. Anything shorter is almost certainly a typo (e.g.
// "30" intended as seconds) and would leave peers in a permanent
// refresh-storm within moments of being issued.
const MinChildTokenTTL = time.Minute

// ParseFlexibleDuration accepts the Go-native duration syntax plus a plain
// "Nd" suffix for days, because the primary operator thinks in days and the
// default TTL lives in "720h"-land if we force pure time.ParseDuration.
//
// Accepted examples:
//
//	"24h"    -> 24 hours
//	"30d"    -> 720 hours
//	"72h30m" -> 72 hours 30 minutes
//	"1d12h"  -> 36 hours
//
// Returns an error for empty input, invalid syntax, or values outside
// [MinChildTokenTTL, MaxChildTokenTTL].
func ParseFlexibleDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// Walk the string and rewrite any "Nd" (where N is a run of digits)
	// into "N*24h", leaving the rest for time.ParseDuration. Runs once
	// left-to-right and preserves other units, so "1d12h" -> "24h12h"
	// which ParseDuration handles as 36h.
	var out strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		if c >= '0' && c <= '9' {
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j < len(s) && s[j] == 'd' {
				days, err := strconv.Atoi(s[i:j])
				if err != nil {
					return 0, fmt.Errorf("parse days: %w", err)
				}
				fmt.Fprintf(&out, "%dh", days*24)
				i = j + 1
				continue
			}
			out.WriteString(s[i:j])
			i = j
			continue
		}
		out.WriteByte(c)
		i++
	}

	d, err := time.ParseDuration(out.String())
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", s, err)
	}

	if d < MinChildTokenTTL {
		return 0, fmt.Errorf("ttl %s is below minimum %s", d, MinChildTokenTTL)
	}
	if d > MaxChildTokenTTL {
		return 0, fmt.Errorf("ttl %s exceeds maximum %s (root token lifetime)", d, MaxChildTokenTTL)
	}
	return d, nil
}

// defaultChildTTL returns the broker's configured default TTL for newly
// minted child tokens (used by /refresh-token and by issue-token when no
// --ttl flag is passed). Falls back to 24h to preserve the historical
// behaviour if the config is missing or unparseable. Callers that NEED to
// surface a parse error should call ParseFlexibleDuration directly.
func defaultChildTTL() time.Duration {
	if cfg.DefaultChildTTL == "" {
		return 24 * time.Hour
	}
	d, err := ParseFlexibleDuration(cfg.DefaultChildTTL)
	if err != nil {
		return 24 * time.Hour
	}
	return d
}
