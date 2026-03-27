package targetprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxPageSize           = 1000
	defaultIncludeFields  = "[Id,Name,Description,EntityType,EntityState,Project,AssignedUser,CreateDate,ModifyDate,Tags,Priority,NumericPriority]"
	defaultResultIncludes = "[Id,Name,Description,EntityType,EntityState,Project,AssignedUser,CreateDate,ModifyDate,Tags,Priority,NumericPriority]"
	v2SelectFields        = "{id,name,description,createDate,modifyDate,numericPriority,entityType:{entityType.name},entityState:{entityState.id,entityState.name,entityState.isFinal,entityState.isInitial},project:{project.id,project.name},assignedUser:{assignedUser.id,assignedUser.login,assignedUser.email,assignedUser.firstName,assignedUser.lastName},tags:tagObjects.select(name)}"
)

// Client wraps the Targetprocess v1 REST API.
type Client struct {
	baseURL     string
	httpClient  *http.Client
	accessToken string
	token       string
	username    string
	password    string
}

// EntityRef represents a lightweight Targetprocess entity reference.
type EntityRef struct {
	ResourceType string `json:"ResourceType,omitempty"`
	ID           int    `json:"Id"`
	Name         string `json:"Name,omitempty"`
}

// EntityState represents a workflow state in Targetprocess.
type EntityState struct {
	EntityRef
	IsFinal   bool `json:"IsFinal"`
	IsInitial bool `json:"IsInitial"`
}

// User represents a Targetprocess user.
type User struct {
	EntityRef
	Login     string `json:"Login,omitempty"`
	Email     string `json:"Email,omitempty"`
	FirstName string `json:"FirstName,omitempty"`
	LastName  string `json:"LastName,omitempty"`
}

// Assignable is the subset of fields beads needs from Targetprocess work items.
type Assignable struct {
	ResourceType    string       `json:"ResourceType,omitempty"`
	ID              int          `json:"Id"`
	Name            string       `json:"Name"`
	Description     string       `json:"Description"`
	CreateDate      string       `json:"CreateDate"`
	ModifyDate      string       `json:"ModifyDate"`
	Tags            string       `json:"Tags"`
	NumericPriority float64      `json:"NumericPriority"`
	Priority        *EntityRef   `json:"Priority"`
	EntityType      *EntityRef   `json:"EntityType"`
	EntityState     *EntityState `json:"EntityState"`
	Project         *EntityRef   `json:"Project"`
	AssignedUser    *User        `json:"AssignedUser"`
}

type collectionResponse[T any] struct {
	Items []T    `json:"Items"`
	Next  string `json:"Next"`
}

type v2CollectionResponse[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next"`
}

type v2EntityRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type v2EntityState struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	IsFinal   bool   `json:"isFinal"`
	IsInitial bool   `json:"isInitial"`
}

type v2User struct {
	ID        int    `json:"id"`
	Login     string `json:"login"`
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

type v2Assignable struct {
	ID              int            `json:"id"`
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	CreateDate      string         `json:"createDate"`
	ModifyDate      string         `json:"modifyDate"`
	NumericPriority float64        `json:"numericPriority"`
	EntityType      *v2EntityRef   `json:"entityType"`
	EntityState     *v2EntityState `json:"entityState"`
	Project         *v2EntityRef   `json:"project"`
	AssignedUser    *v2User        `json:"assignedUser"`
	Tags            []string       `json:"tags"`
}

type loggedUser struct {
	ID    int    `json:"Id"`
	Login string `json:"Login"`
}

// NewClient creates a Targetprocess API client.
func NewClient(baseURL, accessToken, token, username, password string) *Client {
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		accessToken: accessToken,
		token:       token,
		username:    username,
		password:    password,
	}
}

func (c *Client) Validate(ctx context.Context) error {
	var user loggedUser
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/Users/LoggedUser/", nil, nil, &user); err != nil {
		return err
	}
	if user.ID == 0 {
		return fmt.Errorf("targetprocess validation failed: empty logged user response")
	}
	return nil
}

func (c *Client) FetchEntities(ctx context.Context, resource string, projectID int, state string, since *time.Time, extraWhere string, limit int) ([]Assignable, error) {
	var (
		items []Assignable
		skip  int
	)

	for {
		take := maxPageSize
		if limit > 0 {
			remaining := limit - len(items)
			if remaining <= 0 {
				break
			}
			if remaining < take {
				take = remaining
			}
		}

		params := url.Values{}
		params.Set("take", strconv.Itoa(take))
		params.Set("skip", strconv.Itoa(skip))
		params.Set("include", defaultIncludeFields)

		where := buildWhereClause(projectID, state, since, extraWhere)
		if where != "" {
			params.Set("where", where)
		}

		var page collectionResponse[Assignable]
		if err := c.doJSON(ctx, http.MethodGet, "/api/v1/"+resource, params, nil, &page); err != nil {
			return nil, err
		}

		items = append(items, page.Items...)
		if len(page.Items) < take {
			break
		}
		skip += take
	}

	return items, nil
}

func (c *Client) FetchEntitiesV2(ctx context.Context, entity string, projectID int, state string, since *time.Time, extraWhere string, limit int) ([]Assignable, error) {
	var (
		items []Assignable
		skip  int
	)

	path := v2EntityPath(entity)

	for {
		take := maxPageSize
		if limit > 0 {
			remaining := limit - len(items)
			if remaining <= 0 {
				break
			}
			if remaining < take {
				take = remaining
			}
		}

		params := url.Values{}
		params.Set("take", strconv.Itoa(take))
		params.Set("skip", strconv.Itoa(skip))
		params.Set("select", v2SelectFields)
		params.Set("orderBy", "modifyDate desc")

		where := buildWhereClauseV2(projectID, state, since, extraWhere)
		if where != "" {
			params.Set("where", where)
		}

		var page v2CollectionResponse[v2Assignable]
		if err := c.doJSON(ctx, http.MethodGet, path, params, nil, &page); err != nil {
			return nil, err
		}

		for i := range page.Items {
			items = append(items, normalizeV2Assignable(entity, &page.Items[i]))
		}

		if len(page.Items) < take {
			break
		}
		skip += take
	}

	return items, nil
}

func (c *Client) FetchAssignable(ctx context.Context, id int) (*Assignable, error) {
	var item Assignable
	params := url.Values{}
	params.Set("include", defaultIncludeFields)
	if err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v1/Assignables/%d", id), params, nil, &item); err != nil {
		return nil, err
	}
	if item.ID == 0 {
		return nil, nil
	}
	return &item, nil
}

func (c *Client) CreateIssue(ctx context.Context, entityType string, payload map[string]interface{}) (*Assignable, error) {
	resource, err := resourceForEntityType(entityType)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("resultInclude", defaultResultIncludes)

	var item Assignable
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/"+resource, params, payload, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

func (c *Client) UpdateAssignable(ctx context.Context, id int, payload map[string]interface{}) (*Assignable, error) {
	params := url.Values{}
	params.Set("resultInclude", defaultResultIncludes)

	var item Assignable
	if err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/Assignables/%d", id), params, payload, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

func (c *Client) FetchEntityStates(ctx context.Context, entityType string) ([]EntityState, error) {
	params := url.Values{}
	params.Set("take", strconv.Itoa(maxPageSize))
	params.Set("include", "[Id,Name,IsFinal,IsInitial]")
	params.Set("where", fmt.Sprintf("(Workflow.EntityType.Name eq '%s')", entityType))

	var page collectionResponse[EntityState]
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/EntityStates", params, nil, &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, params url.Values, payload interface{}, out interface{}) error {
	req, err := c.newRequest(ctx, method, path, params, payload)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("targetprocess %s %s failed: %s: %s", method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}

	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding targetprocess response: %w", err)
	}

	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, params url.Values, payload interface{}) (*http.Request, error) {
	requestURL, err := c.resolveURL(path)
	if err != nil {
		return nil, err
	}

	query := requestURL.Query()
	for key, values := range params {
		query.Del(key)
		for _, value := range values {
			query.Add(key, value)
		}
	}
	if query.Get("format") == "" {
		query.Set("format", "json")
	}
	if c.accessToken != "" && query.Get("access_token") == "" {
		query.Set("access_token", c.accessToken)
	}
	if c.token != "" && c.accessToken == "" && query.Get("token") == "" {
		query.Set("token", c.token)
	}
	requestURL.RawQuery = query.Encode()

	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal targetprocess payload: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	return req, nil
}

func (c *Client) resolveURL(path string) (*url.URL, error) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return url.Parse(path)
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("targetprocess base URL is empty")
	}
	return url.Parse(c.baseURL + "/" + strings.TrimLeft(path, "/"))
}

func buildWhereClause(projectID int, state string, since *time.Time, extraWhere string) string {
	var clauses []string

	if projectID > 0 {
		clauses = append(clauses, fmt.Sprintf("(Project.Id eq %d)", projectID))
	}

	switch state {
	case "open":
		clauses = append(clauses, "(EntityState.IsFinal eq 'false')")
	case "closed":
		clauses = append(clauses, "(EntityState.IsFinal eq 'true')")
	}

	if since != nil {
		clauses = append(clauses, fmt.Sprintf("(ModifyDate gte '%s')", since.UTC().Format("2006-01-02T15:04:05")))
	}

	if strings.TrimSpace(extraWhere) != "" {
		clauses = append(clauses, "("+strings.TrimSpace(extraWhere)+")")
	}

	if len(clauses) == 0 {
		return ""
	}

	return strings.Join(clauses, "and")
}

func buildWhereClauseV2(projectID int, state string, since *time.Time, extraWhere string) string {
	var clauses []string

	if projectID > 0 {
		clauses = append(clauses, fmt.Sprintf("(project.id=%d)", projectID))
	}

	switch state {
	case "open":
		clauses = append(clauses, "(entityState.isFinal=false)")
	case "closed":
		clauses = append(clauses, "(entityState.isFinal=true)")
	}

	if since != nil {
		clauses = append(clauses, fmt.Sprintf("(modifyDate>='%s')", since.UTC().Format("2006-01-02T15:04:05")))
	}

	if strings.TrimSpace(extraWhere) != "" {
		clauses = append(clauses, "("+strings.TrimSpace(extraWhere)+")")
	}

	if len(clauses) == 0 {
		return ""
	}

	return strings.Join(clauses, " and ")
}

func resourceForEntityType(entityType string) (string, error) {
	switch strings.ToLower(entityType) {
	case "userstory":
		return "UserStories", nil
	case "bug":
		return "Bugs", nil
	default:
		return "", fmt.Errorf("unsupported targetprocess entity type %q", entityType)
	}
}

func v2EntityPath(entity string) string {
	switch strings.ToLower(entity) {
	case "userstory":
		return "/api/v2/UserStory"
	case "bug":
		return "/api/v2/Bug"
	default:
		return "/api/v2/" + entity
	}
}

func normalizeV2Assignable(entity string, item *v2Assignable) Assignable {
	assignable := Assignable{
		ResourceType:    entity,
		ID:              item.ID,
		Name:            item.Name,
		Description:     item.Description,
		CreateDate:      item.CreateDate,
		ModifyDate:      item.ModifyDate,
		Tags:            strings.Join(item.Tags, ", "),
		NumericPriority: item.NumericPriority,
	}

	if item.EntityType != nil {
		assignable.EntityType = &EntityRef{
			ID:   item.EntityType.ID,
			Name: item.EntityType.Name,
		}
		if assignable.ResourceType == "" {
			assignable.ResourceType = item.EntityType.Name
		}
	}
	if item.EntityState != nil {
		assignable.EntityState = &EntityState{
			EntityRef: EntityRef{
				ID:   item.EntityState.ID,
				Name: item.EntityState.Name,
			},
			IsFinal:   item.EntityState.IsFinal,
			IsInitial: item.EntityState.IsInitial,
		}
	}
	if item.Project != nil {
		assignable.Project = &EntityRef{
			ID:   item.Project.ID,
			Name: item.Project.Name,
		}
	}
	if item.AssignedUser != nil {
		assignable.AssignedUser = &User{
			EntityRef: EntityRef{
				ID: item.AssignedUser.ID,
			},
			Login:     item.AssignedUser.Login,
			Email:     item.AssignedUser.Email,
			FirstName: item.AssignedUser.FirstName,
			LastName:  item.AssignedUser.LastName,
		}
	}

	return assignable
}
