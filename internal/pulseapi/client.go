package pulseapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/try-pulse/pulse-import/internal/version"
)

const (
	maxAttempts      = 4
	maxErrorBodySize = 64 << 10
	maxRetryDelay    = 30 * time.Second
)

type Client struct {
	BaseURL     string
	Token       string
	WorkspaceID string
	HTTP        *http.Client
	Backoff     func(attempt int) time.Duration
	UserAgent   string
}

func New(baseURL, token, workspaceID string) (*Client, error) {
	normalized, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL:     normalized,
		Token:       token,
		WorkspaceID: workspaceID,
		HTTP:        &http.Client{Timeout: 60 * time.Second},
		Backoff: func(attempt int) time.Duration {
			delay := time.Second << attempt
			if delay > maxRetryDelay {
				return maxRetryDelay
			}
			return delay
		},
		UserAgent: "pulse-import/" + version.Current(),
	}, nil
}

func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid Pulse API URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid Pulse API URL: scheme must be http or https")
	}
	if u.Host == "" || u.User != nil {
		return "", fmt.Errorf("invalid Pulse API URL: host is required and credentials are not allowed")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid Pulse API URL: query and fragment are not allowed")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return strings.TrimRight(u.String(), "/"), nil
}

func (c *Client) InsecureRemote() bool {
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "localhost" && host != "127.0.0.1" && host != "::1"
}

type APIError struct {
	Status     int
	Code       string
	Message    string
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("pulse api %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("pulse api %d: %s", e.Status, e.Body)
}

// IsAmbiguousWriteError reports errors for which a POST may have reached Pulse even
// though the client did not receive a definitive success response.
func IsAmbiguousWriteError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	return true
}

func (c *Client) doRaw(ctx context.Context, method, path string, body any) ([]byte, error) {
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		data, err := c.roundTrip(ctx, method, path, body, "", nil)
		if err == nil {
			return data, nil
		}
		last = err
		apiErr, ok := err.(*APIError)
		retry := ok && apiErr.Status == http.StatusTooManyRequests
		if method == http.MethodGet {
			retry = retry || !ok || apiErr.Status >= http.StatusInternalServerError
		}
		if !retry || attempt == maxAttempts-1 || ctx.Err() != nil {
			return nil, err
		}
		var retryAfter time.Duration
		if ok {
			retryAfter = apiErr.RetryAfter
		}
		if err := c.wait(ctx, attempt, retryAfter); err != nil {
			return nil, err
		}
	}
	return nil, last
}

func (c *Client) wait(ctx context.Context, attempt int, retryAfter time.Duration) error {
	d := retryAfter
	if d <= 0 {
		d = time.Second
	}
	if c.Backoff != nil {
		if fallback := c.Backoff(attempt); retryAfter <= 0 {
			d = fallback
		}
	}
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) roundTrip(ctx context.Context, method, path string, jsonBody any, contentType string, rawBody io.Reader) ([]byte, error) {
	rdr := rawBody
	if jsonBody != nil {
		b, err := json.Marshal(jsonBody)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.WorkspaceID != "" {
		req.Header.Set("X-Workspace-ID", c.WorkspaceID)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var data []byte
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, err = readBounded(resp.Body, maxErrorBodySize)
	} else {
		data, err = io.ReadAll(resp.Body)
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(data, &errBody)
		msg := errBody.Message
		if msg == "" {
			msg = errBody.Error
		}
		return nil, &APIError{
			Status:     resp.StatusCode,
			Code:       errBody.Code,
			Message:    msg,
			Body:       string(data),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}
	return data, nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) <= limit {
		return data, nil
	}
	return append(data[:limit], []byte("\n… response truncated")...), nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func decode[T any](data []byte) (T, error) {
	var out T
	if len(data) == 0 || string(data) == "null" {
		return out, fmt.Errorf("empty response")
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}

func get[T any](c *Client, ctx context.Context, path string) (T, error) {
	data, err := c.doRaw(ctx, http.MethodGet, path, nil)
	if err != nil {
		var zero T
		return zero, err
	}
	return decode[T](data)
}

func post[T any](c *Client, ctx context.Context, path string, body any) (T, error) {
	data, err := c.doRaw(ctx, http.MethodPost, path, body)
	if err != nil {
		var zero T
		return zero, err
	}
	return decode[T](data)
}

type page[T any] struct {
	Data []T `json:"data"`
}

func listAll[T any](c *Client, ctx context.Context, path string) ([]T, error) {
	var all []T
	pageNum := 1
	for {
		q := url.Values{}
		q.Set("page", fmt.Sprintf("%d", pageNum))
		q.Set("limit", "100")
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		out, err := get[page[T]](c, ctx, path+sep+q.Encode())
		if err != nil {
			return nil, err
		}
		all = append(all, out.Data...)
		if len(out.Data) < 100 {
			break
		}
		pageNum++
	}
	return all, nil
}

type User struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	DisplayName string `json:"display_name"`
}

type Workspace struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type WorkspaceMembership struct {
	Role      string     `json:"role"`
	Status    string     `json:"status"`
	Workspace *Workspace `json:"workspace"`
}

// EstimateSettings mirrors a team's estimate configuration. Pulse rejects an
// estimate that is not in the team's allowed set, so an import has to read this
// before it can map Jira story points.
type EstimateSettings struct {
	Enabled       bool   `json:"enabled"`
	ScaleType     string `json:"scale_type"`
	AllowZero     bool   `json:"allow_zero"`
	ExtendedScale bool   `json:"extended_scale"`
}

type TeamRef struct {
	ID string `json:"id"`
}

type Team struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	IconCode         string           `json:"icon_code"`
	IconColor        string           `json:"icon_color"`
	Parent           *TeamRef         `json:"parent,omitempty"`
	EstimateSettings EstimateSettings `json:"estimate_settings"`
}

// Project carries Pulse's `title` field. It is deliberately not called Name:
// the API has no `name` on a project, and reading the wrong key silently left
// every project unnamed.
type Project struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	TeamID    string  `json:"team_id"`
	MainDocID *string `json:"main_doc_id,omitempty"`
}

type TeamMember struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Role      string `json:"role"`
}

type Label struct {
	ID         string `json:"id"`
	TeamID     string `json:"team_id"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	EntityType string `json:"entity_type"`
	Archived   bool   `json:"archived"`
}

type Issue struct {
	ID           string   `json:"id"`
	Code         string   `json:"code"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Status       string   `json:"status"`
	Priority     string   `json:"priority"`
	Type         string   `json:"type"`
	TeamID       string   `json:"team_id"`
	ProjectID    *string  `json:"project_id,omitempty"`
	ParentID     *string  `json:"parent_id,omitempty"`
	AssigneeID   *string  `json:"assignee_id,omitempty"`
	MainDocID    *string  `json:"main_doc_id,omitempty"`
	BlocksIDs    []string `json:"blocks_ids,omitempty"`
	BlockedByIDs []string `json:"blocked_by_ids,omitempty"`
}

type CreateIssueRequest struct {
	Title        string     `json:"title"`
	Description  string     `json:"description,omitempty"`
	Status       string     `json:"status,omitempty"`
	Priority     string     `json:"priority,omitempty"`
	Type         string     `json:"type,omitempty"`
	TeamID       string     `json:"team_id"`
	ProjectID    *string    `json:"project_id,omitempty"`
	ParentID     *string    `json:"parent_id,omitempty"`
	AssigneeID   *string    `json:"assignee_id,omitempty"`
	TimeEstimate *int       `json:"time_estimate,omitempty"`
	DueDate      *time.Time `json:"due_date,omitempty"`
	LabelIDs     []string   `json:"label_ids,omitempty"`
}

// UpdateIssueRequest carries only the fields the importer sets after creation.
// Issue relations reference other imported issues, so they can only be applied
// once every issue in the plan has an id.
type UpdateIssueRequest struct {
	BlocksIDs    *[]string `json:"blocks_ids,omitempty"`
	BlockedByIDs *[]string `json:"blocked_by_ids,omitempty"`
}

type CreateProjectRequest struct {
	Title    string   `json:"title"`
	Status   string   `json:"status,omitempty"`
	Priority string   `json:"priority,omitempty"`
	TeamID   string   `json:"team_id"`
	LabelIDs []string `json:"label_ids,omitempty"`
}

type CreateCommentRequest struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Text       string `json:"text"`
}

type Comment struct {
	ID string `json:"id"`
}

type CreateLabelRequest struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description,omitempty"`
	EntityType  string `json:"entity_type"`
}

type AttachmentRequest struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	IsMainDoc  bool   `json:"is_main_doc,omitempty"`
}

type Document struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (c *Client) Me(ctx context.Context) (*User, error) {
	wrap, err := get[struct {
		User User `json:"user"`
	}](c, ctx, "/auth/me")
	if err != nil {
		return nil, err
	}
	if wrap.User.ID == "" {
		return nil, fmt.Errorf("auth/me: empty user")
	}
	return &wrap.User, nil
}

func (c *Client) ListMyWorkspaces(ctx context.Context) ([]WorkspaceMembership, error) {
	out, err := get[page[WorkspaceMembership]](c, ctx, "/workspaces/me")
	if err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (c *Client) ListTeams(ctx context.Context) ([]Team, error) {
	return listAll[Team](c, ctx, "/teams")
}

func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	return listAll[User](c, ctx, "/users")
}

func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	return listAll[Project](c, ctx, "/projects")
}

// ListTeamMembers returns the users Pulse will accept as assignees for a team.
// This is the correct roster for assignee mapping: unlike GET /users it is not
// gated on the workspace-admin `users:read` permission, and unlike
// GET /users/select it includes email addresses.
func (c *Client) ListTeamMembers(ctx context.Context, teamID string) ([]TeamMember, error) {
	out, err := get[struct {
		Members []TeamMember `json:"members"`
	}](c, ctx, fmt.Sprintf("/teams/%s/members", url.PathEscape(teamID)))
	if err != nil {
		return nil, err
	}
	return out.Members, nil
}

// UserOption is one entry from the workspace-wide user picker, which every
// authenticated caller may read. It carries no email, so it can only widen
// name-based matching.
type UserOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func (c *Client) ListUserOptions(ctx context.Context) ([]UserOption, error) {
	var all []UserOption
	for page := 1; ; page++ {
		out, err := get[struct {
			Data       []UserOption `json:"data"`
			Pagination struct {
				HasNext bool `json:"has_next"`
			} `json:"pagination"`
		}](c, ctx, fmt.Sprintf("/users/select?workspace_wide=true&page=%d&limit=100", page))
		if err != nil {
			return nil, err
		}
		all = append(all, out.Data...)
		if !out.Pagination.HasNext || len(out.Data) == 0 {
			return all, nil
		}
	}
}

// CountTeamIssues reports how many issues a team already holds. Pulse assigns
// issue codes sequentially per team, so identifiers only line up with Jira when
// the target team starts empty.
func (c *Client) CountTeamIssues(ctx context.Context, teamID string) (int64, error) {
	query := url.Values{}
	query.Set("limit", "1")
	query.Set("filters[team_id][operator]", "eq")
	query.Set("filters[team_id][value]", teamID)
	out, err := get[struct {
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
	}](c, ctx, "/issues?"+query.Encode())
	if err != nil {
		return 0, err
	}
	return out.Pagination.Total, nil
}

func (c *Client) ListLabels(ctx context.Context, teamID string) ([]Label, error) {
	path := fmt.Sprintf("/teams/%s/labels?archived=false&entity_type=issue", url.PathEscape(teamID))
	out, err := get[page[Label]](c, ctx, path)
	if err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (c *Client) CreateLabel(ctx context.Context, teamID string, req CreateLabelRequest) (*Label, error) {
	label, err := post[Label](c, ctx, fmt.Sprintf("/teams/%s/labels", url.PathEscape(teamID)), req)
	if err != nil {
		return nil, err
	}
	if label.ID == "" {
		return nil, fmt.Errorf("create label: empty response")
	}
	return &label, nil
}

func (c *Client) CreateIssue(ctx context.Context, req CreateIssueRequest) (*Issue, error) {
	issue, err := post[Issue](c, ctx, "/issues", req)
	if err != nil {
		return nil, err
	}
	if issue.ID == "" {
		return nil, fmt.Errorf("create issue: empty response")
	}
	return &issue, nil
}

func (c *Client) GetIssue(ctx context.Context, issueID string) (*Issue, error) {
	issue, err := get[Issue](c, ctx, "/issues/"+url.PathEscape(issueID))
	if err != nil {
		return nil, err
	}
	if issue.ID == "" {
		return nil, fmt.Errorf("get issue: empty response")
	}
	return &issue, nil
}

// UpdateIssue applies the fields that can only be set once every issue exists.
func (c *Client) UpdateIssue(ctx context.Context, issueID string, req UpdateIssueRequest) (*Issue, error) {
	data, err := c.doRaw(ctx, http.MethodPut, "/issues/"+url.PathEscape(issueID), req)
	if err != nil {
		return nil, err
	}
	issue, err := decode[Issue](data)
	if err != nil {
		return nil, err
	}
	return &issue, nil
}

// ListArchivedLabels lists a team's archived issue labels. Pulse's uniqueness
// index ignores the archived flag, so an archived label still blocks creating a
// live one with the same name and has to be discovered separately.
func (c *Client) ListArchivedLabels(ctx context.Context, teamID string) ([]Label, error) {
	path := fmt.Sprintf("/teams/%s/labels?archived=true&entity_type=issue", url.PathEscape(teamID))
	out, err := get[page[Label]](c, ctx, path)
	if err != nil {
		return nil, err
	}
	return out.Data, nil
}

// UnarchiveLabel brings an archived label back so an import can reuse it
// instead of failing on the duplicate-name conflict.
func (c *Client) UnarchiveLabel(ctx context.Context, labelID string) (*Label, error) {
	label, err := post[Label](c, ctx, fmt.Sprintf("/labels/%s/unarchive", url.PathEscape(labelID)), struct{}{})
	if err != nil {
		return nil, err
	}
	return &label, nil
}

func (c *Client) CreateProject(ctx context.Context, req CreateProjectRequest) (*Project, error) {
	wrap, err := post[struct {
		Project Project `json:"project"`
	}](c, ctx, "/projects", req)
	if err != nil {
		return nil, err
	}
	if wrap.Project.ID == "" {
		return nil, fmt.Errorf("create project: empty response")
	}
	return &wrap.Project, nil
}

func (c *Client) GetProject(ctx context.Context, projectID string) (*Project, error) {
	data, err := c.doRaw(ctx, http.MethodGet, "/projects/"+url.PathEscape(projectID), nil)
	if err != nil {
		return nil, err
	}
	// The endpoint has used both a bare project and a {"project": …} wrapper.
	if wrap, err := decode[struct {
		Project Project `json:"project"`
	}](data); err == nil && wrap.Project.ID != "" {
		return &wrap.Project, nil
	}
	project, err := decode[Project](data)
	if err != nil {
		return nil, err
	}
	if project.ID == "" {
		return nil, fmt.Errorf("get project: empty response")
	}
	return &project, nil
}

func (c *Client) CreateComment(ctx context.Context, req CreateCommentRequest) (*Comment, error) {
	wrap, err := post[struct {
		Comment Comment `json:"comment"`
	}](c, ctx, "/comments", req)
	if err != nil {
		return nil, err
	}
	if wrap.Comment.ID == "" {
		return nil, fmt.Errorf("create comment: empty response")
	}
	return &wrap.Comment, nil
}

// CountComments reports how many comments an issue already carries, so a
// resumed import can tell which of a row's comments were already posted.
func (c *Client) CountComments(ctx context.Context, issueID string) (int64, error) {
	query := url.Values{}
	query.Set("target_type", "issue")
	query.Set("target_id", issueID)
	query.Set("limit", "1")
	out, err := get[struct {
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
	}](c, ctx, "/comments?"+query.Encode())
	if err != nil {
		return 0, err
	}
	return out.Pagination.Total, nil
}

func (c *Client) DeleteIssue(ctx context.Context, issueID string) error {
	_, err := c.doRaw(ctx, http.MethodDelete, "/issues/"+url.PathEscape(issueID), nil)
	return err
}

func (c *Client) DeleteProject(ctx context.Context, projectID string) error {
	_, err := c.doRaw(ctx, http.MethodDelete, "/projects/"+url.PathEscape(projectID), nil)
	return err
}

func (c *Client) DeleteDocument(ctx context.Context, documentID string) error {
	_, err := c.doRaw(ctx, http.MethodDelete, "/content/documents/"+url.PathEscape(documentID), nil)
	return err
}

// IsNotFound reports a 404, which rollback treats as "already gone".
func IsNotFound(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusNotFound
	}
	return false
}

// IsConflict reports a 409, used to detect a duplicate label name.
func IsConflict(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusConflict
	}
	return false
}

// IsForbidden reports a 403, which the importer turns into an actionable
// permissions message instead of a raw API error.
func IsForbidden(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusForbidden
	}
	return false
}

// UploadMainDoc attaches a Plate document to an entity as its main document.
// entityType is "issue" or "project".
func (c *Client) UploadMainDoc(ctx context.Context, entityType, entityID, title string, plateJSON []byte) (*Document, error) {
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		doc, err := c.uploadMainDocOnce(ctx, entityType, entityID, title, plateJSON)
		if err == nil {
			return doc, nil
		}
		last = err
		apiErr, ok := err.(*APIError)
		if !ok || apiErr.Status != 429 || attempt == maxAttempts-1 {
			return nil, err
		}
		if err := c.wait(ctx, attempt, apiErr.RetryAfter); err != nil {
			return nil, err
		}
	}
	return nil, last
}

// mainDocContentType must match what the Pulse clients upload for a native
// document. Pulse decides whether a document can be opened in the Plate editor
// from its stored content_type, and only `text/plain` and `application/json`
// qualify — anything else (including multipart's default
// application/octet-stream) is treated as an opaque file to download, which
// would make every imported description unopenable in the product.
const mainDocContentType = "text/plain"

func (c *Client) uploadMainDocOnce(ctx context.Context, entityType, entityID, title string, plateJSON []byte) (*Document, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(
		`form-data; name="file"; filename=%q`, sanitizeFileName(title),
	))
	header.Set("Content-Type", mainDocContentType)
	part, err := w.CreatePart(header)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(plateJSON); err != nil {
		return nil, err
	}
	_ = w.WriteField("title", TruncateForAPI(title, MaxTitleBytes))
	atts, _ := json.Marshal([]AttachmentRequest{{
		EntityType: entityType, EntityID: entityID, IsMainDoc: true,
	}})
	_ = w.WriteField("attachments", string(atts))
	if err := w.Close(); err != nil {
		return nil, err
	}

	data, err := c.roundTrip(ctx, http.MethodPost, "/content/documents/upload", nil, w.FormDataContentType(), &buf)
	if err != nil {
		return nil, err
	}

	if doc, err := decode[Document](data); err == nil && doc.ID != "" {
		return &doc, nil
	}
	if wrap, err := decode[struct {
		Content Document `json:"content"`
	}](data); err == nil && wrap.Content.ID != "" {
		return &wrap.Content, nil
	}
	return nil, fmt.Errorf("upload main doc: response missing document id")
}

func sanitizeFileName(title string) string {
	b := strings.Builder{}
	for _, r := range title {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	s := b.String()
	if s == "" {
		s = "document"
	}
	// `.txt` mirrors the Pulse clients: a native document is Plate JSON stored
	// in a text file, and the extension is part of how it is recognised.
	return truncateRunes(s, 80) + ".txt"
}

// Pulse's field limits are counted in BYTES on the server (len(string), not a
// rune count), even though request binding validates rune counts. Truncating on
// runes therefore still produces a 400 for non-Latin text, where one character
// costs two to four bytes.
const (
	MaxTitleBytes = 200
	MaxLabelBytes = 50
	MaxTextBytes  = 4000
)

// TruncateForAPI shortens s to at most limit bytes without splitting a rune.
func TruncateForAPI(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// ExceedsAPIBytes reports whether s is longer than Pulse's byte limit.
func ExceedsAPIBytes(s string, limit int) bool {
	return len(s) > limit
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
