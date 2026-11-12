package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Enrichment has a 10 MiB content limit. Its separate bounded transport allows
// response envelopes without increasing the limit for unrelated API families.
func (c *Client) enrichmentRequest(ctx context.Context, method, route string, input, output any) (bool, error) {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil || len(b) > 10<<20 {
			return false, errors.New("enrichment request exceeds the 10 MiB API limit or cannot be encoded")
		}
		body = bytes.NewReader(b)
	}
	res, err := c.do(ctx, method, route, body, nil)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, errors.New("OnPager request failed; check endpoint, connectivity and TLS configuration")
	}
	defer res.Body.Close()
	success := res.StatusCode >= 200 && res.StatusCode < 300
	committed := success && method != http.MethodGet
	b, err := io.ReadAll(io.LimitReader(res.Body, (12<<20)+1))
	if err != nil {
		return committed, errors.New("could not read OnPager response")
	}
	if len(b) > 12<<20 {
		return committed, errors.New("OnPager enrichment response exceeds the 12 MiB client limit")
	}
	if !success {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &envelope)
		code := envelope.Error.Code
		if !safeCode.MatchString(code) {
			code = ""
		}
		return false, &APIError{StatusCode: res.StatusCode, Code: code}
	}
	if method == http.MethodDelete && (res.StatusCode != http.StatusNoContent || len(bytes.TrimSpace(b)) != 0) {
		return committed, errors.New("OnPager did not confirm enrichment dataset deletion with an empty HTTP 204 response")
	}
	if output != nil && json.Unmarshal(b, output) != nil {
		return committed, errors.New("OnPager returned an invalid enrichment JSON response")
	}
	return committed, nil
}

type EnrichmentDatasetInput struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Enabled     bool                `json:"enabled"`
	Rows        []map[string]string `json:"rows"`
}
type EnrichmentDatasetUpdate struct {
	Description *string `json:"description,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
}
type EnrichmentDatasetRevision struct {
	DatasetID string `json:"dataset_id"`
	Revision  int64  `json:"revision"`
	RowCount  *int64 `json:"row_count"`
}
type EnrichmentDataset struct {
	ID                   string                     `json:"id"`
	Name                 string                     `json:"name"`
	Description          string                     `json:"description"`
	Enabled              bool                       `json:"enabled"`
	ConfigurationVersion int64                      `json:"configuration_version"`
	CurrentRevision      int64                      `json:"current_revision"`
	Revision             *EnrichmentDatasetRevision `json:"revision"`
	// Committed is set for successful mutation statuses, even for invalid bodies.
	Committed bool `json:"-"`
	complete  bool
}

func (v *EnrichmentDataset) UnmarshalJSON(data []byte) error {
	type plain EnrichmentDataset
	var p plain
	err := json.Unmarshal(data, &p)
	*v = EnrichmentDataset(p)
	if err != nil {
		return err
	}
	var present struct {
		Description       *string `json:"description"`
		Enabled           *bool   `json:"enabled"`
		HasPublisherToken *bool   `json:"has_publisher_token"`
	}
	if err = json.Unmarshal(data, &present); err != nil {
		return err
	}
	v.complete = present.Description != nil && present.Enabled != nil && present.HasPublisherToken != nil
	return nil
}

var enrichmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,127}$`)

func enrichmentText(v string, limit int) bool {
	return len(v) <= limit && utf8.ValidString(v) && !strings.ContainsRune(v, 0)
}
func enrichmentDatasetPath(id string) string { return "/v1/enrichment/datasets/" + url.PathEscape(id) }
func validateEnrichmentDataset(v EnrichmentDataset, id string, detail bool) error {
	if !v.complete || !validResponseID(v.ID) || (id != "" && v.ID != id) || !enrichmentNamePattern.MatchString(v.Name) || !enrichmentText(v.Description, 4096) || v.ConfigurationVersion <= 0 || v.CurrentRevision <= 0 {
		return errors.New("OnPager returned incomplete or unexpected enrichment dataset metadata")
	}
	if detail && (v.Revision == nil || v.Revision.DatasetID != v.ID || v.Revision.Revision != v.CurrentRevision || v.Revision.RowCount == nil || *v.Revision.RowCount < 0 || *v.Revision.RowCount > 100000) {
		return errors.New("OnPager returned incomplete or unexpected enrichment dataset revision")
	}
	return nil
}
func (c *Client) CreateEnrichmentDataset(ctx context.Context, in EnrichmentDatasetInput) (EnrichmentDataset, error) {
	var out EnrichmentDataset
	if !enrichmentNamePattern.MatchString(in.Name) || !enrichmentText(in.Description, 4096) || !validEnrichmentRows(in.Rows) {
		return out, errors.New("invalid enrichment dataset metadata or rows")
	}
	committed, err := c.enrichmentRequest(ctx, http.MethodPost, "/v1/enrichment/datasets", in, &out)
	out.Committed = committed
	if err == nil {
		err = validateEnrichmentDataset(out, "", true)
	}
	if err == nil && (out.Name != in.Name || out.Description != in.Description || out.Enabled != in.Enabled || out.ConfigurationVersion != 1 || out.CurrentRevision != 1) {
		err = errors.New("OnPager did not confirm the initial enrichment dataset configuration")
	}
	return out, err
}
func (c *Client) GetEnrichmentDataset(ctx context.Context, id string) (EnrichmentDataset, error) {
	var out EnrichmentDataset
	_, err := c.enrichmentRequest(ctx, http.MethodGet, enrichmentDatasetPath(id), nil, &out)
	if err == nil {
		err = validateEnrichmentDataset(out, id, true)
	}
	return out, err
}
func (c *Client) UpdateEnrichmentDataset(ctx context.Context, id string, version int64, in EnrichmentDatasetUpdate) (EnrichmentDataset, error) {
	var out EnrichmentDataset
	if version <= 0 || (in.Description == nil && in.Enabled == nil) || (in.Description != nil && !enrichmentText(*in.Description, 4096)) {
		return out, errors.New("dataset update requires its current positive configuration_version and changed metadata")
	}
	body := struct {
		ExpectedVersion int64 `json:"expected_version"`
		EnrichmentDatasetUpdate
	}{version, in}
	committed, err := c.enrichmentRequest(ctx, http.MethodPatch, enrichmentDatasetPath(id), body, &out)
	out.Committed = committed
	if err == nil {
		err = validateEnrichmentDataset(out, id, false)
	}
	if err == nil && (out.ConfigurationVersion != version+1 || (in.Description != nil && out.Description != *in.Description) || (in.Enabled != nil && out.Enabled != *in.Enabled)) {
		err = errors.New("OnPager did not confirm the requested enrichment dataset metadata")
	}
	return out, err
}
func (c *Client) PublishEnrichmentDataset(ctx context.Context, id string, version int64, rows []map[string]string) (EnrichmentDataset, error) {
	var out EnrichmentDataset
	if version <= 0 || !validEnrichmentRows(rows) {
		return out, errors.New("dataset publication requires its current positive configuration_version and valid rows")
	}
	body := struct {
		ExpectedVersion int64               `json:"expected_version"`
		Rows            []map[string]string `json:"rows"`
	}{version, rows}
	committed, err := c.enrichmentRequest(ctx, http.MethodPost, enrichmentDatasetPath(id)+"/revisions", body, &out)
	out.Committed = committed
	if err == nil {
		err = validateEnrichmentDataset(out, id, true)
	}
	if err == nil && out.ConfigurationVersion != version+1 {
		err = errors.New("OnPager did not confirm the committed enrichment configuration_version")
	}
	return out, err
}
func (c *Client) DeleteEnrichmentDataset(ctx context.Context, id string, version int64) error {
	if version <= 0 {
		return errors.New("dataset deletion requires its current positive configuration_version; refresh before retrying")
	}
	_, err := c.enrichmentRequest(ctx, http.MethodDelete, enrichmentDatasetPath(id), struct {
		ExpectedVersion int64 `json:"expected_version"`
	}{version}, nil)
	return allowAbsent(err)
}
func validEnrichmentRows(rows []map[string]string) bool {
	if rows == nil || len(rows) > 100000 {
		return false
	}
	fields := map[string]bool{}
	for _, row := range rows {
		if row == nil {
			return false
		}
		for k, v := range row {
			if k == "" || !enrichmentText(k, 256) || !enrichmentText(v, 4096) {
				return false
			}
			fields[k] = true
		}
		if len(fields) > 256 {
			return false
		}
	}
	b, err := json.Marshal(rows)
	return err == nil && len(b) <= 10<<20
}

// ValidateEnrichmentRows checks content bounds without issuing an API request.
func ValidateEnrichmentRows(rows []map[string]string) error {
	if !validEnrichmentRows(rows) {
		return errors.New("rows must be an explicit set of at most 100,000 string maps, 256 distinct fields (1–256 UTF-8 bytes), 4096 bytes per value and 10 MiB JSON, without null values or NUL")
	}
	return nil
}

// GetEnrichmentDatasetRows reads only the selected immutable revision. The known
// row count and contiguous positions detect truncated or looping pagination.
func (c *Client) GetEnrichmentDatasetRows(ctx context.Context, id string, revision, count int64) ([]map[string]string, error) {
	if revision <= 0 || count < 0 || count > 100000 {
		return nil, errors.New("invalid enrichment row revision or count")
	}
	rows := make([]map[string]string, 0, count)
	fields := map[string]bool{}
	encodedBytes := 2 // The canonical JSON array's opening and closing brackets.
	for {
		var page struct {
			Items []struct {
				Position *int64          `json:"row_position"`
				Data     json.RawMessage `json:"data"`
			} `json:"items"`
			HasMore *bool `json:"has_more"`
		}
		route := enrichmentDatasetPath(id) + "/revisions/" + strconv.FormatInt(revision, 10) + "/rows?limit=200&offset=" + strconv.Itoa(len(rows))
		if _, err := c.enrichmentRequest(ctx, http.MethodGet, route, nil, &page); err != nil {
			return nil, err
		}
		if page.Items == nil || page.HasMore == nil || len(page.Items) > 200 || (len(page.Items) == 0 && *page.HasMore) {
			return nil, errors.New("OnPager returned incomplete enrichment row pagination")
		}
		for _, item := range page.Items {
			if item.Position == nil || *item.Position != int64(len(rows)) || int64(len(rows)) >= count {
				return nil, errors.New("OnPager returned unexpected enrichment row positions or count")
			}
			var raw map[string]json.RawMessage
			if !utf8.Valid(item.Data) || json.Unmarshal(item.Data, &raw) != nil || raw == nil {
				return nil, errors.New("OnPager returned an invalid enrichment row")
			}
			row := map[string]string{}
			for k, v := range raw {
				var s *string
				if json.Unmarshal(v, &s) != nil || s == nil {
					return nil, errors.New("OnPager returned a non-string enrichment row value")
				}
				if k == "" || !enrichmentText(k, 256) || !enrichmentText(*s, 4096) {
					return nil, errors.New("OnPager returned an enrichment row field or value outside the API text limits")
				}
				fields[k] = true
				if len(fields) > 256 {
					return nil, errors.New("OnPager enrichment dataset exceeds 256 distinct fields")
				}
				row[k] = *s
			}
			encoded, err := json.Marshal(row)
			if err != nil {
				return nil, errors.New("OnPager returned an invalid enrichment row")
			}
			rowBytes := len(encoded)
			if len(rows) > 0 {
				rowBytes++ // Array element separator.
			}
			if rowBytes > (10<<20)-encodedBytes {
				return nil, errors.New("OnPager enrichment dataset exceeds the 10 MiB content limit")
			}
			encodedBytes += rowBytes
			rows = append(rows, row)
		}
		if !*page.HasMore {
			break
		}
		if int64(len(rows)) >= count {
			return nil, errors.New("OnPager enrichment pagination exceeds the selected revision row count")
		}
	}
	if int64(len(rows)) != count {
		return nil, errors.New("OnPager returned incomplete or invalid enrichment revision rows")
	}
	return rows, nil
}

type EnrichmentRule struct {
	ID        string              `json:"id"`
	DatasetID string              `json:"dataset_id"`
	Lookup    []map[string]string `json:"lookup"`
	Emit      map[string]string   `json:"emit"`
}
type EnrichmentRules struct {
	Revision        int64            `json:"revision"`
	Rules           []EnrichmentRule `json:"rules"`
	Committed       bool             `json:"-"`
	RevisionPresent bool             `json:"-"`
}

func (v *EnrichmentRules) UnmarshalJSON(b []byte) error {
	type plain EnrichmentRules
	var p plain
	err := json.Unmarshal(b, &p)
	*v = EnrichmentRules(p)
	var present struct {
		Revision *int64 `json:"revision"`
	}
	if json.Unmarshal(b, &present) == nil {
		v.RevisionPresent = present.Revision != nil
	}
	return err
}

// ValidateEnrichmentRules validates the API's structural bounds without resolving
// datasets, previewing content or mutating the current configuration.
func ValidateEnrichmentRules(rules []EnrichmentRule) error {
	if rules == nil || len(rules) > 20 {
		return errors.New("rules must be an explicit ordered list of at most 20 rules")
	}
	b, err := json.Marshal(rules)
	if err != nil || len(b) > 256<<10 {
		return errors.New("enrichment rules exceed the 256 KiB API limit")
	}
	ids := map[string]bool{}
	for _, r := range rules {
		if !validResponseID(r.ID) || !validResponseID(r.DatasetID) || ids[r.ID] || len(r.Lookup) < 1 || len(r.Lookup) > 16 || len(r.Emit) < 1 || len(r.Emit) > 16 {
			return errors.New("each enrichment rule needs a unique UUID, dataset UUID, 1–16 lookup alternatives and 1–16 emitted labels")
		}
		ids[r.ID] = true
		for k, v := range r.Emit {
			if k == "" || v == "" || !enrichmentText(k, 256) || !enrichmentText(v, 256) {
				return errors.New("enrichment emit keys and dataset fields must be nonempty and at most 256 bytes")
			}
		}
		for _, alt := range r.Lookup {
			if len(alt) < 1 || len(alt) > 8 {
				return errors.New("each lookup alternative must have 1–8 dataset-field references")
			}
			for field, ref := range alt {
				ns, key, ok := strings.Cut(ref, ".")
				if !ok || field == "" || key == "" || !enrichmentText(field, 256) || !enrichmentText(key, 256) || (ns != "labels" && ns != "enriched_labels") {
					return errors.New("lookup references must use labels.key or enriched_labels.key with keys and fields of at most 256 bytes")
				}
			}
		}
	}
	return nil
}
func (c *Client) GetEnrichmentRules(ctx context.Context) (EnrichmentRules, error) {
	var out EnrichmentRules
	_, err := c.enrichmentRequest(ctx, http.MethodGet, "/v1/enrichment/rules", nil, &out)
	if err == nil && (!out.RevisionPresent || out.Revision < 0) {
		err = errors.New("OnPager returned an incomplete enrichment rules revision")
	}
	if err == nil {
		err = ValidateEnrichmentRules(out.Rules)
	}
	return out, err
}
func (c *Client) PublishEnrichmentRules(ctx context.Context, revision int64, rules []EnrichmentRule) (EnrichmentRules, error) {
	var out EnrichmentRules
	if revision < 0 {
		return out, errors.New("rules publication requires a nonnegative current revision")
	}
	if err := ValidateEnrichmentRules(rules); err != nil {
		return out, err
	}
	body := struct {
		ExpectedRevision int64            `json:"expected_revision"`
		Rules            []EnrichmentRule `json:"rules"`
	}{revision, rules}
	committed, err := c.enrichmentRequest(ctx, http.MethodPost, "/v1/enrichment/rules/revisions", body, &out)
	out.Committed = committed
	if err == nil && (!out.RevisionPresent || out.Revision != revision+1) {
		err = errors.New("OnPager did not confirm the committed enrichment rules revision")
	}
	if err == nil {
		err = ValidateEnrichmentRules(out.Rules)
	}
	if err == nil && !reflect.DeepEqual(out.Rules, rules) {
		err = errors.New("OnPager did not confirm the requested ordered enrichment rules snapshot")
	}
	return out, err
}
