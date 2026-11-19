package client

import (
	"context"
	"net/http"
	"net/url"
)

// EventDependency is one source dependency between episodes (ADR 0038):
// EventID cites, copies or derives from DependsOn. Kind is cites, copies or
// derived_from; Signal is metadata, link, named_source or declared. Times are
// RFC 3339; parse them with [ParseTime]. RemovedAt is set on a dependency
// removed by hand. The JSON keys are the server's brain-native wire names.
type EventDependency struct {
	EventID   string `json:"episode_id"`
	DependsOn string `json:"depends_on"`
	Kind      string `json:"kind"`
	Signal    string `json:"signal"`
	Reference string `json:"reference"`
	CreatedAt string `json:"created_at"`
	CreatedBy string `json:"created_by"`
	RemovedAt string `json:"removed_at,omitempty"`
	RemovedBy string `json:"removed_by,omitempty"`
}

// PendingCitation is a citation that named no stored episode yet.
type PendingCitation struct {
	LookupKey string `json:"lookup_key"`
	CreatedAt string `json:"created_at"`
}

// EventDependencies is the document GET /v1/episodes/{id}/dependencies
// returns: the same as `mnemos dependencies --json` and the
// episode_dependencies MCP tool.
type EventDependencies struct {
	EventID          string            `json:"episode_id"`
	DependsOn        []EventDependency `json:"depends_on"`
	Dependents       []EventDependency `json:"dependents"`
	PendingCitations []PendingCitation `json:"pending_citations"`
}

// EventDependencyChange is what declaring or removing a dependency did: the
// stored row, the beliefs of the dependent episode that were rescored, and
// whether the source episode is retracted (the edit never retracts or
// un-retracts anything).
type EventDependencyChange struct {
	Dependency      EventDependency `json:"dependency"`
	RescoredClaims  []string        `json:"rescored_beliefs"`
	SourceRetracted bool            `json:"source_retracted"`
}

// EventDependencies lists eventID's source dependencies. GET
// /v1/episodes/{id}/dependencies; includeRemoved also returns dependencies
// removed by hand. An unknown event, or one outside the token's runs, is an
// *[APIError] with Status 404. A disabled client returns an empty listing.
func (c *Client) EventDependencies(ctx context.Context, eventID string, includeRemoved bool) (*EventDependencies, error) {
	path := "/v1/episodes/" + url.PathEscape(eventID) + "/dependencies"
	if includeRemoved {
		path += "?include_removed=true"
	}
	var out EventDependencies
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeclareEventDependency records that eventID depends on dependsOn (kind is
// cites, copies or derived_from; note is optional). POST
// /v1/episodes/{id}/dependencies; needs claims:write. An unknown event is
// Status 404, a bad kind, note or pair 400, and a pair with a live dependency
// 409. A disabled client returns an empty change.
func (c *Client) DeclareEventDependency(ctx context.Context, eventID, dependsOn, kind, note string) (*EventDependencyChange, error) {
	body := map[string]string{"depends_on": dependsOn, "kind": kind}
	if note != "" {
		body["note"] = note
	}
	var out EventDependencyChange
	if err := c.do(ctx, http.MethodPost, "/v1/episodes/"+url.PathEscape(eventID)+"/dependencies", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveEventDependency removes eventID's dependency on dependsOn, keeping it
// as a tombstone detection cannot undo. DELETE
// /v1/episodes/{id}/dependencies/{depends_on}; needs claims:write. A pair with
// no live dependency is Status 404. A disabled client returns an empty change.
func (c *Client) RemoveEventDependency(ctx context.Context, eventID, dependsOn string) (*EventDependencyChange, error) {
	var out EventDependencyChange
	if err := c.do(ctx, http.MethodDelete, "/v1/episodes/"+url.PathEscape(eventID)+"/dependencies/"+url.PathEscape(dependsOn), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
