package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"

	"github.com/m11s-io/terraform-provider-huggingface/internal/hfapi"
	"github.com/oapi-codegen/nullable"
)

// Repository describes model identity and visibility. RepoType must be model.
// URL is derived from the configured endpoint. No artifact contents are managed.
type Repository struct {
	RepoType, Namespace, Name, URL string
	Private                        bool
}

func (r Repository) ID() string     { return r.RepoType + "/" + r.Namespace + "/" + r.Name }
func (r Repository) RepoID() string { return r.Namespace + "/" + r.Name }

var segment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (r Repository) validate() error {
	if r.RepoType != "model" || !segment.MatchString(r.Namespace) || !segment.MatchString(r.Name) {
		return errors.New("repository requires repo_type model and nonempty namespace/name path segments containing letters, digits, dots, underscores or hyphens")
	}
	return nil
}
func (c *Client) repositoryURL(r Repository) string {
	return c.endpoint.String() + url.PathEscape(r.Namespace) + "/" + url.PathEscape(r.Name)
}

func (c *Client) GetRepository(ctx context.Context, r Repository) (Repository, error) {
	if err := r.validate(); err != nil {
		return Repository{}, err
	}
	rsp, err := c.api.GetModel(ctx, r.Namespace, r.Name)
	if err == nil && rsp.StatusCode == 404 {
		return Repository{}, c.confirmAbsence(ctx, r, rsp)
	}
	parsed, err := parse(c, "read repository", rsp, err, hfapi.ParseGetModelResponse)
	if err != nil {
		return Repository{}, err
	}
	if parsed.JSON200 == nil || !requiredJSON(parsed.Body, "id", "private") || parsed.JSON200.Id != r.RepoID() {
		return Repository{}, c.issue("read repository", rsp, "invalid model response identity or visibility")
	}
	r.Private = parsed.JSON200.Private
	r.URL = c.repositoryURL(r)
	return r, nil
}

// confirmAbsence deliberately recognizes only the tested owning-account legacy
// Write PAT authority. Organization membership, read/OIDC/fine-grained or unknown
// credentials cannot establish that a hidden private repo was actually deleted.
func (c *Client) confirmAbsence(ctx context.Context, r Repository, missing *http.Response) error {
	if missing.Body != nil {
		_ = missing.Body.Close()
	}
	rsp, err := c.api.WhoAmI(ctx)
	parsed, err := parse(c, "confirm repository absence", rsp, err, hfapi.ParseWhoAmIResponse)
	if err == nil && parsed.JSON200 != nil {
		identity, decodeErr := parsed.JSON200.AsWhoAmI200JSONResponseBody0()
		if decodeErr == nil && identity.Type == "user" && identity.Name == r.Namespace && identity.Auth.Type == "access_token" && identity.Auth.AccessToken != nil && identity.Auth.AccessToken.Role == "write" && identity.Auth.AccessToken.FineGrained == nil && identity.Auth.Resource == nil {
			e := c.issue("read repository", missing, "repository is confirmed absent")
			e.Kind = ErrNotFound
			return e
		}
	}
	e := c.issue("read repository", missing, "repository absence is ambiguous; verify credential access before removing state")
	e.Kind = ErrAmbiguousAbsence
	return e
}

func (c *Client) CreateRepository(ctx context.Context, r Repository) (Repository, error) {
	if err := r.validate(); err != nil {
		return Repository{}, err
	}
	repoType := hfapi.CreateRepoRequest1Type("model")
	body := hfapi.CreateRepoRequest{}
	if err := body.FromCreateRepoRequest1(hfapi.CreateRepoRequest1{Name: r.Name, Organization: nullable.NewNullableWithValue(r.Namespace), Private: nullable.NewNullableWithValue(r.Private), Type: &repoType}); err != nil {
		return Repository{}, errors.New("could not encode model create request")
	}
	rsp, err := c.api.CreateRepository(ctx, body)
	if err == nil && rsp.StatusCode == 409 {
		e := c.issue("create repository", rsp, "repository already exists; explicitly import it")
		e.Kind = ErrImportRequired
		e.ImportIDs = []string{c.safe(r.ID())}
		return Repository{}, e
	}
	parsed, parseErr := parse(c, "create repository", rsp, err, hfapi.ParseCreateRepositoryResponse)
	if parseErr == nil && (parsed.JSON200 == nil || parsed.JSON200.Name != r.RepoID() || parsed.JSON200.Id == "" || parsed.JSON200.Url == "" || !requiredJSON(parsed.Body, "id", "name", "url")) {
		parseErr = c.issue("create repository", rsp, "invalid create response")
	}
	if parseErr != nil {
		return Repository{}, c.createFailure(ctx, r, parseErr)
	}
	// The authoritative model read is also a post-create outcome check. Failure must
	// not return planned values as adopted state.
	result, err := c.GetRepository(ctx, r)
	if err != nil {
		e := c.issue("create repository", rsp, "created repository could not be verified")
		e.MayHaveSucceeded = true
		e.ImportIDs = []string{c.safe(r.ID())}
		return Repository{}, e
	}
	return result, nil
}
func (c *Client) createFailure(ctx context.Context, r Repository, err error) error {
	var e *Error
	if !errors.As(err, &e) {
		return err
	}
	if e.Status == 0 || e.Status == 200 || e.Status >= 500 || e.MayHaveSucceeded {
		e.MayHaveSucceeded = true
		// A read is advisory only: the remote object is never adopted.
		if _, readErr := c.GetRepository(ctx, r); readErr == nil {
			e.ImportIDs = []string{c.safe(r.ID())}
		}
	}
	return e
}

func (c *Client) UpdateRepository(ctx context.Context, r Repository) error {
	if err := r.validate(); err != nil {
		return err
	}
	rsp, err := c.api.UpdateModelSettings(ctx, r.Namespace, r.Name, hfapi.UpdateModelSettingsJSONRequestBody{Private: &r.Private})
	parsed, err := parse(c, "update repository", rsp, err, hfapi.ParseUpdateModelSettingsResponse)
	if err != nil {
		return err
	}
	if parsed.JSON200 == nil || !requiredJSON(parsed.Body, "private") || parsed.JSON200.Private == nil || *parsed.JSON200.Private != r.Private {
		return c.issue("update repository", rsp, "invalid visibility response")
	}
	return nil
}
func (c *Client) DeleteRepository(ctx context.Context, r Repository) error {
	if err := r.validate(); err != nil {
		return err
	}
	rsp, err := c.api.DeleteRepository(ctx, hfapi.DeleteRepoRequest{Name: r.Name, Organization: r.Namespace, Type: "model"})
	if err == nil && rsp.StatusCode == 404 {
		_ = rsp.Body.Close()
		_, lookupErr := c.GetRepository(ctx, r)
		if errors.Is(lookupErr, ErrNotFound) {
			return nil
		}
		if lookupErr != nil {
			return lookupErr
		}
		return c.issue("delete repository", rsp, "repository still exists after delete returned 404")
	}
	parsed, err := parse(c, "delete repository", rsp, err, hfapi.ParseDeleteRepositoryResponse)
	if err != nil {
		return err
	}
	if string(parsed.Body) != "OK" {
		return c.issue("delete repository", rsp, "invalid delete acknowledgement")
	}
	return nil
}

// Generated decoders intentionally do not enforce required/non-null fields.
// Validate their presence independently before returning successful domain data.
func requiredJSON(body []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return false
	}
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || string(value) == "null" {
			return false
		}
	}
	return true
}
