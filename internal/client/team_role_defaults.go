package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
)

type TeamRoleDefaultsInput struct {
	Members []string `json:"members"`
	Leads   []string `json:"leads"`
}

// TeamRoleDefaults is the tenant's complete live team member/lead role policy.
// Committed survives malformed successful PUT responses. Revision is retained
// only when its body value and strong ETag prove the policy's own revision.
type TeamRoleDefaults struct {
	TeamRoleDefaultsInput
	Revision  int64 `json:"revision"`
	Committed bool  `json:"-"`
}

func validTeamRoleDefaults(input TeamRoleDefaultsInput) bool {
	for _, values := range [][]string{input.Members, input.Leads} {
		if values == nil {
			return false
		}
		seen := make(map[string]bool, len(values))
		for _, id := range values {
			if !validResponseID(id) || seen[id] {
				return false
			}
			seen[id] = true
		}
	}
	return true
}

func sameTeamRoleSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func (c *Client) teamRoleDefaultsRequest(ctx context.Context, method string, revision int64, input *TeamRoleDefaultsInput) (TeamRoleDefaults, error) {
	var out TeamRoleDefaults
	var body io.Reader
	headers := make(http.Header)
	if input != nil {
		if revision <= 0 || revision == math.MaxInt64 || !validTeamRoleDefaults(*input) {
			return out, errors.New("team role defaults require both explicit UUID sets and a positive current policy revision; refresh before retrying")
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return out, errors.New("could not encode OnPager team role defaults")
		}
		body = bytes.NewReader(raw)
		headers.Set("If-Match", fmt.Sprintf(`"%d"`, revision))
	}
	res, err := c.do(ctx, method, "/v1/rbac/team-defaults", body, headers)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	out.Committed = success && method == http.MethodPut
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil {
		return out, errors.New("could not read OnPager response")
	}
	if len(raw) > 4<<20 {
		return out, errors.New("OnPager response exceeds the 4 MiB client limit")
	}
	if !success {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		code := envelope.Error.Code
		if !safeCode.MatchString(code) {
			code = ""
		}
		return out, &APIError{StatusCode: res.StatusCode, Code: code}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return out, errors.New("OnPager returned invalid team role defaults JSON")
	}
	// Decode the revision independently: a malformed set cannot hide an already
	// committed, independently verified revision needed for later safe recovery.
	var ownRevision int64
	if json.Unmarshal(fields["revision"], &ownRevision) == nil && ownRevision > 0 && res.Header.Get("ETag") == fmt.Sprintf(`"%d"`, ownRevision) && (input == nil || ownRevision == revision+1) {
		out.Revision = ownRevision
	}
	if res.StatusCode != http.StatusOK {
		return out, errors.New("OnPager returned an unexpected team role defaults success status")
	}
	if out.Revision <= 0 {
		return out, errors.New("OnPager did not confirm the policy's own positive revision and strong numeric ETag")
	}
	if json.Unmarshal(fields["members"], &out.Members) != nil || json.Unmarshal(fields["leads"], &out.Leads) != nil || !validTeamRoleDefaults(out.TeamRoleDefaultsInput) {
		return out, errors.New("OnPager returned incomplete or invalid team role defaults UUID sets")
	}
	if input != nil && (!sameTeamRoleSet(out.Members, input.Members) || !sameTeamRoleSet(out.Leads, input.Leads)) {
		return out, errors.New("OnPager did not confirm the requested complete team role defaults")
	}
	return out, nil
}

func (c *Client) GetTeamRoleDefaults(ctx context.Context) (TeamRoleDefaults, error) {
	return c.teamRoleDefaultsRequest(ctx, http.MethodGet, 0, nil)
}

func (c *Client) ReplaceTeamRoleDefaults(ctx context.Context, revision int64, input TeamRoleDefaultsInput) (TeamRoleDefaults, error) {
	return c.teamRoleDefaultsRequest(ctx, http.MethodPut, revision, &input)
}
